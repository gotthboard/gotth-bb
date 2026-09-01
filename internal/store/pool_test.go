package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOpenPoolRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	parsed, err := pgxpool.ParseConfig("postgres://forum@127.0.0.1/forum?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() returned error: %v", err)
	}
	if pool, err := OpenPool(nil, parsed); err == nil || pool != nil {
		t.Fatalf("OpenPool(nil, config) = (%+v, %v), want (nil, error)", pool, err)
	}
	if pool, err := OpenPool(context.Background(), nil); err == nil || pool != nil {
		t.Fatalf("OpenPool(context, nil) = (%+v, %v), want (nil, error)", pool, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pool, err := OpenPool(ctx, parsed); !errors.Is(err, context.Canceled) || pool != nil {
		t.Fatalf("OpenPool(canceled, config) = (%+v, %v), want (nil, context.Canceled)", pool, err)
	}
}

func TestOpenPoolRejectsInvalidParsedConfiguration(t *testing.T) {
	t.Parallel()

	parsed, err := pgxpool.ParseConfig("postgres://forum@127.0.0.1/forum?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() returned error: %v", err)
	}
	parsed.MaxConns = 0
	if pool, err := OpenPool(context.Background(), parsed); err == nil || pool != nil {
		t.Fatalf("OpenPool(context, invalid config) = (%+v, %v), want (nil, error)", pool, err)
	}
}

func TestOpenPoolPreservesCancellationDuringInitialPing(t *testing.T) {
	t.Parallel()

	parsed, err := pgxpool.ParseConfig("postgres://forum@127.0.0.1/forum?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	parsed.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		cancel()
		return context.Canceled
	}
	pool, err := OpenPool(ctx, parsed)
	if !errors.Is(err, context.Canceled) || pool != nil {
		t.Fatalf("OpenPool(canceled during ping, config) = (%+v, %v), want (nil, context.Canceled)", pool, err)
	}
}
