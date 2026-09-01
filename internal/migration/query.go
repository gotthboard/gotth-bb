package migration

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const selectAppliedMigrationsSQL = `SELECT version, name, sha256
FROM public.gotth_schema_migrations
ORDER BY version
LIMIT $1`

type appliedMigrationQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// readAppliedMigrations fetches at most one row beyond the loaded release so
// unknown database versions fail closed without allowing an unbounded result.
//
// Complexity: for a returned rows and delegated query cost Q(a), time is
// O(Q(a)+a) and Omega(a); no tight Theta bound is established because the
// database query cost is external. Returned and auxiliary space are O(a),
// Omega(a), and tight Theta(a).
func readAppliedMigrations(ctx context.Context, querier appliedMigrationQuerier, releaseCount int) ([]appliedMigration, error) {
	if ctx == nil {
		return nil, fmt.Errorf("migration query context is required")
	}
	if querier == nil {
		return nil, fmt.Errorf("migration query connection is required")
	}
	limit := int64(releaseCount)
	if limit < 1 || limit == int64(^uint64(0)>>1) {
		return nil, fmt.Errorf("release migration count is invalid")
	}
	rows, err := querier.Query(ctx, selectAppliedMigrationsSQL, limit+1)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	return scanAppliedMigrations(rows)
}
