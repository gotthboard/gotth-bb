package migration

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestApplyUsesParsedPgxConfiguration(t *testing.T) {
	t.Parallel()

	dialFailure := errors.New("dial failure")
	configured, err := pgx.ParseConfig("postgres://forum@127.0.0.1/forum?sslmode=disable")
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	configured.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		return nil, dialFailure
	}
	err = Apply(context.Background(), configured, coordinatorMigrationFS())
	if err == nil || !errors.Is(err, dialFailure) {
		t.Fatalf("Apply() error = %v, want dial failure cause", err)
	}
}

func TestApplyRejectsNilPublicInputs(t *testing.T) {
	t.Parallel()

	if err := Apply(nil, &pgx.ConnConfig{}, coordinatorMigrationFS()); err == nil {
		t.Fatal("Apply(nil context) returned nil error")
	}
	if err := Apply(context.Background(), nil, coordinatorMigrationFS()); err == nil {
		t.Fatal("Apply(nil config) returned nil error")
	}
}
