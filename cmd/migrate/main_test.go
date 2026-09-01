package main

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRunAppliesEmbeddedReleaseWithDatabaseConfiguration(t *testing.T) {
	t.Parallel()

	release := fstest.MapFS{"000001_test.sql": {Data: []byte("SELECT 1;\n")}}
	called := false
	lookup := func(name string) (string, bool) {
		if name != "DATABASE_URL" {
			t.Fatalf("configuration requested unexpected key %q", name)
		}
		return "postgres://migrator:secret@db.example.test:5433/forum?sslmode=require&pool_max_conns=99", true
	}
	err := run(context.Background(), lookup, release, func(ctx context.Context, configured *pgx.ConnConfig, filesystem fs.FS) error {
		called = true
		if ctx == nil || configured.Host != "db.example.test" || configured.Port != 5433 || configured.Database != "forum" || configured.User != "migrator" || configured.ConnectTimeout != 5*time.Second {
			t.Fatal("runner did not receive the expected context and redacted connection identity")
		}
		body, readErr := fs.ReadFile(filesystem, "000001_test.sql")
		if readErr != nil || string(body) != "SELECT 1;\n" {
			t.Fatalf("runner release = (%q, %v)", body, readErr)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("run() = %v, runner called = %v", err, called)
	}
}

func TestRunRejectsInvalidDependenciesBeforeApplying(t *testing.T) {
	t.Parallel()

	release := fstest.MapFS{"000001_test.sql": {Data: []byte("SELECT 1;\n")}}
	validLookup := mapLookup(map[string]string{"DATABASE_URL": "postgres://migrator@127.0.0.1/forum"})
	runner := func(context.Context, *pgx.ConnConfig, fs.FS) error {
		t.Fatal("runner called for invalid input")
		return nil
	}
	tests := []struct {
		name       string
		ctx        context.Context
		lookup     func(string) (string, bool)
		filesystem fs.FS
		runner     migrationRunner
	}{
		{name: "nil context", lookup: validLookup, filesystem: release, runner: runner},
		{name: "nil lookup", ctx: context.Background(), filesystem: release, runner: runner},
		{name: "nil filesystem", ctx: context.Background(), lookup: validLookup, runner: runner},
		{name: "nil runner", ctx: context.Background(), lookup: validLookup, filesystem: release},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := run(test.ctx, test.lookup, test.filesystem, test.runner); err == nil {
				t.Fatal("run() accepted invalid dependency")
			}
		})
	}
}

func TestRunStopsBeforeApplyingWhenAlreadyCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, mapLookup(map[string]string{"DATABASE_URL": "postgres://migrator@127.0.0.1/forum"}), fstest.MapFS{"000001_test.sql": {Data: []byte("SELECT 1;\n")}}, func(context.Context, *pgx.ConnConfig, fs.FS) error {
		t.Fatal("runner called after cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want context cancellation", err)
	}
}

func TestRunStopsWhenCanceledWhileLoadingConfiguration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	lookup := func(name string) (string, bool) {
		cancel()
		if name != "DATABASE_URL" {
			t.Fatalf("configuration requested unexpected key %q", name)
		}
		return "postgres://migrator@127.0.0.1/forum", true
	}
	err := run(ctx, lookup, fstest.MapFS{"000001_test.sql": {Data: []byte("SELECT 1;\n")}}, func(context.Context, *pgx.ConnConfig, fs.FS) error {
		t.Fatal("runner called after cancellation during configuration")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want context cancellation", err)
	}
}

func TestRunRedactsConfigurationFailureAndPreservesRunnerFailure(t *testing.T) {
	t.Parallel()

	const secret = "do-not-expose-migration-command-secret"
	release := fstest.MapFS{"000001_test.sql": {Data: []byte("SELECT 1;\n")}}
	err := run(context.Background(), mapLookup(map[string]string{"DATABASE_URL": "postgres://" + secret + "%zz"}), release, func(context.Context, *pgx.ConnConfig, fs.FS) error {
		t.Fatal("runner called with invalid configuration")
		return nil
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("run() configuration error = %v, want redacted failure", err)
	}

	cause := errors.New("migration execution failed")
	err = run(context.Background(), mapLookup(map[string]string{"DATABASE_URL": "postgres://migrator@127.0.0.1/forum"}), release, func(context.Context, *pgx.ConnConfig, fs.FS) error {
		return cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("run() error = %v, want runner cause", err)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
