package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestBootstrapAdministratorCommitsExactGovernedGrant(t *testing.T) {
	t.Parallel()

	tx := &bootstrapTestTx{}
	result, err := BootstrapAdministrator(
		context.Background(), bootstrapTestBeginner{tx: tx},
		func() time.Time { return time.Date(2026, time.September, 1, 23, 0, 0, 123456789, time.UTC) },
		"https://auth.example/application/o/gotth-bb/", "subject-1", "operator@example.test",
		pgtype.UUID{Bytes: [16]byte{0x10, 0x20}, Valid: true},
	)
	if err != nil || result != (BootstrapResult{UserID: 41, AuditID: 73}) {
		t.Fatalf("bootstrapAdministrator() = (%+v, %v)", result, err)
	}
	if tx.queryCalls != 4 || !tx.commitCalled || tx.rollbackCalled {
		t.Fatalf("transaction = (queries %d, commit %t, rollback %t)", tx.queryCalls, tx.commitCalled, tx.rollbackCalled)
	}
}

func TestBootstrapAdministratorRejectsInvalidInputBeforeTransaction(t *testing.T) {
	t.Parallel()

	validClock := func() time.Time { return time.Date(2026, time.September, 1, 23, 5, 0, 0, time.UTC) }
	validRequestID := pgtype.UUID{Bytes: [16]byte{0x10}, Valid: true}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name       string
		ctx        context.Context
		beginner   transactionBeginner
		clock      func() time.Time
		issuer     string
		subject    string
		operatorID string
		requestID  pgtype.UUID
		wantCause  error
	}{
		{name: "nil context", beginner: panicBootstrapBeginner{}, clock: validClock, issuer: "issuer", subject: "subject", operatorID: "operator", requestID: validRequestID},
		{name: "nil beginner", ctx: context.Background(), clock: validClock, issuer: "issuer", subject: "subject", operatorID: "operator", requestID: validRequestID},
		{name: "nil clock", ctx: context.Background(), beginner: panicBootstrapBeginner{}, issuer: "issuer", subject: "subject", operatorID: "operator", requestID: validRequestID},
		{name: "empty issuer", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, subject: "subject", operatorID: "operator", requestID: validRequestID},
		{name: "invalid UTF-8 issuer", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, issuer: string([]byte{0xff}), subject: "subject", operatorID: "operator", requestID: validRequestID},
		{name: "control subject", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, issuer: "issuer", subject: "subject\n", operatorID: "operator", requestID: validRequestID},
		{name: "empty operator", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, issuer: "issuer", subject: "subject", requestID: validRequestID},
		{name: "invalid request ID", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, issuer: "issuer", subject: "subject", operatorID: "operator"},
		{name: "zero request ID", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, issuer: "issuer", subject: "subject", operatorID: "operator", requestID: pgtype.UUID{Valid: true}},
		{name: "canceled context", ctx: canceledContext, beginner: panicBootstrapBeginner{}, clock: validClock, issuer: "issuer", subject: "subject", operatorID: "operator", requestID: validRequestID, wantCause: context.Canceled},
		{name: "zero clock", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: func() time.Time { return time.Time{} }, issuer: "issuer", subject: "subject", operatorID: "operator", requestID: validRequestID},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := BootstrapAdministrator(
				test.ctx, test.beginner, test.clock, test.issuer, test.subject, test.operatorID, test.requestID,
			)
			if err == nil || got != (BootstrapResult{}) || test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("bootstrapAdministrator() = (%+v, %v)", got, err)
			}
		})
	}
}

func TestBootstrapAdministratorRollsBackEveryTransactionalFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("stage failed")
	for _, failure := range []string{
		"governance lock query", "false governance lock", "administrator count query", "existing administrator",
		"identity query", "invalid identity", "bootstrap query", "invalid bootstrap result", "commit",
	} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &bootstrapTestTx{failure: failure, cause: cause}
			got, err := BootstrapAdministrator(
				context.Background(), bootstrapTestBeginner{tx: tx},
				func() time.Time { return time.Date(2026, time.September, 1, 23, 10, 0, 0, time.UTC) },
				"issuer", "subject", "operator", pgtype.UUID{Bytes: [16]byte{0x10}, Valid: true},
			)
			if err == nil || got != (BootstrapResult{}) || !tx.rollbackCalled {
				t.Fatalf("failure %q = (%+v, %v, rollback %t)", failure, got, err, tx.rollbackCalled)
			}
			if tx.commitCalled != (failure == "commit") {
				t.Fatalf("failure %q commit = %t", failure, tx.commitCalled)
			}
			if stringsHasSuffix(failure, "query") || failure == "commit" {
				if !errors.Is(err, cause) {
					t.Fatalf("failure %q error = %v, want cause %v", failure, err, cause)
				}
			}
		})
	}
}

func stringsHasSuffix(value, suffix string) bool {
	return len(value) >= len(suffix) && value[len(value)-len(suffix):] == suffix
}

type panicBootstrapBeginner struct{}

func (panicBootstrapBeginner) Begin(context.Context) (pgx.Tx, error) {
	panic("transaction must not begin")
}

type bootstrapTestBeginner struct {
	tx *bootstrapTestTx
}

func (beginner bootstrapTestBeginner) Begin(context.Context) (pgx.Tx, error) {
	return beginner.tx, nil
}

type bootstrapTestRow struct {
	scan func(...any) error
}

func (row bootstrapTestRow) Scan(destinations ...any) error {
	return row.scan(destinations...)
}

type bootstrapTestTx struct {
	pgx.Tx
	failure        string
	cause          error
	queryCalls     int
	commitCalled   bool
	rollbackCalled bool
}

func (tx *bootstrapTestTx) QueryRow(context.Context, string, ...any) pgx.Row {
	tx.queryCalls++
	switch tx.queryCalls {
	case 1:
		return bootstrapTestRow{scan: func(destinations ...any) error {
			if tx.failure == "governance lock query" {
				return tx.cause
			}
			*(destinations[0].(*bool)) = tx.failure != "false governance lock"
			return nil
		}}
	case 2:
		return bootstrapTestRow{scan: func(destinations ...any) error {
			if tx.failure == "administrator count query" {
				return tx.cause
			}
			if tx.failure == "existing administrator" {
				*(destinations[0].(*int64)) = 1
			}
			return nil
		}}
	case 3:
		return bootstrapTestRow{scan: func(destinations ...any) error {
			if tx.failure == "identity query" {
				return tx.cause
			}
			userID := int64(41)
			if tx.failure == "invalid identity" {
				userID = 0
			}
			*(destinations[0].(*int64)) = userID
			return nil
		}}
	case 4:
		return bootstrapTestRow{scan: func(destinations ...any) error {
			if tx.failure == "bootstrap query" {
				return tx.cause
			}
			userID, auditID := int64(41), int64(73)
			if tx.failure == "invalid bootstrap result" {
				auditID = 0
			}
			*(destinations[0].(*int64)) = userID
			*(destinations[1].(*int64)) = auditID
			return nil
		}}
	default:
		panic("unexpected governance query")
	}
}

func (tx *bootstrapTestTx) Commit(context.Context) error {
	tx.commitCalled = true
	if tx.failure == "commit" {
		return tx.cause
	}
	return nil
}

func (tx *bootstrapTestTx) Rollback(context.Context) error {
	tx.rollbackCalled = true
	return nil
}
