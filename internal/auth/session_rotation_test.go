package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRotateRevalidatedSessionRejectsInvalidInputBeforeEntropyOrTransaction(t *testing.T) {
	t.Parallel()

	validClaims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	validClock := func() time.Time { return time.Date(2026, time.September, 1, 21, 0, 0, 0, time.UTC) }
	validToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, sessionTokenBytes))
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name        string
		ctx         context.Context
		beginner    transactionBeginner
		entropy     io.Reader
		clock       func() time.Time
		maximumAge  time.Duration
		idleTimeout time.Duration
		sessionID   int64
		oldToken    string
		claims      verifiedIdentityClaims
		wantCause   error
	}{
		{name: "nil context", beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "nil beginner", ctx: context.Background(), entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "nil entropy", ctx: context.Background(), beginner: panicTransactionBeginner{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "nil clock", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "zero maximum age", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "subprecision maximum age", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Microsecond - time.Nanosecond, idleTimeout: time.Nanosecond, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "subprecision idle timeout", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Second, idleTimeout: time.Nanosecond, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "zero idle timeout", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "idle exceeds maximum age", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Minute, idleTimeout: time.Hour, sessionID: 7, oldToken: validToken, claims: validClaims},
		{name: "zero session ID", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, oldToken: validToken, claims: validClaims},
		{name: "incomplete claims", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken},
		{name: "short old token", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: "short", claims: validClaims},
		{name: "malformed old token", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken[:len(validToken)-1] + "=", claims: validClaims},
		{name: "canceled context", ctx: canceledContext, beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: validClock, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken, claims: validClaims, wantCause: context.Canceled},
		{name: "zero clock", ctx: context.Background(), beginner: panicTransactionBeginner{}, entropy: panicReader{}, clock: func() time.Time { return time.Time{} }, maximumAge: time.Hour, idleTimeout: time.Minute, sessionID: 7, oldToken: validToken, claims: validClaims},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := rotateRevalidatedSession(
				test.ctx, test.beginner, test.entropy, test.clock, test.maximumAge,
				test.idleTimeout, test.sessionID, test.oldToken, test.claims,
			)
			if err == nil || got != (createdRevalidatedSession{}) {
				t.Fatalf("rotateRevalidatedSession() = (%+v, %v), want zero/error", got, err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
}

type rotationFunctionRow struct {
	scan func(...any) error
}

func (row rotationFunctionRow) Scan(destinations ...any) error {
	return row.scan(destinations...)
}

type rotationFunctionTx struct {
	pgx.Tx
	failure        string
	cause          error
	queryCalls     int
	execCalls      int
	revokeRows     int64
	commitCalled   bool
	rollbackCalled bool
}

func (tx *rotationFunctionTx) QueryRow(context.Context, string, ...any) pgx.Row {
	tx.queryCalls++
	switch tx.queryCalls {
	case 1:
		return rotationFunctionRow{scan: func(destinations ...any) error {
			if tx.failure == "active query" {
				return tx.cause
			}
			userID := int64(17)
			issuer, subject := "issuer", "subject"
			expiresAt := pgtype.Timestamptz{Time: time.Date(2026, time.September, 1, 23, 0, 0, 0, time.UTC), Valid: true}
			if tx.failure == "incomplete active" {
				userID = 0
			}
			if tx.failure == "identity mismatch" {
				subject = "other-subject"
			}
			*(destinations[0].(*int64)) = userID
			*(destinations[1].(*string)) = issuer
			*(destinations[2].(*string)) = subject
			*(destinations[3].(*pgtype.Timestamptz)) = expiresAt
			return nil
		}}
	case 2:
		return rotationFunctionRow{scan: func(destinations ...any) error {
			if tx.failure == "profile update" {
				return tx.cause
			}
			userID := int64(17)
			if tx.failure == "user mismatch" {
				userID = 18
			}
			*(destinations[0].(*int64)) = userID
			return nil
		}}
	case 3:
		return rotationFunctionRow{scan: func(destinations ...any) error {
			if tx.failure == "session insert" {
				return tx.cause
			}
			sessionID, userID := int64(29), int64(17)
			if tx.failure == "invalid replacement" {
				sessionID = 0
			}
			*(destinations[0].(*int64)) = sessionID
			*(destinations[2].(*int64)) = userID
			return nil
		}}
	default:
		panic("unexpected rotation query")
	}
}

func (tx *rotationFunctionTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	tx.execCalls++
	if tx.execCalls == 1 && tx.failure == "identity update" {
		return pgconn.CommandTag{}, tx.cause
	}
	if tx.execCalls == 2 && tx.failure == "session revoke" {
		return pgconn.CommandTag{}, tx.cause
	}
	rows := int64(1)
	if tx.execCalls == 2 {
		rows = tx.revokeRows
	}
	switch rows {
	case 0:
		return pgconn.NewCommandTag("UPDATE 0"), nil
	case 1:
		return pgconn.NewCommandTag("UPDATE 1"), nil
	default:
		return pgconn.NewCommandTag("UPDATE 2"), nil
	}
}

func (tx *rotationFunctionTx) Commit(context.Context) error {
	tx.commitCalled = true
	if tx.failure == "commit" {
		return tx.cause
	}
	return nil
}

func (tx *rotationFunctionTx) Rollback(context.Context) error {
	tx.rollbackCalled = true
	return nil
}

type rotationFunctionBeginner struct {
	tx *rotationFunctionTx
}

func (beginner rotationFunctionBeginner) Begin(context.Context) (pgx.Tx, error) {
	return beginner.tx, nil
}

func TestRotateRevalidatedSessionRollsBackEveryTransactionalFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("stage failed")
	oldToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, sessionTokenBytes))
	claims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	for _, failure := range []string{
		"active query", "incomplete active", "identity mismatch", "profile update", "user mismatch",
		"identity update", "session insert", "invalid replacement", "session revoke", "zero revoke", "two revokes", "commit",
	} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			txFailure := failure
			revokeRows := int64(1)
			switch failure {
			case "zero revoke":
				txFailure, revokeRows = "", 0
			case "two revokes":
				txFailure, revokeRows = "", 2
			}
			tx := &rotationFunctionTx{failure: txFailure, cause: cause, revokeRows: revokeRows}
			got, err := rotateRevalidatedSession(
				context.Background(), rotationFunctionBeginner{tx: tx},
				bytes.NewReader(bytes.Repeat([]byte{0x53}, sessionTokenBytes)),
				func() time.Time { return time.Date(2026, time.September, 1, 22, 0, 0, 0, time.UTC) },
				time.Hour, time.Minute, 7, oldToken, claims,
			)
			if err == nil || got != (createdRevalidatedSession{}) || !tx.rollbackCalled {
				t.Fatalf("failure %q = (%+v, %v, rollback %t), want zero/error/true", failure, got, err, tx.rollbackCalled)
			}
			if tx.commitCalled != (failure == "commit") {
				t.Fatalf("failure %q commit = %t", failure, tx.commitCalled)
			}
			if failure == "active query" || failure == "profile update" || failure == "identity update" ||
				failure == "session insert" || failure == "session revoke" || failure == "commit" {
				if !errors.Is(err, cause) {
					t.Fatalf("failure %q error = %v, want cause %v", failure, err, cause)
				}
			}
		})
	}
}

