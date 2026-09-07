// Package readiness implements the bounded database invariants required before
// GOTTH Board may advertise that it can serve its configured release safely.
package readiness

import (
	"context"
	"fmt"
	"time"

	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
)

const probeTimeout = 2 * time.Second

const (
	rendererConstraintDefinition       = "CHECK (((renderer_version = 'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2'::text) OR ((renderer_version = 'goldmark-v1.8.5-bluemonday-v1.0.27-p1-preserved'::text) AND (redacted_at IS NULL)) OR ((renderer_version = 'moderation-redaction-v1'::text) AND (redacted_at IS NOT NULL))))"
	renderedSizeConstraintDefinition   = "CHECK ((octet_length(rendered_html) <= 262144))"
	rendererCursorConstraintDefinition = "CHECK ((((converted_count = 0) AND (last_processed_post_id IS NULL)) OR ((converted_count > 0) AND (last_processed_post_id IS NOT NULL))))"
)

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
    )
    AND EXISTS (
        SELECT 1
        FROM public.content_renderer_state
        WHERE singleton
          AND target_version = $2::text
          AND completed_at IS NOT NULL
    )
    AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_attribute AS cursor_column
        WHERE cursor_column.attrelid = 'public.content_renderer_state'::regclass
          AND cursor_column.attname = 'last_processed_post_id'
          AND cursor_column.atttypid = 'pg_catalog.int8'::regtype
          AND cursor_column.atttypmod = -1
          AND NOT cursor_column.attnotnull
          AND NOT cursor_column.attisdropped
          AND cursor_column.attgenerated = ''
          AND cursor_column.attidentity = ''
          AND NOT cursor_column.atthasdef
    )
    AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_constraint AS cursor_constraint
        WHERE cursor_constraint.conrelid = 'public.content_renderer_state'::regclass
          AND cursor_constraint.conname = 'content_renderer_state_cursor_progress'
          AND cursor_constraint.contype = 'c'
          AND cursor_constraint.convalidated
          AND pg_catalog.pg_get_constraintdef(cursor_constraint.oid, false) = $5::text
    )
    AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_constraint AS constraint_state
        JOIN pg_catalog.pg_class AS constrained_table
          ON constrained_table.oid = constraint_state.conrelid
        JOIN pg_catalog.pg_namespace AS constrained_schema
          ON constrained_schema.oid = constrained_table.relnamespace
        WHERE constrained_schema.nspname = 'public'
          AND constrained_table.relname = 'posts'
          AND constraint_state.conname = 'posts_renderer_version_current'
          AND constraint_state.contype = 'c'
          AND constraint_state.convalidated
          AND pg_catalog.pg_get_constraintdef(constraint_state.oid, false) = $3::text
    )
    AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_constraint AS constraint_state
        JOIN pg_catalog.pg_class AS constrained_table
          ON constrained_table.oid = constraint_state.conrelid
        JOIN pg_catalog.pg_namespace AS constrained_schema
          ON constrained_schema.oid = constrained_table.relnamespace
        WHERE constrained_schema.nspname = 'public'
          AND constrained_table.relname = 'posts'
          AND constraint_state.conname = 'posts_rendered_size'
          AND constraint_state.contype = 'c'
          AND constraint_state.convalidated
          AND pg_catalog.pg_get_constraintdef(constraint_state.oid, false) = $4::text
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
	if err := checker.database.QueryRow(
		probeContext,
		governanceInvariantSQL,
		observedAt,
		contentrender.RendererVersion,
		rendererConstraintDefinition,
		renderedSizeConstraintDefinition,
		rendererCursorConstraintDefinition,
	).Scan(&valid); err != nil {
		return fmt.Errorf("query governance readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("governance readiness invariants are not satisfied")
	}
	return nil
}
