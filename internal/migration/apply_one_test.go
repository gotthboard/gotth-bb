package migration

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordedExecution struct {
	statement string
	arguments []any
}

type stubMigrationTx struct {
	pgx.Tx
	executions     []recordedExecution
	execErrors     []error
	execHook       func(int)
	commitErr      error
	rollbackErr    error
	commitCalled   bool
	rollbackCalled bool
	rollbackCtxErr error
}

func (tx *stubMigrationTx) Exec(_ context.Context, statement string, arguments ...any) (pgconn.CommandTag, error) {
	index := len(tx.executions)
	tx.executions = append(tx.executions, recordedExecution{statement: statement, arguments: arguments})
	if tx.execHook != nil {
		tx.execHook(index)
	}
	if index < len(tx.execErrors) {
		return pgconn.CommandTag{}, tx.execErrors[index]
	}
	return pgconn.NewCommandTag("OK"), nil
}

func (tx *stubMigrationTx) Commit(context.Context) error {
	tx.commitCalled = true
	return tx.commitErr
}

func (tx *stubMigrationTx) Rollback(ctx context.Context) error {
	tx.rollbackCalled = true
	tx.rollbackCtxErr = ctx.Err()
	return tx.rollbackErr
}

type stubMigrationBeginner struct {
	tx         pgx.Tx
	err        error
	called     bool
	contextErr error
}

func (beginner *stubMigrationBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	beginner.called = true
	beginner.contextErr = ctx.Err()
	return beginner.tx, beginner.err
}

func validTestMigration() migrationFile {
	sql := "CREATE TABLE alpha_one (id bigint PRIMARY KEY);"
	return migrationFile{
		Version: 1,
		Name:    "000001_alpha_one.sql",
		SQL:     sql,
		SHA256:  sha256.Sum256([]byte(sql)),
	}
}

func TestApplyMigrationExecutesSQLAndLedgerRecordInOneTransaction(t *testing.T) {
	t.Parallel()

	file := validTestMigration()
	tx := &stubMigrationTx{}
	beginner := &stubMigrationBeginner{tx: tx}
	if err := applyMigration(context.Background(), beginner, file); err != nil {
		t.Fatalf("applyMigration() returned error: %v", err)
	}
	if !beginner.called || beginner.contextErr != nil {
		t.Fatalf("Begin() = (called %t, context error %v), want (true, nil)", beginner.called, beginner.contextErr)
	}
	if len(tx.executions) != 2 {
		t.Fatalf("Exec() count = %d, want 2", len(tx.executions))
	}
	if tx.executions[0].statement != file.SQL || len(tx.executions[0].arguments) != 0 {
		t.Fatalf("first Exec() = %+v, want exact migration SQL without arguments", tx.executions[0])
	}
	insert := tx.executions[1]
	if insert.statement != insertMigrationRecordSQL || len(insert.arguments) != 3 {
		t.Fatalf("second Exec() = %+v, want exact ledger insert", insert)
	}
	if insert.arguments[0] != file.Version || insert.arguments[1] != file.Name {
		t.Fatalf("ledger identity arguments = %+v, want version/name", insert.arguments[:2])
	}
	digest, ok := insert.arguments[2].([]byte)
	if !ok || len(digest) != sha256.Size || string(digest) != string(file.SHA256[:]) {
		t.Fatalf("ledger digest = %x, want %x", digest, file.SHA256)
	}
	if !tx.commitCalled || tx.rollbackCalled {
		t.Fatalf("transaction calls = (commit %t, rollback %t), want (true, false)", tx.commitCalled, tx.rollbackCalled)
	}
}

func TestApplyMigrationRejectsInvalidInputsBeforeBegin(t *testing.T) {
	t.Parallel()

	valid := validTestMigration()
	badName := valid
	badName.Name = "1_bad.sql"
	emptySQL := valid
	emptySQL.SQL = " \n\t"
	emptySQL.SHA256 = sha256.Sum256([]byte(emptySQL.SQL))
	wrongDigest := valid
	wrongDigest.SHA256 = sha256.Sum256([]byte("different"))
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		beginner *stubMigrationBeginner
		file     migrationFile
		cause    error
	}{
		{name: "nil context", beginner: &stubMigrationBeginner{}, file: valid},
		{name: "nil beginner", ctx: context.Background(), file: valid},
		{name: "canceled context", ctx: canceledContext, beginner: &stubMigrationBeginner{}, file: valid, cause: context.Canceled},
		{name: "nonpositive version", ctx: context.Background(), beginner: &stubMigrationBeginner{}, file: migrationFile{}},
		{name: "bad filename", ctx: context.Background(), beginner: &stubMigrationBeginner{}, file: badName},
		{name: "empty SQL", ctx: context.Background(), beginner: &stubMigrationBeginner{}, file: emptySQL},
		{name: "wrong digest", ctx: context.Background(), beginner: &stubMigrationBeginner{}, file: wrongDigest},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var beginner migrationBeginner = test.beginner
			if test.beginner == nil {
				beginner = nil
			}
			err := applyMigration(test.ctx, beginner, test.file)
			if err == nil {
				t.Fatal("applyMigration() returned nil error")
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("applyMigration() error = %v, want cause %v", err, test.cause)
			}
			if test.beginner != nil && test.beginner.called {
				t.Fatal("Begin() called for invalid input")
			}
		})
	}
}

