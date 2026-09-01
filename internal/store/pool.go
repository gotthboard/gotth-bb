package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const databaseStartupTimeout = 5 * time.Second

// OpenPool constructs a pool only from a pgx-parsed configuration and proves
// one bounded PostgreSQL round trip before returning ownership to the caller.
// A failed initial round trip closes the pool and returns no usable handle.
//
// Complexity: for configured pool capacity c and initial round-trip latency r,
// delegated construction and ping cost N(c)+r time and A(c) auxiliary space.
// Total time is O(N(c)+r), Omega(1), with no tighter Theta bound established
// because pgx and network scheduling costs are not documented; auxiliary space
// is O(A(c)), Omega(1), with no tighter Theta bound established. Local checks,
// timeout creation, and ownership transfer are time and auxiliary-space O(1),
// Omega(1), and tight Theta(1). The returned pool owns its pgx background
// worker and any connections it creates after startup.
func OpenPool(ctx context.Context, configured *pgxpool.Config) (*pgxpool.Pool, error) {
	if ctx == nil {
		return nil, fmt.Errorf("PostgreSQL startup context is required")
	}
	if configured == nil {
		return nil, fmt.Errorf("PostgreSQL pool configuration is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("PostgreSQL startup canceled: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, configured)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL pool: invalid configuration")
	}
	pingContext, cancel := context.WithTimeout(ctx, databaseStartupTimeout)
	err = pool.Ping(pingContext)
	cancel()
	if err != nil {
		pool.Close()
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, fmt.Errorf("PostgreSQL startup canceled: %w", contextErr)
		}
		return nil, fmt.Errorf("PostgreSQL startup check failed")
	}
	return pool, nil
}
