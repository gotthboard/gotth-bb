package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
)

const migrationConnectionCloseTimeout = 5 * time.Second

type ownedMigrationConnection interface {
	migrationConnection
	Close(context.Context) error
}

type migrationConnector func(context.Context, *pgx.ConnConfig) (ownedMigrationConnection, error)

// applyWithConnector owns one dedicated PostgreSQL connection from creation
// through unconditional bounded close, including connection/coordination
// failures and caller cancellation.
//
// Complexity: local work and space are tight Theta(1) beyond applyRelease.
// Total cost is delegated connection C, release R, and close K work:
// O(C+R+K), Omega(1), with no tighter Theta bound because network, filesystem,
// and PostgreSQL behavior are external.
func applyWithConnector(ctx context.Context, configured *pgx.ConnConfig, filesystem fs.FS, connector migrationConnector) (result error) {
	if ctx == nil {
		return fmt.Errorf("migration owner context is required")
	}
	if configured == nil {
		return fmt.Errorf("migration connection configuration is required")
	}
	if connector == nil {
		return fmt.Errorf("migration connector is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration owner canceled: %w", err)
	}
	connection, err := connector(ctx, configured)
	if connection != nil {
		defer func() {
			closeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), migrationConnectionCloseTimeout)
			defer cancel()
			if err := connection.Close(closeContext); err != nil {
				result = errors.Join(result, fmt.Errorf("close migration connection: %w", err))
			}
		}()
	}
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("migration connection canceled: %w", contextErr)
		}
		return fmt.Errorf("connect migration database: %w", err)
	}
	if connection == nil {
		return fmt.Errorf("migration connector returned no connection")
	}
	return applyRelease(ctx, connection, filesystem)
}
