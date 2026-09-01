package config

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabasePoolConfig parses the redacted database secret at its narrow pgx
// boundary and applies bounded application-owned pool limits.
//
// Complexity: for n connection-string bytes, the empty-secret failure is time
// and auxiliary-space O(1), Omega(1), and tight Theta(1). For valid input,
// delegated pgx parsing costs P(n) time and S(n) auxiliary space; total time is
// O(P(n)), Omega(n), with no tighter Theta bound established because pgx does
// not document P, while auxiliary space is O(S(n)), Omega(1), with no tighter
// Theta bound established. The application-owned limit assignments are time
// and auxiliary-space O(1), Omega(1), and tight Theta(1).
func (configured Config) DatabasePoolConfig() (*pgxpool.Config, error) {
	if configured.databaseURL.value == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	poolConfig, err := pgxpool.ParseConfig(configured.databaseURL.value)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL is not a valid PostgreSQL pool configuration")
	}
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 0
	poolConfig.MinIdleConns = 0
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnLifetimeJitter = 0
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 30 * time.Second
	poolConfig.PingTimeout = 2 * time.Second
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	return poolConfig, nil
}
