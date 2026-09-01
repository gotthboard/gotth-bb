package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type stubMigrationLockRow struct {
	unlocked bool
	err      error
}

func (row stubMigrationLockRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	*dest[0].(*bool) = row.unlocked
	return nil
}

type stubMigrationLockConnection struct {
	execContext   context.Context
	execStatement string
	execArguments []any
	execErr       error
	rowContext    context.Context
	rowContextErr error
	rowStatement  string
	rowArguments  []any
	row           pgx.Row
}

func (connection *stubMigrationLockConnection) Exec(ctx context.Context, statement string, arguments ...any) (pgconn.CommandTag, error) {
	connection.execContext = ctx
	connection.execStatement = statement
	connection.execArguments = arguments
	return pgconn.CommandTag{}, connection.execErr
}

func (connection *stubMigrationLockConnection) QueryRow(ctx context.Context, statement string, arguments ...any) pgx.Row {
	connection.rowContext = ctx
	connection.rowContextErr = ctx.Err()
	connection.rowStatement = statement
	connection.rowArguments = arguments
	return connection.row
}

func TestWithMigrationLockAcquiresRunsAndReleases(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	connection := &stubMigrationLockConnection{row: stubMigrationLockRow{unlocked: true}}
	actionCalled := false
	err := withMigrationLock(ctx, connection, func() error {
		actionCalled = true
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("withMigrationLock() returned error: %v", err)
	}
	if !actionCalled {
		t.Fatal("withMigrationLock() did not call action")
	}
	if connection.execStatement != acquireMigrationLockSQL || len(connection.execArguments) != 1 || connection.execArguments[0] != migrationAdvisoryLockKey {
		t.Fatalf("acquire = (%q, %+v), want exact advisory lock", connection.execStatement, connection.execArguments)
	}
	if connection.rowStatement != releaseMigrationLockSQL || len(connection.rowArguments) != 1 || connection.rowArguments[0] != migrationAdvisoryLockKey {
		t.Fatalf("release = (%q, %+v), want exact advisory unlock", connection.rowStatement, connection.rowArguments)
	}
	if connection.rowContext == nil || connection.rowContextErr != nil {
		t.Fatalf("unlock context error at query = %v, want cancellation-detached cleanup", connection.rowContextErr)
	}
}

func TestWithMigrationLockRejectsInvalidInputsAndPreservesFailures(t *testing.T) {
	t.Parallel()

	acquireFailure := errors.New("acquire failure")
	actionFailure := errors.New("action failure")
	unlockFailure := errors.New("unlock failure")
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		ctx        context.Context
		connection *stubMigrationLockConnection
		action     func() error
		wantCause  error
		wantSecond error
	}{
		{name: "nil context", connection: &stubMigrationLockConnection{}, action: func() error { return nil }},
		{name: "nil connection", ctx: context.Background(), action: func() error { return nil }},
		{name: "nil action", ctx: context.Background(), connection: &stubMigrationLockConnection{}},
		{name: "canceled context", ctx: canceledContext, connection: &stubMigrationLockConnection{}, action: func() error { return nil }, wantCause: context.Canceled},
		{name: "acquire failure", ctx: context.Background(), connection: &stubMigrationLockConnection{execErr: acquireFailure}, action: func() error { return nil }, wantCause: acquireFailure},
		{name: "action failure", ctx: context.Background(), connection: &stubMigrationLockConnection{row: stubMigrationLockRow{unlocked: true}}, action: func() error { return actionFailure }, wantCause: actionFailure},
		{name: "unlock rejected", ctx: context.Background(), connection: &stubMigrationLockConnection{row: stubMigrationLockRow{}}, action: func() error { return nil }},
		{name: "unlock failure", ctx: context.Background(), connection: &stubMigrationLockConnection{row: stubMigrationLockRow{err: unlockFailure}}, action: func() error { return actionFailure }, wantCause: actionFailure, wantSecond: unlockFailure},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var connection migrationLockConnection = test.connection
			if test.connection == nil {
				connection = nil
			}
			err := withMigrationLock(test.ctx, connection, test.action)
			if err == nil {
				t.Fatal("withMigrationLock() returned nil error")
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("withMigrationLock() error = %v, want cause %v", err, test.wantCause)
			}
			if test.wantSecond != nil && !errors.Is(err, test.wantSecond) {
				t.Fatalf("withMigrationLock() error = %v, want second cause %v", err, test.wantSecond)
			}
		})
	}
}
