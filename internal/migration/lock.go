package migration

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// migrationAdvisoryLockKey is the signed first eight bytes of the SHA-256
	// digest for "gotth-bb:migrations:v1".
	migrationAdvisoryLockKey int64 = -4032937164874229523
	migrationUnlockTimeout         = 5 * time.Second
	acquireMigrationLockSQL        = "SELECT pg_advisory_lock($1)"
	releaseMigrationLockSQL        = "SELECT pg_advisory_unlock($1)"
)

type migrationLockConnection interface {
	migrationExecer
	migrationRowQuerier
}

// withMigrationLock serializes one migration action on a PostgreSQL session.
// The caller must close the dedicated connection on every return so an
// ambiguous unlock failure cannot leak a session lock into a connection pool.
//
// Complexity: local work is constant. Total time is the delegated lock wait L,
// action time W, and unlock round trip U: O(L+W+U), Omega(W), with no tighter
// Theta bound because PostgreSQL scheduling and action work are external.
// Auxiliary space is the action's space plus tight Theta(1) local state.
func withMigrationLock(ctx context.Context, connection migrationLockConnection, action func() error) (result error) {
	if ctx == nil {
		return fmt.Errorf("migration lock context is required")
	}
	if connection == nil {
		return fmt.Errorf("migration lock connection is required")
	}
	if action == nil {
		return fmt.Errorf("migration lock action is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration lock canceled: %w", err)
	}
	if _, err := connection.Exec(ctx, acquireMigrationLockSQL, migrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), migrationUnlockTimeout)
		defer cancel()
		var unlocked bool
		if err := connection.QueryRow(cleanupContext, releaseMigrationLockSQL, migrationAdvisoryLockKey).Scan(&unlocked); err != nil {
			result = errors.Join(result, fmt.Errorf("release migration advisory lock: %w", err))
			return
		}
		if !unlocked {
			result = errors.Join(result, fmt.Errorf("release migration advisory lock: lock was not held"))
		}
	}()
	return action()
}
