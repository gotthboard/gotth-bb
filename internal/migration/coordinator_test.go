package migration

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type coordinatorQueryResult struct {
	rows pgx.Rows
	err  error
}

type stubReleaseConnection struct {
	execStatements []string
	execErrors     map[string]error
	rowStatements  []string
	attestation    pgx.Row
	attestations   []pgx.Row
	attestCount    int
	unlock         pgx.Row
	queryResults   []coordinatorQueryResult
	queryCount     int
	transactions   []pgx.Tx
	beginCount     int
}

func (connection *stubReleaseConnection) Exec(_ context.Context, statement string, _ ...any) (pgconn.CommandTag, error) {
	connection.execStatements = append(connection.execStatements, statement)
	return pgconn.CommandTag{}, connection.execErrors[statement]
}

func (connection *stubReleaseConnection) QueryRow(_ context.Context, statement string, _ ...any) pgx.Row {
	connection.rowStatements = append(connection.rowStatements, statement)
	if statement == attestHistoryTableSQL {
		if len(connection.attestations) != 0 {
			row := connection.attestations[connection.attestCount]
			connection.attestCount++
			return row
		}
		return connection.attestation
	}
	return connection.unlock
}

func (connection *stubReleaseConnection) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	result := connection.queryResults[connection.queryCount]
	connection.queryCount++
	return result.rows, result.err
}

func (connection *stubReleaseConnection) Begin(context.Context) (pgx.Tx, error) {
	tx := connection.transactions[connection.beginCount]
	connection.beginCount++
	return tx, nil
}

func coordinatorMigrationFS() fstest.MapFS {
	return fstest.MapFS{
		"000001_first.sql":  {Data: []byte("SELECT 1;")},
		"000002_second.sql": {Data: []byte("SELECT 2;")},
	}
}

func coordinatorAppliedRows(count int) *stubAppliedRows {
	fixtures := []appliedRowFixture{
		{version: 1, name: "000001_first.sql", digest: hashBytes("SELECT 1;")},
		{version: 2, name: "000002_second.sql", digest: hashBytes("SELECT 2;")},
	}
	return &stubAppliedRows{fixtures: fixtures[:count]}
}

func coordinatorDriftRows() *stubAppliedRows {
	rows := coordinatorAppliedRows(1)
	rows.fixtures[0].digest = hashBytes("changed")
	return rows
}

func hashBytes(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func readyReleaseConnection(results ...coordinatorQueryResult) *stubReleaseConnection {
	return &stubReleaseConnection{
		attestation:  stubAttestationRow{valid: true},
		unlock:       stubMigrationLockRow{unlocked: true},
		queryResults: results,
	}
}

func TestApplyReleaseCoordinatesFreshAndCurrentDatabases(t *testing.T) {
	t.Parallel()

	t.Run("fresh", func(t *testing.T) {
		t.Parallel()
		firstTx := &stubMigrationTx{}
		secondTx := &stubMigrationTx{}
		connection := readyReleaseConnection(
			coordinatorQueryResult{rows: coordinatorAppliedRows(0)},
			coordinatorQueryResult{rows: coordinatorAppliedRows(2)},
		)
		connection.transactions = []pgx.Tx{firstTx, secondTx}
		if err := applyRelease(context.Background(), connection, coordinatorMigrationFS()); err != nil {
			t.Fatalf("applyRelease() returned error: %v", err)
		}
		if connection.queryCount != 2 || connection.beginCount != 2 || !firstTx.commitCalled || !secondTx.commitCalled {
			t.Fatalf("coordination = (queries %d, begins %d, commits %t/%t), want (2, 2, true/true)", connection.queryCount, connection.beginCount, firstTx.commitCalled, secondTx.commitCalled)
		}
		if len(connection.execStatements) != 2 || connection.execStatements[0] != acquireMigrationLockSQL || connection.execStatements[1] != createHistoryTableSQL {
			t.Fatalf("connection Exec order = %+v, want lock then bootstrap", connection.execStatements)
		}
		if len(connection.rowStatements) != 3 || connection.rowStatements[0] != attestHistoryTableSQL || connection.rowStatements[1] != attestHistoryTableSQL || connection.rowStatements[2] != releaseMigrationLockSQL {
			t.Fatalf("connection QueryRow order = %+v, want attest, re-attest, then unlock", connection.rowStatements)
		}
	})

	t.Run("already current", func(t *testing.T) {
		t.Parallel()
		connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(2)})
		if err := applyRelease(context.Background(), connection, coordinatorMigrationFS()); err != nil {
			t.Fatalf("applyRelease() returned error: %v", err)
		}
		if connection.queryCount != 1 || connection.beginCount != 0 {
			t.Fatalf("coordination = (queries %d, begins %d), want (1, 0)", connection.queryCount, connection.beginCount)
		}
	})
}

