package migration

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type stubMigrationQuerier struct {
	rows      pgx.Rows
	err       error
	called    bool
	query     string
	arguments []any
}

func (querier *stubMigrationQuerier) Query(_ context.Context, query string, arguments ...any) (pgx.Rows, error) {
	querier.called = true
	querier.query = query
	querier.arguments = arguments
	return querier.rows, querier.err
}

func TestReadAppliedMigrationsQueriesOrderedBoundedHistory(t *testing.T) {
	t.Parallel()

	digest := sha256.Sum256([]byte("one"))
	rows := &stubAppliedRows{fixtures: []appliedRowFixture{{
		version: 1,
		name:    "000001_initial.sql",
		digest:  digest[:],
	}}}
	querier := &stubMigrationQuerier{rows: rows}
	applied, err := readAppliedMigrations(context.Background(), querier, 2)
	if err != nil {
		t.Fatalf("readAppliedMigrations() returned error: %v", err)
	}
	if len(applied) != 1 || applied[0].Version != 1 || applied[0].Name != "000001_initial.sql" || applied[0].SHA256 != digest {
		t.Fatalf("readAppliedMigrations() = %+v, want exact first record", applied)
	}
	if querier.query != selectAppliedMigrationsSQL {
		t.Fatalf("query = %q, want %q", querier.query, selectAppliedMigrationsSQL)
	}
	if len(querier.arguments) != 1 || querier.arguments[0] != int64(3) {
		t.Fatalf("query arguments = %+v, want [3]", querier.arguments)
	}
}

func TestReadAppliedMigrationsRejectsInvalidInputsAndQueryFailures(t *testing.T) {
	t.Parallel()

	queryFailure := errors.New("query failure")
	tests := []struct {
		name         string
		ctx          context.Context
		querier      *stubMigrationQuerier
		releaseCount int
		wantCause    error
		wantCalled   bool
	}{
		{name: "nil context", querier: &stubMigrationQuerier{}, releaseCount: 1},
		{name: "nil querier", ctx: context.Background(), releaseCount: 1},
		{name: "empty release", ctx: context.Background(), querier: &stubMigrationQuerier{}},
		{name: "overflowing release", ctx: context.Background(), querier: &stubMigrationQuerier{}, releaseCount: int(^uint(0) >> 1)},
		{name: "query failure", ctx: context.Background(), querier: &stubMigrationQuerier{err: queryFailure}, releaseCount: 1, wantCause: queryFailure, wantCalled: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var querier appliedMigrationQuerier = test.querier
			if test.querier == nil {
				querier = nil
			}
			applied, err := readAppliedMigrations(test.ctx, querier, test.releaseCount)
			if err == nil || applied != nil {
				t.Fatalf("readAppliedMigrations() = (%+v, %v), want (nil, error)", applied, err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("readAppliedMigrations() error = %v, want cause %v", err, test.wantCause)
			}
			if test.querier != nil && test.querier.called != test.wantCalled {
				t.Fatalf("querier called = %t, want %t", test.querier.called, test.wantCalled)
			}
		})
	}
}
