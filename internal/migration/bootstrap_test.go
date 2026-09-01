package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type stubMigrationExecer struct {
	called    bool
	statement string
	err       error
}

func (execer *stubMigrationExecer) Exec(_ context.Context, statement string, _ ...any) (pgconn.CommandTag, error) {
	execer.called = true
	execer.statement = statement
	return pgconn.CommandTag{}, execer.err
}

func TestEnsureHistoryTableExecutesExactDefinition(t *testing.T) {
	t.Parallel()

	execer := &stubMigrationExecer{}
	if err := ensureHistoryTable(context.Background(), execer); err != nil {
		t.Fatalf("ensureHistoryTable() returned error: %v", err)
	}
	if execer.statement != createHistoryTableSQL {
		t.Fatalf("statement = %q, want exact history-table definition", execer.statement)
	}
}

func TestEnsureHistoryTableRejectsInvalidInputsAndExecutionFailure(t *testing.T) {
	t.Parallel()

	executionFailure := errors.New("execution failure")
	tests := []struct {
		name       string
		ctx        context.Context
		execer     *stubMigrationExecer
		wantCause  error
		wantCalled bool
	}{
		{name: "nil context", execer: &stubMigrationExecer{}},
		{name: "nil executor", ctx: context.Background()},
		{name: "execution failure", ctx: context.Background(), execer: &stubMigrationExecer{err: executionFailure}, wantCause: executionFailure, wantCalled: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var execer migrationExecer = test.execer
			if test.execer == nil {
				execer = nil
			}
			err := ensureHistoryTable(test.ctx, execer)
			if err == nil {
				t.Fatal("ensureHistoryTable() returned nil error")
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("ensureHistoryTable() error = %v, want cause %v", err, test.wantCause)
			}
			if test.execer != nil && test.execer.called != test.wantCalled {
				t.Fatalf("executor called = %t, want %t", test.execer.called, test.wantCalled)
			}
		})
	}
}
