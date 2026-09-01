//go:build integration

package migration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestWithMigrationLockOwnsOnePostgreSQLSessionLock(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect lock owner: %v", err)
	}
	defer owner.Close(context.Background())
	observer, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect lock observer: %v", err)
	}
	defer observer.Close(context.Background())

	if err := withMigrationLock(ctx, owner, func() error {
		var acquired bool
		if err := observer.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", migrationAdvisoryLockKey).Scan(&acquired); err != nil {
			return err
		}
		if acquired {
			t.Fatal("second PostgreSQL session acquired the held migration lock")
		}
		return nil
	}); err != nil {
		t.Fatalf("withMigrationLock() returned error: %v", err)
	}

	var acquired bool
	if err := observer.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", migrationAdvisoryLockKey).Scan(&acquired); err != nil {
		t.Fatalf("try lock after release: %v", err)
	}
	if !acquired {
		t.Fatal("migration lock remained held after withMigrationLock returned")
	}
	var released bool
	if err := observer.QueryRow(ctx, releaseMigrationLockSQL, migrationAdvisoryLockKey).Scan(&released); err != nil || !released {
		t.Fatalf("release observer lock = (%t, %v), want (true, nil)", released, err)
	}
}
