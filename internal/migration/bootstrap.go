package migration

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

const createHistoryTableSQL = `CREATE TABLE IF NOT EXISTS public.gotth_schema_migrations (
    version bigint PRIMARY KEY,
    name text NOT NULL,
    sha256 bytea NOT NULL,
    applied_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT gotth_schema_migrations_version_positive CHECK (version > 0),
    CONSTRAINT gotth_schema_migrations_sha256_length CHECK (octet_length(sha256) = 32)
)`

type migrationExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// ensureHistoryTable creates the project-owned migration ledger idempotently.
// The caller must serialize this operation with the migration advisory lock.
//
// Complexity: delegated PostgreSQL execution has time E and auxiliary space A.
// Total time is O(E), Omega(1), and space O(A), Omega(1); no tighter Theta
// bounds are established because database catalog and lock costs are external.
func ensureHistoryTable(ctx context.Context, execer migrationExecer) error {
	if ctx == nil {
		return fmt.Errorf("migration bootstrap context is required")
	}
	if execer == nil {
		return fmt.Errorf("migration bootstrap connection is required")
	}
	if _, err := execer.Exec(ctx, createHistoryTableSQL); err != nil {
		return fmt.Errorf("create migration history table: %w", err)
	}
	return nil
}
