package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gotthboard/gotth-bb/internal/buildinfo"
	"github.com/gotthboard/gotth-bb/internal/config"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/rerender"
	"github.com/gotthboard/gotth-bb/internal/searchprojection"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

type migrationRunner func(context.Context, *pgx.ConnConfig, fs.FS) error
type releaseIdentityLoader func() (buildinfo.Info, error)

const rendererConnectionCloseTimeout = 5 * time.Second

// main binds process termination signals to the one-shot migration runner and
// emits one bounded top-level failure before returning a nonzero status.
//
// Complexity: local time and auxiliary space are tight Theta(1); database and
// release work are delegated to run.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command(ctx, os.Args[1:], os.Stdout, buildinfo.Current, os.LookupEnv, migrations.Files(), func(runContext context.Context, configured *pgx.ConnConfig, filesystem fs.FS) error {
		return applyRelease(runContext, configured, filesystem)
	}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb-migrate: %v\n", err)
		os.Exit(1)
	}
}

// applyRelease first proves every existing post can cross the Alpha.3 renderer
// boundary in bounded read-only transactions, then applies the immutable SQL
// ledger and resumes the bounded derived-content rebuild. The documented
// stop/drain must remain in force across the preflight transactions and the
// later apply gap; those phases are not an atomic substitute for stopping old
// writers.
//
// Complexity: for migration work m and p stale posts containing n source and h
// rendered bytes, delegated time is O(m+p+n+h), Omega(m), with no tighter
// Theta bound because database I/O varies; auxiliary space is bounded by one
// renderer batch plus the migration runner's state.
func applyRelease(ctx context.Context, configured *pgx.ConnConfig, filesystem fs.FS) (resultErr error) {
	if ctx == nil {
		return fmt.Errorf("release migration context is required")
	}
	if configured == nil {
		return fmt.Errorf("release migration database configuration is required")
	}
	if filesystem == nil {
		return fmt.Errorf("release migration filesystem is required")
	}
	connection, err := pgx.ConnectConfig(ctx, configured)
	if err != nil {
		return fmt.Errorf("connect for renderer preflight and migration: %w", err)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rendererConnectionCloseTimeout)
		defer cancel()
		if closeErr := connection.Close(closeContext); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close renderer migration connection: %w", closeErr))
		}
	}()
	if err := rerender.Preflight(ctx, connection, rerender.MaximumBatchSize); err != nil {
		return fmt.Errorf("preflight persisted Markdown: %w", err)
	}
	if err := searchprojection.Preflight(ctx, connection, searchprojection.MaximumBatchSize); err != nil {
		return fmt.Errorf("preflight search projection: %w", err)
	}
	if err := migration.Apply(ctx, configured, filesystem); err != nil {
		return err
	}
	if err := rerender.Run(ctx, connection, rerender.MaximumBatchSize); err != nil {
		return fmt.Errorf("re-render persisted Markdown: %w", err)
	}
	if err := searchprojection.Run(ctx, connection, searchprojection.MaximumBatchSize); err != nil {
		return fmt.Errorf("populate search projection: %w", err)
	}
	return nil
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
