// Package readiness implements the bounded database invariants required before
// GOTTH Board may advertise that it can serve its configured release safely.
package readiness

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const probeTimeout = 2 * time.Second

const governanceInvariantSQL = `SELECT
    (SELECT count(*) = 1 FROM public.governance_state)
    AND EXISTS (
        SELECT 1
        FROM public.users
        WHERE role = 'administrator'
          AND (
              suspended_at IS NULL
              OR suspended_at > $1::timestamptz
              OR suspended_until <= $1::timestamptz
          )
    )`

type database interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MigrationVerifier proves the live migration ledger exactly matches the
// immutable release embedded in the running process.
type MigrationVerifier func(context.Context) error

// Checker owns the minimum dependencies for one fail-closed readiness probe.
type Checker struct {
	database         database
	verifyMigrations MigrationVerifier
	now              func() time.Time
}

// New constructs a checker without touching PostgreSQL. Dependencies are
// validated at startup so request handling cannot silently omit an invariant.
//
// Complexity: tight Theta(1) time and auxiliary space.
func New(database database, verifyMigrations MigrationVerifier, now func() time.Time) (*Checker, error) {
	if database == nil {
		return nil, fmt.Errorf("readiness database is required")
	}
	if verifyMigrations == nil {
		return nil, fmt.Errorf("readiness migration verifier is required")
	}
	if now == nil {
		return nil, fmt.Errorf("readiness clock is required")
	}
	return &Checker{database: database, verifyMigrations: verifyMigrations, now: now}, nil
}

// Check proves the exact release migration head, singleton governance row,
// and existence of an active administrator under one two-second deadline. It
// performs no writes and exposes no database details to the HTTP boundary.
//
// Complexity: local work and auxiliary space are tight Theta(1). Total time is
// bounded by probeTimeout plus scheduler delay and otherwise delegated to one
// migration verification and one constant-shape SQL query.
func (checker *Checker) Check(ctx context.Context) error {
	if checker == nil || checker.database == nil || checker.verifyMigrations == nil || checker.now == nil {
		return fmt.Errorf("readiness checker is incomplete")
	}
	if ctx == nil {
		return fmt.Errorf("readiness context is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("readiness canceled: %w", err)
	}
	probeContext, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := checker.verifyMigrations(probeContext); err != nil {
		return fmt.Errorf("migration readiness failed: %w", err)
	}
	observedAt := checker.now().UTC()
	if observedAt.IsZero() {
		return fmt.Errorf("readiness clock returned zero time")
	}
	var valid bool
	if err := checker.database.QueryRow(probeContext, governanceInvariantSQL, observedAt).Scan(&valid); err != nil {
		return fmt.Errorf("query governance readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("governance readiness invariants are not satisfied")
	}
	return nil
}
