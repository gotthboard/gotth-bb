package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type stubAttestationRow struct {
	valid bool
	err   error
}

func (row stubAttestationRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	*dest[0].(*bool) = row.valid
	return nil
}

type stubAttestationQuerier struct {
	called       bool
	contextError error
	statement    string
	row          pgx.Row
}

func (querier *stubAttestationQuerier) QueryRow(ctx context.Context, statement string, _ ...any) pgx.Row {
	querier.called = true
	querier.contextError = ctx.Err()
	querier.statement = statement
	return querier.row
}

func TestAttestHistoryTableAcceptsExactCatalogShape(t *testing.T) {
	t.Parallel()

	querier := &stubAttestationQuerier{row: stubAttestationRow{valid: true}}
	if err := attestHistoryTable(context.Background(), querier); err != nil {
		t.Fatalf("attestHistoryTable() returned error: %v", err)
	}
	if !querier.called || querier.contextError != nil || querier.statement != attestHistoryTableSQL {
		t.Fatalf("QueryRow() = (called %t, context error %v, statement %q), want exact attestation query", querier.called, querier.contextError, querier.statement)
	}
}

func TestAttestHistoryTableRejectsInvalidInputsAndCatalog(t *testing.T) {
	t.Parallel()

	scanFailure := errors.New("scan failure")
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		querier   *stubAttestationQuerier
		wantCause error
		called    bool
	}{
		{name: "nil context", querier: &stubAttestationQuerier{}},
		{name: "nil querier", ctx: context.Background()},
		{name: "canceled context", ctx: canceledContext, querier: &stubAttestationQuerier{}, wantCause: context.Canceled},
		{name: "scan failure", ctx: context.Background(), querier: &stubAttestationQuerier{row: stubAttestationRow{err: scanFailure}}, wantCause: scanFailure, called: true},
		{name: "catalog mismatch", ctx: context.Background(), querier: &stubAttestationQuerier{row: stubAttestationRow{}}, called: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var querier migrationRowQuerier = test.querier
			if test.querier == nil {
				querier = nil
			}
			err := attestHistoryTable(test.ctx, querier)
			if err == nil {
				t.Fatal("attestHistoryTable() returned nil error")
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("attestHistoryTable() error = %v, want cause %v", err, test.wantCause)
			}
			if test.querier != nil && test.querier.called != test.called {
				t.Fatalf("querier called = %t, want %t", test.querier.called, test.called)
			}
		})
	}
}
