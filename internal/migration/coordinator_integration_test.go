//go:build integration

package migration

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func runIntegrationRelease(ctx context.Context, databaseURL string, filesystem fstest.MapFS) (result error) {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result = errors.Join(result, conn.Close(closeContext))
	}()
	return applyRelease(ctx, conn, filesystem)
}

func TestApplyReleaseFreshIdempotentAndDriftOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	const cleanupSQL = `DROP TABLE IF EXISTS public.gotth_coordinator_first;
DROP TABLE IF EXISTS public.gotth_coordinator_second;
DROP TABLE IF EXISTS public.gotth_schema_migrations`
	if _, err := admin.Exec(ctx, cleanupSQL); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, cleanupSQL); err != nil {
			t.Errorf("final cleanup: %v", err)
		}
	})

	migrations := fstest.MapFS{
		"000001_coordinator_first.sql":  {Data: []byte("CREATE TABLE public.gotth_coordinator_first (id bigint PRIMARY KEY);")},
		"000002_coordinator_second.sql": {Data: []byte("CREATE TABLE public.gotth_coordinator_second (id bigint PRIMARY KEY);")},
	}
	if err := runIntegrationRelease(ctx, databaseURL, migrations); err != nil {
		t.Fatalf("applyRelease() fresh: %v", err)
	}
	if err := runIntegrationRelease(ctx, databaseURL, migrations); err != nil {
		t.Fatalf("applyRelease() idempotent: %v", err)
	}
	var historyCount int
	var bothTables bool
	if err := admin.QueryRow(ctx, `SELECT count(*),
       (SELECT to_regclass('public.gotth_coordinator_first') IS NOT NULL
               AND to_regclass('public.gotth_coordinator_second') IS NOT NULL)
FROM public.gotth_schema_migrations`).Scan(&historyCount, &bothTables); err != nil {
		t.Fatalf("inspect migrated database: %v", err)
	}
	if historyCount != 2 || !bothTables {
		t.Fatalf("migrated state = (history %d, tables %t), want (2, true)", historyCount, bothTables)
	}

	drifted := fstest.MapFS{
		"000001_coordinator_first.sql":  {Data: []byte("CREATE TABLE public.gotth_coordinator_first (id bigint PRIMARY KEY);\n-- changed")},
		"000002_coordinator_second.sql": migrations["000002_coordinator_second.sql"],
	}
	if err := runIntegrationRelease(ctx, databaseURL, drifted); err == nil {
		t.Fatal("applyRelease() accepted changed applied bytes")
	}
	unknownDigest := sha256.Sum256([]byte("unknown"))
	if _, err := admin.Exec(ctx, `INSERT INTO public.gotth_schema_migrations (version, name, sha256)
VALUES (3, '000003_unknown.sql', $1)`, unknownDigest[:]); err != nil {
		t.Fatalf("insert unknown history version: %v", err)
	}
	if err := runIntegrationRelease(ctx, databaseURL, migrations); err == nil {
		t.Fatal("applyRelease() accepted a database version unknown to the release")
	}
}

func TestApplyReleaseSerializesConcurrentRunnersOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	const cleanupSQL = `DROP TABLE IF EXISTS public.gotth_concurrent_one;
DROP TABLE IF EXISTS public.gotth_concurrent_two;
DROP TABLE IF EXISTS public.gotth_schema_migrations`
	if _, err := admin.Exec(ctx, cleanupSQL); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, cleanupSQL); err != nil {
			t.Errorf("final cleanup: %v", err)
		}
	})
	migrations := fstest.MapFS{
		"000001_concurrent_one.sql": {Data: []byte("CREATE TABLE public.gotth_concurrent_one (id bigint PRIMARY KEY);")},
		"000002_concurrent_two.sql": {Data: []byte("CREATE TABLE public.gotth_concurrent_two (id bigint PRIMARY KEY);")},
	}
	errorsChannel := make(chan error, 2)
	for range 2 {
		go func() {
			errorsChannel <- runIntegrationRelease(ctx, databaseURL, migrations)
		}()
	}
	for range 2 {
		if err := <-errorsChannel; err != nil {
			t.Fatalf("concurrent applyRelease() returned error: %v", err)
		}
	}
	var historyCount int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM public.gotth_schema_migrations").Scan(&historyCount); err != nil {
		t.Fatalf("count concurrent history: %v", err)
	}
	if historyCount != 2 {
		t.Fatalf("concurrent history count = %d, want 2", historyCount)
	}
}
