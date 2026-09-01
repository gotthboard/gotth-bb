//go:build integration

package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOpenPoolConnectsToPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	parsed, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() returned error: %v", err)
	}
	parsed.MaxConns = 2
	parsed.MinConns = 0
	parsed.MinIdleConns = 0
	parsed.ConnConfig.ConnectTimeout = time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := OpenPool(ctx, parsed)
	if err != nil {
		t.Fatalf("OpenPool() returned error: %v", err)
	}
	defer pool.Close()

	var serverVersion int
	if err := pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&serverVersion); err != nil {
		t.Fatalf("query PostgreSQL version: %v", err)
	}
	if serverVersion != 170010 {
		t.Fatalf("PostgreSQL server_version_num = %d, want 170010", serverVersion)
	}
}

func TestOpenPoolRedactsConnectionFailure(t *testing.T) {
	const secret = "do-not-expose-pool-secret"
	parsed, err := pgxpool.ParseConfig("postgres://forum:" + secret + "@127.0.0.1:1/forum?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := OpenPool(ctx, parsed)
	if err == nil || pool != nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("OpenPool() = (%+v, %v), want redacted failure", pool, err)
	}
}
