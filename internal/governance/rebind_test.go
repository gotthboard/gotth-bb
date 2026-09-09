package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRebindExternalIdentityCommitsLocksAndExactResult(t *testing.T) {
	t.Parallel()

	tx := &rebindTestTx{}
	result, err := RebindExternalIdentity(
		context.Background(), rebindTestBeginner{tx: tx},
		func() time.Time { return time.Date(2026, time.September, 8, 20, 0, 0, 123456789, time.UTC) },
		"https://shared.example/application/o/gotth-bb/", "old-subject",
		"https://auth.board.example/application/o/gotth-bb/", "new-subject",
		"operator@example.test", pgtype.UUID{Bytes: [16]byte{0x31}, Valid: true},
	)
	want := IdentityRebindResult{UserID: 41, AuditID: 73, RevokedSessions: 2, DiscardedLoginAttempts: 3}
	if err != nil || result != want {
		t.Fatalf("RebindExternalIdentity() = (%+v, %v), want (%+v, nil)", result, err, want)
	}
	if tx.queryCalls != 3 || !tx.commitCalled || tx.rollbackCalled {
		t.Fatalf("transaction = (queries %d, commit %t, rollback %t)", tx.queryCalls, tx.commitCalled, tx.rollbackCalled)
	}
	if tx.locked[0] != [2]string{"https://auth.board.example/application/o/gotth-bb/", "new-subject"} ||
		tx.locked[1] != [2]string{"https://shared.example/application/o/gotth-bb/", "old-subject"} {
		t.Fatalf("identity lock order = %#v", tx.locked)
	}
}

func TestRebindExternalIdentityRejectsInvalidInputBeforeTransaction(t *testing.T) {
	t.Parallel()

	validClock := func() time.Time { return time.Date(2026, time.September, 8, 20, 5, 0, 0, time.UTC) }
	validRequestID := pgtype.UUID{Bytes: [16]byte{0x32}, Valid: true}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name                              string
		ctx                               context.Context
		beginner                          transactionBeginner
		clock                             func() time.Time
		oldIssuer, oldSubject             string
		newIssuer, newSubject, operatorID string
		requestID                         pgtype.UUID
		wantCause                         error
	}{
		{name: "nil context", beginner: panicBootstrapBeginner{}, clock: validClock, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator", requestID: validRequestID},
		{name: "nil beginner", ctx: context.Background(), clock: validClock, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator", requestID: validRequestID},
		{name: "nil clock", ctx: context.Background(), beginner: panicBootstrapBeginner{}, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator", requestID: validRequestID},
		{name: "empty old issuer", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator", requestID: validRequestID},
		{name: "empty new subject", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", operatorID: "operator", requestID: validRequestID},
		{name: "unchanged", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, oldIssuer: "same", oldSubject: "subject", newIssuer: "same", newSubject: "subject", operatorID: "operator", requestID: validRequestID},
		{name: "control operator", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator\n", requestID: validRequestID},
		{name: "invalid request ID", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: validClock, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator"},
		{name: "canceled", ctx: canceledContext, beginner: panicBootstrapBeginner{}, clock: validClock, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator", requestID: validRequestID, wantCause: context.Canceled},
		{name: "zero clock", ctx: context.Background(), beginner: panicBootstrapBeginner{}, clock: func() time.Time { return time.Time{} }, oldIssuer: "old", oldSubject: "old-subject", newIssuer: "new", newSubject: "new-subject", operatorID: "operator", requestID: validRequestID},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := RebindExternalIdentity(
				test.ctx, test.beginner, test.clock,
				test.oldIssuer, test.oldSubject, test.newIssuer, test.newSubject,
				test.operatorID, test.requestID,
			)
			if err == nil || got != (IdentityRebindResult{}) || test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("RebindExternalIdentity() = (%+v, %v)", got, err)
			}
		})
	}
}

func TestRebindExternalIdentityRollsBackEveryTransactionalFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("stage failed")
	for _, failure := range []string{"first lock", "false first lock", "second lock", "false second lock", "rebind", "denied", "invalid result", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &rebindTestTx{failure: failure, cause: cause}
			got, err := RebindExternalIdentity(
				context.Background(), rebindTestBeginner{tx: tx}, time.Now,
				"old", "old-subject", "new", "new-subject", "operator",
				pgtype.UUID{Bytes: [16]byte{0x33}, Valid: true},
			)
			if err == nil || got != (IdentityRebindResult{}) || !tx.rollbackCalled {
				t.Fatalf("failure %q = (%+v, %v, rollback %t)", failure, got, err, tx.rollbackCalled)
			}
			if failure == "denied" && !errors.Is(err, ErrExternalIdentityRebindDenied) {
				t.Fatalf("failure %q error = %v, want denied", failure, err)
			}
			if failure == "first lock" || failure == "second lock" || failure == "rebind" || failure == "commit" {
				if !errors.Is(err, cause) {
					t.Fatalf("failure %q error = %v, want cause", failure, err)
				}
			}
		})
	}
}

type rebindTestBeginner struct{ tx *rebindTestTx }

func (beginner rebindTestBeginner) Begin(context.Context) (pgx.Tx, error) { return beginner.tx, nil }

type rebindTestTx struct {
	pgx.Tx
	failure        string
	cause          error
	queryCalls     int
	locked         [2][2]string
	commitCalled   bool
	rollbackCalled bool
}

func (tx *rebindTestTx) QueryRow(_ context.Context, _ string, arguments ...any) pgx.Row {
	tx.queryCalls++
	switch tx.queryCalls {
	case 1, 2:
		index := tx.queryCalls - 1
		tx.locked[index] = [2]string{arguments[0].(string), arguments[1].(string)}
		return bootstrapTestRow{scan: func(destinations ...any) error {
			if tx.failure == []string{"first lock", "second lock"}[index] {
				return tx.cause
			}
			*(destinations[0].(*bool)) = tx.failure != []string{"false first lock", "false second lock"}[index]
			return nil
		}}
	case 3:
		return bootstrapTestRow{scan: func(destinations ...any) error {
			if tx.failure == "rebind" {
				return tx.cause
			}
			if tx.failure == "denied" {
				return pgx.ErrNoRows
			}
			userID, auditID := int64(41), int64(73)
			if tx.failure == "invalid result" {
				auditID = 0
			}
			*(destinations[0].(*int64)) = userID
			*(destinations[1].(*int64)) = auditID
			*(destinations[2].(*int64)) = 2
			*(destinations[3].(*int64)) = 3
			return nil
		}}
	default:
		panic("unexpected external identity rebind query")
	}
}

func (tx *rebindTestTx) Commit(context.Context) error {
	tx.commitCalled = true
	if tx.failure == "commit" {
		return tx.cause
	}
	return nil
}

func (tx *rebindTestTx) Rollback(context.Context) error {
	tx.rollbackCalled = true
	return nil
}
