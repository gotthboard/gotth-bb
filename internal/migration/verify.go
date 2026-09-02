package migration

import (
	"context"
	"fmt"
	"io/fs"
)

// ReleaseVerifier owns the validated, hashed migration set compiled into one
// release. It can prove a live database ledger is exactly at that release
// without applying migrations or taking the migration session lock.
type ReleaseVerifier struct {
	files []migrationFile
}

type releaseVerificationQuerier interface {
	migrationRowQuerier
	appliedMigrationQuerier
}

// NewReleaseVerifier validates and hashes an immutable release migration set
// once so readiness probes do not repeatedly read and hash embedded files.
//
// Complexity: for f files, p filename bytes, and m migration bytes, time is
// O(f+p+m), Omega(f+m), and retained auxiliary space is Theta(f+m), delegated
// to loadMigrations.
func NewReleaseVerifier(filesystem fs.FS) (*ReleaseVerifier, error) {
	files, err := loadMigrations(filesystem)
	if err != nil {
		return nil, fmt.Errorf("load migration release verifier: %w", err)
	}
	return &ReleaseVerifier{files: files}, nil
}

// Verify proves the migration ledger schema is trusted and its ordered rows
// exactly match every migration in this release. It performs no writes,
// retries, or locking.
//
// Complexity: for release size f, applied rows a, catalog cost C, and query
// cost Q(a), time is O(C+Q(a)+a), Omega(a), with no tighter bound because the
// database is external. Auxiliary space is Theta(a).
func (verifier *ReleaseVerifier) Verify(ctx context.Context, querier releaseVerificationQuerier) error {
	if verifier == nil || len(verifier.files) == 0 {
		return fmt.Errorf("migration release verifier is required")
	}
	if ctx == nil {
		return fmt.Errorf("migration verification context is required")
	}
	if querier == nil {
		return fmt.Errorf("migration verification database is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration verification canceled: %w", err)
	}
	if err := attestHistoryTable(ctx, querier); err != nil {
		return fmt.Errorf("verify migration ledger: %w", err)
	}
	applied, err := readAppliedMigrations(ctx, querier, len(verifier.files))
	if err != nil {
		return fmt.Errorf("verify applied migrations: %w", err)
	}
	pending, err := pendingMigrations(verifier.files, applied)
	if err != nil {
		return fmt.Errorf("verify migration release: %w", err)
	}
	if len(pending) != 0 {
		return fmt.Errorf("database migration release is not at head")
	}
	return nil
}
