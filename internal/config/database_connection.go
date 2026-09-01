package config

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadDatabaseConnectionConfig reads only DATABASE_URL, parses the same pgx
// pool-compatible URL accepted by the service, and returns one direct
// connection configuration for the migration command. Callers must not format
// the returned configuration because it necessarily contains connection
// credentials.
//
// Complexity: for n URL bytes, delegated pgx parsing has time P(n) and space
// S(n). Total time is O(P(n)), Omega(n), and space O(S(n)), Omega(1), with no
// tighter Theta bounds because pgx parsing is external. Local validation,
// timeout assignment, and configuration copy are tight Theta(1).
func LoadDatabaseConnectionConfig(lookup LookupEnv) (*pgx.ConnConfig, error) {
	if lookup == nil {
		return nil, fmt.Errorf("database configuration lookup is required")
	}
	databaseURL, present := lookup("DATABASE_URL")
	if !present || databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL is not a valid PostgreSQL connection configuration")
	}
	configured := poolConfig.ConnConfig.Copy()
	configured.ConnectTimeout = 5 * time.Second
	return configured, nil
}
