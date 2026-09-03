package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gotthboard/gotth-bb/internal/buildinfo"
	"github.com/jackc/pgx/v5"
)

type migrationFailingWriter struct{}

func (migrationFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestCommandReportsDatabaseFreeReleaseIdentity(t *testing.T) {
	t.Parallel()

	const commit = "0123456789abcdef0123456789abcdef01234567"
	var output bytes.Buffer
	err := command(context.Background(), []string{"version"}, &output, func() (buildinfo.Info, error) {
		return buildinfo.Info{Version: "1.0.0-alpha.1", Commit: commit}, nil
	}, func(string) (string, bool) {
		t.Fatal("version command loaded database configuration")
		return "", false
	}, nil, nil)
	if err != nil {
		t.Fatalf("command(version) error = %v", err)
	}
	if got, want := output.String(), "gotth-bb version=1.0.0-alpha.1 commit="+commit+"\n"; got != want {
		t.Fatalf("command(version) output = %q, want %q", got, want)
	}
}

func TestCommandRejectsInvalidVersionBoundaries(t *testing.T) {
	t.Parallel()

	identityFailure := errors.New("identity failed")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		args     []string
		output   io.Writer
		identity releaseIdentityLoader
		want     string
	}{
		{name: "nil context", args: []string{"version"}, output: io.Discard, identity: buildinfo.Current, want: "context is required"},
		{name: "nil arguments", ctx: context.Background(), output: io.Discard, identity: buildinfo.Current, want: "arguments are required"},
		{name: "nil output", ctx: context.Background(), args: []string{"version"}, identity: buildinfo.Current, want: "output is required"},
		{name: "nil identity", ctx: context.Background(), args: []string{"version"}, output: io.Discard, want: "identity loader is required"},
		{name: "canceled", ctx: canceled, args: []string{"version"}, output: io.Discard, identity: buildinfo.Current, want: "command canceled"},
		{name: "unknown argument", ctx: context.Background(), args: []string{"apply"}, output: io.Discard, identity: buildinfo.Current, want: "accepts only"},
		{name: "extra argument", ctx: context.Background(), args: []string{"version", "extra"}, output: io.Discard, identity: buildinfo.Current, want: "accepts only"},
		{name: "identity failure", ctx: context.Background(), args: []string{"version"}, output: io.Discard, identity: func() (buildinfo.Info, error) { return buildinfo.Info{}, identityFailure }, want: "load release identity"},
		{name: "output failure", ctx: context.Background(), args: []string{"version"}, output: migrationFailingWriter{}, identity: buildinfo.Current, want: "write release identity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := command(test.ctx, test.args, test.output, test.identity, nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("command() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCommandDelegatesArgumentFreeMigration(t *testing.T) {
	t.Parallel()

	called := false
	err := command(context.Background(), []string{}, io.Discard, buildinfo.Current, mapLookup(map[string]string{
		"DATABASE_URL": "postgres://migrator@127.0.0.1/forum",
	}), fstest.MapFS{"000001_test.sql": {Data: []byte("SELECT 1;\n")}}, func(context.Context, *pgx.ConnConfig, fs.FS) error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("command() = %v, runner called = %v", err, called)
	}
}

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
