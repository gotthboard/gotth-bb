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

func TestEnsureHistoryTableOnPostgreSQL17(t *testing.T) {
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
	if err := ensureHistoryTable(ctx, conn); err != nil {
		t.Fatalf("ensureHistoryTable() first call: %v", err)
	}
	if err := ensureHistoryTable(ctx, conn); err != nil {
		t.Fatalf("ensureHistoryTable() idempotent call: %v", err)
	}

	digest := sha256.Sum256([]byte("one"))
	var appliedAt time.Time
	if err := conn.QueryRow(ctx, `INSERT INTO public.gotth_schema_migrations (version, name, sha256)
VALUES ($1, $2, $3)
RETURNING applied_at`, int64(1), "000001_initial.sql", digest[:]).Scan(&appliedAt); err != nil {
		t.Fatalf("insert valid history row: %v", err)
	}
	if appliedAt.IsZero() {
		t.Fatal("applied_at default is zero")
	}
	applied, err := readAppliedMigrations(ctx, conn, 1)
	if err != nil || len(applied) != 1 || applied[0].SHA256 != digest {
		t.Fatalf("readAppliedMigrations() = (%+v, %v), want exact inserted row", applied, err)
	}

	invalidStatements := []struct {
		name    string
		version int64
		rowName any
		digest  []byte
	}{
		{name: "nonpositive version", version: 0, rowName: "000000_invalid.sql", digest: digest[:]},
		{name: "null name", version: 2, digest: digest[:]},
		{name: "short digest", version: 2, rowName: "000002_second.sql", digest: digest[:sha256.Size-1]},
		{name: "duplicate version", version: 1, rowName: "000001_duplicate.sql", digest: digest[:]},
	}
	for _, test := range invalidStatements {
		t.Run(test.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx, `INSERT INTO public.gotth_schema_migrations (version, name, sha256)
VALUES ($1, $2, $3)`, test.version, test.rowName, test.digest); err == nil {
				t.Fatal("invalid history row was accepted")
			}
		})
	}
}