func TestApplyReleaseRejectsInvalidInputsAndStageFailures(t *testing.T) {
	t.Parallel()

	failure := errors.New("stage failure")
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		ctx        context.Context
		connection *stubReleaseConnection
		filesystem fstest.MapFS
		cause      error
	}{
		{name: "nil context", connection: readyReleaseConnection(), filesystem: coordinatorMigrationFS()},
		{name: "nil connection", ctx: context.Background(), filesystem: coordinatorMigrationFS()},
		{name: "canceled context", ctx: canceledContext, connection: readyReleaseConnection(), filesystem: coordinatorMigrationFS(), cause: context.Canceled},
		{name: "invalid filesystem", ctx: context.Background(), connection: readyReleaseConnection(), filesystem: fstest.MapFS{}},
		{name: "lock acquisition", ctx: context.Background(), connection: &stubReleaseConnection{execErrors: map[string]error{acquireMigrationLockSQL: failure}}, filesystem: coordinatorMigrationFS(), cause: failure},
		{name: "bootstrap", ctx: context.Background(), connection: &stubReleaseConnection{execErrors: map[string]error{createHistoryTableSQL: failure}, unlock: stubMigrationLockRow{unlocked: true}}, filesystem: coordinatorMigrationFS(), cause: failure},
		{name: "attestation", ctx: context.Background(), connection: &stubReleaseConnection{attestation: stubAttestationRow{}, unlock: stubMigrationLockRow{unlocked: true}}, filesystem: coordinatorMigrationFS()},
		{name: "history query", ctx: context.Background(), connection: readyReleaseConnection(coordinatorQueryResult{err: failure}), filesystem: coordinatorMigrationFS(), cause: failure},
		{name: "history drift", ctx: context.Background(), connection: readyReleaseConnection(coordinatorQueryResult{rows: coordinatorDriftRows()}), filesystem: coordinatorMigrationFS()},
		{name: "migration execution", ctx: context.Background(), connection: func() *stubReleaseConnection {
			connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(0)})
			connection.transactions = []pgx.Tx{&stubMigrationTx{execErrors: []error{failure}}}
			return connection
		}(), filesystem: coordinatorMigrationFS(), cause: failure},
		{name: "final attestation", ctx: context.Background(), connection: func() *stubReleaseConnection {
			connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(1)})
			connection.transactions = []pgx.Tx{&stubMigrationTx{}}
			connection.attestations = []pgx.Row{stubAttestationRow{valid: true}, stubAttestationRow{}}
			return connection
		}(), filesystem: coordinatorMigrationFS()},
		{name: "final history query", ctx: context.Background(), connection: func() *stubReleaseConnection {
			connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(1)}, coordinatorQueryResult{err: failure})
			connection.transactions = []pgx.Tx{&stubMigrationTx{}}
			return connection
		}(), filesystem: coordinatorMigrationFS(), cause: failure},
		{name: "final history drift", ctx: context.Background(), connection: func() *stubReleaseConnection {
			connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(1)}, coordinatorQueryResult{rows: coordinatorDriftRows()})
			connection.transactions = []pgx.Tx{&stubMigrationTx{}}
			return connection
		}(), filesystem: coordinatorMigrationFS()},
		{name: "final head mismatch", ctx: context.Background(), connection: func() *stubReleaseConnection {
			connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(1)}, coordinatorQueryResult{rows: coordinatorAppliedRows(1)})
			connection.transactions = []pgx.Tx{&stubMigrationTx{}}
			return connection
		}(), filesystem: coordinatorMigrationFS()},
		{name: "unlock rejected", ctx: context.Background(), connection: func() *stubReleaseConnection {
			connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(2)})
			connection.unlock = stubMigrationLockRow{}
			return connection
		}(), filesystem: coordinatorMigrationFS()},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var connection migrationConnection = test.connection
			if test.connection == nil {
				connection = nil
			}
			err := applyRelease(test.ctx, connection, test.filesystem)
			if err == nil {
				t.Fatal("applyRelease() returned nil error")
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("applyRelease() error = %v, want cause %v", err, test.cause)
			}
		})
	}
}
