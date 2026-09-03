package store

import (
	"context"
	"errors"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

type stubTransaction struct {
	pgx.Tx
	commitErr      error
	rollbackErr    error
	commitCalled   bool
	rollbackCalled bool
	rollbackCtxErr error
}

func (tx *stubTransaction) Commit(context.Context) error {
	tx.commitCalled = true
	return tx.commitErr
}

func (tx *stubTransaction) Rollback(ctx context.Context) error {
	tx.rollbackCalled = true
	tx.rollbackCtxErr = ctx.Err()
	return tx.rollbackErr
}

type stubTransactionBeginner struct {
	tx     pgx.Tx
	err    error
	called bool
}

func (beginner *stubTransactionBeginner) Begin(context.Context) (pgx.Tx, error) {
	beginner.called = true
	return beginner.tx, beginner.err
}

func TestWithinTxCommitsSuccessfulAction(t *testing.T) {
	t.Parallel()

	tx := &stubTransaction{}
	beginner := &stubTransactionBeginner{tx: tx}
	actionCalled := false
	err := WithinTx(context.Background(), beginner, func(queries *db.Queries) error {
		actionCalled = queries != nil
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTx() returned error: %v", err)
	}
	if !beginner.called || !actionCalled || !tx.commitCalled || tx.rollbackCalled {
		t.Fatalf("transaction = (begin %t, action %t, commit %t, rollback %t), want (true, true, true, false)", beginner.called, actionCalled, tx.commitCalled, tx.rollbackCalled)
	}
}

func TestWithinTxRejectsInvalidInputsBeforeBegin(t *testing.T) {
	t.Parallel()

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		beginner *stubTransactionBeginner
		action   func(*db.Queries) error
		cause    error
	}{
		{name: "nil context", beginner: &stubTransactionBeginner{}, action: func(*db.Queries) error { return nil }},
		{name: "nil beginner", ctx: context.Background(), action: func(*db.Queries) error { return nil }},
		{name: "nil action", ctx: context.Background(), beginner: &stubTransactionBeginner{}},
		{name: "canceled context", ctx: canceledContext, beginner: &stubTransactionBeginner{}, action: func(*db.Queries) error { return nil }, cause: context.Canceled},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var beginner interface {
				Begin(context.Context) (pgx.Tx, error)
			} = test.beginner
			if test.beginner == nil {
				beginner = nil
			}
			err := WithinTx(test.ctx, beginner, test.action)
			if err == nil {
				t.Fatal("WithinTx() returned nil error")
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("WithinTx() error = %v, want cause %v", err, test.cause)
			}
			if test.beginner != nil && test.beginner.called {
				t.Fatal("Begin() called for invalid input")
			}
		})
	}
}

func TestWithinTxRollsBackAndPreservesFailures(t *testing.T) {
	t.Parallel()

	beginFailure := errors.New("begin failure")
	actionFailure := errors.New("action failure")
	commitFailure := errors.New("commit failure")
	rollbackFailure := errors.New("rollback failure")
	tests := []struct {
		name         string
		beginner     *stubTransactionBeginner
		action       func(*db.Queries) error
		wantCause    error
		wantSecond   error
		wantCommit   bool
		wantRollback bool
	}{
		{name: "begin failure", beginner: &stubTransactionBeginner{err: beginFailure}, action: func(*db.Queries) error { return nil }, wantCause: beginFailure},
		{name: "transaction plus begin failure", beginner: &stubTransactionBeginner{tx: &stubTransaction{}, err: beginFailure}, action: func(*db.Queries) error { return nil }, wantCause: beginFailure, wantRollback: true},
		{name: "nil transaction", beginner: &stubTransactionBeginner{}, action: func(*db.Queries) error { return nil }, wantCause: errNilTransaction},
		{name: "action failure", beginner: &stubTransactionBeginner{tx: &stubTransaction{}}, action: func(*db.Queries) error { return actionFailure }, wantCause: actionFailure, wantRollback: true},
		{name: "commit failure", beginner: &stubTransactionBeginner{tx: &stubTransaction{commitErr: commitFailure, rollbackErr: pgx.ErrTxClosed}}, action: func(*db.Queries) error { return nil }, wantCause: commitFailure, wantCommit: true, wantRollback: true},
		{name: "action and rollback failure", beginner: &stubTransactionBeginner{tx: &stubTransaction{rollbackErr: rollbackFailure}}, action: func(*db.Queries) error { return actionFailure }, wantCause: actionFailure, wantSecond: rollbackFailure, wantRollback: true},
		{name: "commit and rollback failure", beginner: &stubTransactionBeginner{tx: &stubTransaction{commitErr: commitFailure, rollbackErr: rollbackFailure}}, action: func(*db.Queries) error { return nil }, wantCause: commitFailure, wantSecond: rollbackFailure, wantCommit: true, wantRollback: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := WithinTx(context.Background(), test.beginner, test.action)
			if err == nil || !errors.Is(err, test.wantCause) {
				t.Fatalf("WithinTx() error = %v, want cause %v", err, test.wantCause)
			}
			if test.wantSecond != nil && !errors.Is(err, test.wantSecond) {
				t.Fatalf("WithinTx() error = %v, want second cause %v", err, test.wantSecond)
			}
			tx, _ := test.beginner.tx.(*stubTransaction)
			if tx != nil && (tx.commitCalled != test.wantCommit || tx.rollbackCalled != test.wantRollback) {
				t.Fatalf("transaction = (commit %t, rollback %t), want (%t, %t)", tx.commitCalled, tx.rollbackCalled, test.wantCommit, test.wantRollback)
			}
		})
	}
}

func TestWithinTxDetachesRollbackFromCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	tx := &stubTransaction{}
	err := WithinTx(ctx, &stubTransactionBeginner{tx: tx}, func(*db.Queries) error {
		cancel()
		return context.Canceled
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("WithinTx() error = %v, want context.Canceled", err)
	}
	if !tx.rollbackCalled || tx.rollbackCtxErr != nil {
		t.Fatalf("rollback = (called %t, context %v), want (true, nil)", tx.rollbackCalled, tx.rollbackCtxErr)
	}
}
