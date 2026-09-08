package searchprojection

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type unusableDatabase struct{}

func (unusableDatabase) Begin(context.Context) (pgx.Tx, error) { return nil, nil }
func (unusableDatabase) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, nil
}
func (unusableDatabase) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (unusableDatabase) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func TestPreflightRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		ctx       context.Context
		database  preflightDatabase
		batchSize int
	}{
		{name: "nil context", database: unusableDatabase{}, batchSize: 1},
		{name: "nil database", ctx: context.Background(), batchSize: 1},
		{name: "zero batch", ctx: context.Background(), database: unusableDatabase{}},
		{name: "oversized batch", ctx: context.Background(), database: unusableDatabase{}, batchSize: MaximumBatchSize + 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := Preflight(test.ctx, test.database, test.batchSize); err == nil {
				t.Fatal("Preflight() accepted invalid boundary")
			}
		})
	}
}

func TestRunRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		ctx       context.Context
		database  database
		batchSize int
	}{
		{name: "nil context", database: unusableDatabase{}, batchSize: 1},
		{name: "nil database", ctx: context.Background(), batchSize: 1},
		{name: "zero batch", ctx: context.Background(), database: unusableDatabase{}},
		{name: "oversized batch", ctx: context.Background(), database: unusableDatabase{}, batchSize: MaximumBatchSize + 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := Run(test.ctx, test.database, test.batchSize); err == nil {
				t.Fatal("Run() accepted invalid boundary")
			}
		})
	}
}

func TestBatchQueriesUseDirectPositiveKeysets(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]string{
		"topics": selectTopicsAfterCursorSQL,
		"posts":  selectPostsAfterCursorSQL,
	} {
		if !strings.Contains(query, "id > $1") || strings.Contains(query, "IS NULL OR") || !strings.Contains(query, "LIMIT $2") {
			t.Fatalf("%s cursor query is not a direct bounded keyset: %s", name, query)
		}
	}
}

func TestPostPreflightQueryToleratesThePreModerationSchema(t *testing.T) {
	t.Parallel()

	if strings.Contains(selectInitialPostsSQL, "to_jsonb") {
		t.Fatal("population query unexpectedly hides a required current-schema column")
	}
	const compatible = "COALESCE((pg_catalog.to_jsonb(post)->>'redacted_at') IS NOT NULL, false)"
	if !strings.Contains(selectPreflightPostsSQL, compatible) || strings.Contains(selectPreflightPostsSQL, ", post.redacted_at IS NOT NULL") {
		t.Fatalf("post preflight query is not compatible with the Alpha.2 schema: %s", selectPreflightPostsSQL)
	}
}

func TestFiniteTimestampMatchesPostgreSQLFiniteBoundary(t *testing.T) {
	t.Parallel()

	if !finiteTimestamp(pgtype.Timestamptz{Time: time.Time{}, Valid: true}) {
		t.Fatal("finiteTimestamp() rejected PostgreSQL's finite year-one instant")
	}
	for _, value := range []pgtype.Timestamptz{
		{},
		{Valid: true, InfinityModifier: pgtype.Infinity},
		{Valid: true, InfinityModifier: pgtype.NegativeInfinity},
	} {
		if finiteTimestamp(value) {
			t.Fatalf("finiteTimestamp(%+v) accepted a null or infinite timestamp", value)
		}
	}
}
