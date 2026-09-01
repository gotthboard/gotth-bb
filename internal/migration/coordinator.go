package migration

import (
	"context"
	"fmt"
	"io/fs"
)

type migrationConnection interface {
	migrationLockConnection
	migrationBeginner
	appliedMigrationQuerier
}

// applyRelease loads one immutable release before taking the session lock,
// proves the ledger and applied prefix, applies the pending suffix in order,
// and re-reads head before releasing the lock. Its connection must be dedicated
// to this invocation and closed by the owner on every return.
//
// Complexity: for migration count f, filename bytes p, SQL bytes m, applied
// rows a, delegated load D(f), lock L, catalog C, query Q(a), and execution X(m),
// time is O(D(f)+p+m+L+C+Q(a)+f+X(m)), Omega(f+m). No tight Theta bound is
// established because filesystem and PostgreSQL costs are external. Returned
// space is O(1); peak auxiliary space is O(f+m+a), Omega(f+m).
func applyRelease(ctx context.Context, connection migrationConnection, filesystem fs.FS) error {
	if ctx == nil {
		return fmt.Errorf("migration coordinator context is required")
	}
	if connection == nil {
		return fmt.Errorf("migration coordinator connection is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration coordinator canceled: %w", err)
	}
	files, err := loadMigrations(filesystem)
	if err != nil {
		return fmt.Errorf("load release migrations: %w", err)
	}

	return withMigrationLock(ctx, connection, func() error {
		if err := ensureHistoryTable(ctx, connection); err != nil {
			return err
		}
		if err := attestHistoryTable(ctx, connection); err != nil {
			return err
		}
		applied, err := readAppliedMigrations(ctx, connection, len(files))
		if err != nil {
			return err
		}
		pending, err := pendingMigrations(files, applied)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		for _, file := range pending {
			if err := applyMigration(ctx, connection, file); err != nil {
				return err
			}
		}
		if err := attestHistoryTable(ctx, connection); err != nil {
			return err
		}
		verified, err := readAppliedMigrations(ctx, connection, len(files))
		if err != nil {
			return err
		}
		remaining, err := pendingMigrations(files, verified)
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			return fmt.Errorf("migration release did not reach head")
		}
		return nil
	})
}
