package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"syscall"

	"git.dannyhunn.com/agents/gotth-bb/internal/buildinfo"
	"git.dannyhunn.com/agents/gotth-bb/internal/config"
	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

type migrationRunner func(context.Context, *pgx.ConnConfig, fs.FS) error
type releaseIdentityLoader func() (buildinfo.Info, error)

// main binds process termination signals to the one-shot migration runner and
// emits one bounded top-level failure before returning a nonzero status.
//
// Complexity: local time and auxiliary space are tight Theta(1); database and
// release work are delegated to run.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command(ctx, os.Args[1:], os.Stdout, buildinfo.Current, os.LookupEnv, migrations.Files(), migration.Apply); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb-migrate: %v\n", err)
		os.Exit(1)
	}
}

// command exposes the database-free release identity or delegates the
// argument-free migration action without weakening its configuration boundary.
//
// Complexity: local time and auxiliary space are tight Theta(1); output and
// migration work are delegated.
func command(
	ctx context.Context,
	args []string,
	output io.Writer,
	loadIdentity releaseIdentityLoader,
	lookup config.LookupEnv,
	filesystem fs.FS,
	apply migrationRunner,
) error {
	if ctx == nil {
		return fmt.Errorf("migration command context is required")
	}
	if args == nil {
		return fmt.Errorf("migration command arguments are required")
	}
	if output == nil {
		return fmt.Errorf("migration command output is required")
	}
	if loadIdentity == nil {
		return fmt.Errorf("migration release identity loader is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration command canceled: %w", err)
	}
	if len(args) == 1 && args[0] == "version" {
		release, err := loadIdentity()
		if err != nil {
			return fmt.Errorf("load release identity: %w", err)
		}
		if _, err := fmt.Fprintf(output, "gotth-bb version=%s commit=%s\n", release.Version, release.Commit); err != nil {
			return fmt.Errorf("write release identity: %w", err)
		}
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("migration command accepts only the optional version argument")
	}
	return run(ctx, lookup, filesystem, apply)
}

// run loads only migration database configuration and applies the exact SQL
// release supplied by the caller once. It does not start HTTP, create a pool,
// or retry an unknown database outcome.
//
// Complexity: for n connection-string bytes and delegated release work r,
// total time is O(n+r), Omega(1), with no tight Theta bound because pgx parsing,
// filesystem, network, and PostgreSQL costs are external. Local auxiliary space
// is O(n), Omega(1); the parsed pgx configuration retains connection data.
func run(ctx context.Context, lookup config.LookupEnv, filesystem fs.FS, apply migrationRunner) error {
	if ctx == nil {
		return fmt.Errorf("migration command context is required")
	}
	if filesystem == nil {
		return fmt.Errorf("migration command filesystem is required")
	}
	if apply == nil {
		return fmt.Errorf("migration command runner is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration command canceled: %w", err)
	}
	configured, err := config.LoadDatabaseConnectionConfig(lookup)
	if err != nil {
		return fmt.Errorf("load migration database configuration: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration command canceled: %w", err)
	}
	if err := apply(ctx, configured, filesystem); err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}