func TestApplyMigrationRollsBackAndPreservesFailures(t *testing.T) {
	t.Parallel()

	beginFailure := errors.New("begin failure")
	sqlFailure := errors.New("SQL failure")
	recordFailure := errors.New("record failure")
	commitFailure := errors.New("commit failure")
	rollbackFailure := errors.New("rollback failure")
	tests := []struct {
		name         string
		beginner     *stubMigrationBeginner
		wantCause    error
		wantSecond   error
		wantExecs    int
		wantCommit   bool
		wantRollback bool
	}{
		{name: "begin failure", beginner: &stubMigrationBeginner{err: beginFailure}, wantCause: beginFailure},
		{name: "nil transaction", beginner: &stubMigrationBeginner{}, wantCause: errNilMigrationTransaction},
		{name: "migration SQL failure", beginner: &stubMigrationBeginner{tx: &stubMigrationTx{execErrors: []error{sqlFailure}}}, wantCause: sqlFailure, wantExecs: 1, wantRollback: true},
		{name: "record failure", beginner: &stubMigrationBeginner{tx: &stubMigrationTx{execErrors: []error{nil, recordFailure}}}, wantCause: recordFailure, wantExecs: 2, wantRollback: true},
		{name: "commit failure", beginner: &stubMigrationBeginner{tx: &stubMigrationTx{commitErr: commitFailure, rollbackErr: pgx.ErrTxClosed}}, wantCause: commitFailure, wantExecs: 2, wantCommit: true, wantRollback: true},
		{name: "SQL and rollback failure", beginner: &stubMigrationBeginner{tx: &stubMigrationTx{execErrors: []error{sqlFailure}, rollbackErr: rollbackFailure}}, wantCause: sqlFailure, wantSecond: rollbackFailure, wantExecs: 1, wantRollback: true},
		{name: "commit and rollback failure", beginner: &stubMigrationBeginner{tx: &stubMigrationTx{commitErr: commitFailure, rollbackErr: rollbackFailure}}, wantCause: commitFailure, wantSecond: rollbackFailure, wantExecs: 2, wantCommit: true, wantRollback: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := applyMigration(context.Background(), test.beginner, validTestMigration())
			if err == nil || !errors.Is(err, test.wantCause) {
				t.Fatalf("applyMigration() error = %v, want cause %v", err, test.wantCause)
			}
			if test.wantSecond != nil && !errors.Is(err, test.wantSecond) {
				t.Fatalf("applyMigration() error = %v, want second cause %v", err, test.wantSecond)
			}
			tx, _ := test.beginner.tx.(*stubMigrationTx)
			if tx == nil {
				return
			}
			if len(tx.executions) != test.wantExecs || tx.commitCalled != test.wantCommit || tx.rollbackCalled != test.wantRollback {
				t.Fatalf("transaction = (execs %d, commit %t, rollback %t), want (%d, %t, %t)", len(tx.executions), tx.commitCalled, tx.rollbackCalled, test.wantExecs, test.wantCommit, test.wantRollback)
			}
		})
	}
}

func TestApplyMigrationDetachesRollbackFromCallerCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	tx := &stubMigrationTx{
		execErrors: []error{context.Canceled},
		execHook: func(int) {
			cancel()
		},
	}
	err := applyMigration(ctx, &stubMigrationBeginner{tx: tx}, validTestMigration())
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("applyMigration() error = %v, want context.Canceled", err)
	}
	if !tx.rollbackCalled || tx.rollbackCtxErr != nil {
		t.Fatalf("rollback = (called %t, context error %v), want (true, nil)", tx.rollbackCalled, tx.rollbackCtxErr)
	}
}
