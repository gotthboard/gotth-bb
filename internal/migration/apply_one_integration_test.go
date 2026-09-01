//go:build integration

package migration

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestApplyMigrationIsAtomicOnPostgreSQL17(t *testing.T) {
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

	const cleanupSQL = `DROP TABLE IF EXISTS public.gotth_alpha_one;
DROP TABLE IF EXISTS public.gotth_alpha_partial;
DROP TABLE IF EXISTS public.gotth_alpha_record_rollback;
DROP TABLE IF EXISTS public.gotth_schema_migrations`
	if _, err := conn.Exec(ctx, cleanupSQL); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := conn.Exec(cleanupContext, cleanupSQL); err != nil {
			t.Errorf("final cleanup: %v", err)
		}
	})

	var serverVersion int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&serverVersion); err != nil {
		t.Fatalf("query PostgreSQL version: %v", err)
	}
	if serverVersion != 170010 {
		t.Fatalf("PostgreSQL server_version_num = %d, want 170010", serverVersion)
	}
	if err := ensureHistoryTable(ctx, conn); err != nil {
		t.Fatalf("ensureHistoryTable() returned error: %v", err)
	}

	firstSQL := "CREATE TABLE public.gotth_alpha_one (id bigint PRIMARY KEY);"
	first := migrationFile{Version: 1, Name: "000001_alpha_one.sql", SQL: firstSQL, SHA256: sha256.Sum256([]byte(firstSQL))}
	if err := applyMigration(ctx, conn, first); err != nil {
		t.Fatalf("applyMigration(first) returned error: %v", err)
	}
	applied, err := readAppliedMigrations(ctx, conn, 1)
	if err != nil || len(applied) != 1 || applied[0].Version != first.Version || applied[0].Name != first.Name || applied[0].SHA256 != first.SHA256 {
		t.Fatalf("applied history = (%+v, %v), want exact first migration", applied, err)
	}
	var firstExists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('public.gotth_alpha_one') IS NOT NULL").Scan(&firstExists); err != nil || !firstExists {
		t.Fatalf("first table exists = (%t, %v), want (true, nil)", firstExists, err)
	}

	partialSQL := "CREATE TABLE public.gotth_alpha_partial (id bigint PRIMARY KEY); SELECT 1 / 0;"
	partial := migrationFile{Version: 2, Name: "000002_failed.sql", SQL: partialSQL, SHA256: sha256.Sum256([]byte(partialSQL))}
	if err := applyMigration(ctx, conn, partial); err == nil {
		t.Fatal("applyMigration(partial) returned nil error")
	}
	var partialExists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('public.gotth_alpha_partial') IS NOT NULL").Scan(&partialExists); err != nil || partialExists {
		t.Fatalf("partial table exists = (%t, %v), want (false, nil)", partialExists, err)
	}

	recordRollbackSQL := "CREATE TABLE public.gotth_alpha_record_rollback (id bigint PRIMARY KEY);"
	recordRollback := migrationFile{Version: 1, Name: "000001_duplicate.sql", SQL: recordRollbackSQL, SHA256: sha256.Sum256([]byte(recordRollbackSQL))}
	if err := applyMigration(ctx, conn, recordRollback); err == nil {
		t.Fatal("applyMigration(record duplicate) returned nil error")
	}
	var recordRollbackExists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('public.gotth_alpha_record_rollback') IS NOT NULL").Scan(&recordRollbackExists); err != nil || recordRollbackExists {
		t.Fatalf("record-rollback table exists = (%t, %v), want (false, nil)", recordRollbackExists, err)
	}
	var historyCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.gotth_schema_migrations").Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("history count = (%d, %v), want (1, nil)", historyCount, err)
	}
}
