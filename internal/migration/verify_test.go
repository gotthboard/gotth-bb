package migration

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
)

func TestReleaseVerifierAcceptsExactReleaseHead(t *testing.T) {
	t.Parallel()

	verifier, err := NewReleaseVerifier(coordinatorMigrationFS())
	if err != nil {
		t.Fatalf("NewReleaseVerifier() returned error: %v", err)
	}
	connection := readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(2)})
	if err := verifier.Verify(context.Background(), connection); err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if connection.queryCount != 1 || len(connection.rowStatements) != 1 || connection.rowStatements[0] != attestHistoryTableSQL {
		t.Fatalf("verification calls = (queries %d, rows %+v), want one history query after one attestation", connection.queryCount, connection.rowStatements)
	}
}

func TestReleaseVerifierFailsClosed(t *testing.T) {
	t.Parallel()

	failure := errors.New("database failure")
	valid, err := NewReleaseVerifier(coordinatorMigrationFS())
	if err != nil {
		t.Fatalf("NewReleaseVerifier() returned error: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		verifier *ReleaseVerifier
		ctx      context.Context
		database releaseVerificationQuerier
		cause    error
	}{
		{name: "nil verifier", ctx: context.Background(), database: readyReleaseConnection()},
		{name: "empty verifier", verifier: &ReleaseVerifier{}, ctx: context.Background(), database: readyReleaseConnection()},
		{name: "nil context", verifier: valid, database: readyReleaseConnection()},
		{name: "nil database", verifier: valid, ctx: context.Background()},
		{name: "canceled", verifier: valid, ctx: canceled, database: readyReleaseConnection(), cause: context.Canceled},
		{name: "attestation failure", verifier: valid, ctx: context.Background(), database: &stubReleaseConnection{attestation: stubAttestationRow{err: failure}}, cause: failure},
		{name: "history failure", verifier: valid, ctx: context.Background(), database: readyReleaseConnection(coordinatorQueryResult{err: failure}), cause: failure},
		{name: "pending migration", verifier: valid, ctx: context.Background(), database: readyReleaseConnection(coordinatorQueryResult{rows: coordinatorAppliedRows(1)})},
		{name: "drift", verifier: valid, ctx: context.Background(), database: readyReleaseConnection(coordinatorQueryResult{rows: coordinatorDriftRows()})},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.verifier.Verify(test.ctx, test.database); err == nil {
				t.Fatal("Verify() returned nil error")
			} else if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("Verify() error = %v, want cause %v", err, test.cause)
			}
		})
	}

	if verifier, err := NewReleaseVerifier(fstest.MapFS{}); err == nil || verifier != nil {
		t.Fatalf("NewReleaseVerifier(empty) = (%v, %v), want nil/error", verifier, err)
	}
}
