//go:build integration

package migration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestAttestHistoryTableRejectsCatalogDriftOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pgx.Connect() returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL connection: %v", err)
		}
	})
	drop := func(ctx context.Context) {
		if _, err := conn.Exec(ctx, "DROP TABLE IF EXISTS public.gotth_schema_migrations"); err != nil {
			t.Fatalf("drop history table: %v", err)
		}
	}
	drop(ctx)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		drop(cleanupContext)
	})

	var serverVersion int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&serverVersion); err != nil {
		t.Fatalf("query PostgreSQL version: %v", err)
	}
	if serverVersion != 170010 {
		t.Fatalf("PostgreSQL server_version_num = %d, want 170010", serverVersion)
	}
	drifts := map[string]string{
		"missing digest constraint": "ALTER TABLE public.gotth_schema_migrations DROP CONSTRAINT gotth_schema_migrations_sha256_length",
		"extra column":              "ALTER TABLE public.gotth_schema_migrations ADD COLUMN unexpected text",
		"changed timestamp default": "ALTER TABLE public.gotth_schema_migrations ALTER COLUMN applied_at SET DEFAULT statement_timestamp()",
		"row security":              "ALTER TABLE public.gotth_schema_migrations ENABLE ROW LEVEL SECURITY",
		"insert rewrite rule":       "CREATE RULE gotth_schema_migrations_ignore_insert AS ON INSERT TO public.gotth_schema_migrations DO INSTEAD NOTHING",
	}
	for name, statement := range drifts {
		name, statement := name, statement
		t.Run(name, func(t *testing.T) {
			drop(ctx)
			if err := ensureHistoryTable(ctx, conn); err != nil {
				t.Fatalf("ensureHistoryTable() returned error: %v", err)
			}
			if err := attestHistoryTable(ctx, conn); err != nil {
				t.Fatalf("attestHistoryTable() exact table: %v", err)
			}
			if _, err := conn.Exec(ctx, statement); err != nil {
				t.Fatalf("alter history table: %v", err)
			}
			if err := attestHistoryTable(ctx, conn); err == nil {
				t.Fatal("attestHistoryTable() accepted catalog drift")
			}
		})
	}
}
