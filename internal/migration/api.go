package migration

import (
	"context"
	"io/fs"

	"github.com/jackc/pgx/v5"
)

// Apply opens one direct PostgreSQL connection from a configuration returned
// by pgx.ParseConfig, applies the immutable release, and closes the connection.
// It never creates a pool or retries an unknown transaction outcome.
//
// Complexity: local wrapper work and space are tight Theta(1); total costs are
// those documented by applyWithConnector and applyRelease.
func Apply(ctx context.Context, configured *pgx.ConnConfig, filesystem fs.FS) error {
	return applyWithConnector(ctx, configured, filesystem, func(ctx context.Context, configured *pgx.ConnConfig) (ownedMigrationConnection, error) {
		connection, err := pgx.ConnectConfig(ctx, configured)
		if connection == nil {
			return nil, err
		}
		return connection, err
	})
}