func TestRotateRevalidatedSessionReturnsNoCredentialWhenEntropyOrBeginFails(t *testing.T) {
	t.Parallel()

	claims := verifiedIdentityClaims{issuer: "issuer", subject: "subject", displayName: "Member"}
	clock := func() time.Time { return time.Date(2026, time.September, 1, 21, 5, 0, 0, time.UTC) }
	oldToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, sessionTokenBytes))
	entropyCause := errors.New("entropy unavailable")
	got, err := rotateRevalidatedSession(
		context.Background(), panicTransactionBeginner{}, errReader{cause: entropyCause}, clock,
		time.Hour, time.Minute, 7, oldToken, claims,
	)
	if !errors.Is(err, entropyCause) || got != (createdRevalidatedSession{}) {
		t.Fatalf("entropy failure = (%+v, %v), want zero/%v", got, err, entropyCause)
	}

	beginCause := errors.New("database unavailable")
	beginner := &failingTransactionBeginner{cause: beginCause}
	got, err = rotateRevalidatedSession(
		context.Background(), beginner, bytes.NewReader(bytes.Repeat([]byte{0x43}, sessionTokenBytes)), clock,
		time.Hour, time.Minute, 7, oldToken, claims,
	)
	if !beginner.called || !errors.Is(err, beginCause) || got != (createdRevalidatedSession{}) {
		t.Fatalf("begin failure = (called %t, %+v, %v), want true/zero/%v", beginner.called, got, err, beginCause)
	}
}
