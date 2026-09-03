//go:build integration

package readiness

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const readinessTestDatabase = "gotth_bb_alpha1_readiness_test"

func TestCheckerTracksReleaseAndAdministratorInvariantsOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+readinessTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale readiness database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+readinessTestDatabase); err != nil {
		t.Fatalf("create readiness database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+readinessTestDatabase+" WITH (FORCE)"); err != nil {
			t.Errorf("drop readiness database: %v", err)
		}
	})

	testConfig := adminConfig.Copy()
	testConfig.Database = readinessTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect migrated database: %v", err)
	}
	t.Cleanup(func() {
		if err := connection.Close(context.Background()); err != nil {
			t.Errorf("close readiness connection: %v", err)
		}
	})
	release, err := migration.NewReleaseVerifier(migrations.Files())
	if err != nil {
		t.Fatalf("migration.NewReleaseVerifier() returned error: %v", err)
	}
	checker, err := New(connection, func(checkContext context.Context) error {
		return release.Verify(checkContext, connection)
	}, time.Now)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if err := checker.Check(ctx); err == nil {
		t.Fatal("Check() accepted a database without an administrator")
	}
	if _, err := connection.Exec(ctx, "INSERT INTO public.users (display_name, role) VALUES ('Readiness Administrator', 'administrator')"); err != nil {
		t.Fatalf("insert readiness administrator: %v", err)
	}
	if err := checker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected exact release and governance state: %v", err)
	}
}
