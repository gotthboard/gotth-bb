package db

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRevokeSessionForRotationBindsExactSessionAndReturnsRows(t *testing.T) {
	t.Parallel()

	observedAt := pgtype.Timestamptz{Time: time.Date(2026, time.September, 1, 20, 10, 0, 0, time.UTC), Valid: true}
	tokenHash := bytes.Repeat([]byte{0x62}, 32)
	ctx := context.WithValue(context.Background(), rotationRevokeContextKey{}, "preserved")
	for _, rows := range []int64{0, 1} {
		database := &rotationRevokeDBTX{rows: rows}
		got, err := New(database).RevokeSessionForRotation(ctx, RevokeSessionForRotationParams{
			ObservedAt: observedAt, SessionID: 73, TokenHash: tokenHash,
		})
		if err != nil || got != rows || database.ctx != ctx || database.query != revokeSessionForRotation || len(database.args) != 3 ||
			database.args[0] != int64(73) || !bytes.Equal(database.args[1].([]byte), tokenHash) ||
			!reflect.DeepEqual(database.args[2], observedAt) {
			t.Fatalf("RevokeSessionForRotation() = (rows %d, error %v, query %q, args %#v)", got, err, database.query, database.args)
		}
	}
}

func TestRevokeSessionForRotationReturnsExecutionFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("execution failed")
	database := &rotationRevokeDBTX{err: cause}
	rows, err := New(database).RevokeSessionForRotation(context.Background(), RevokeSessionForRotationParams{})
	if rows != 0 || !errors.Is(err, cause) {
		t.Fatalf("RevokeSessionForRotation() = (%d, %v), want zero/cause", rows, err)
	}
}

type rotationRevokeContextKey struct{}

type rotationRevokeDBTX struct {
	DBTX
	ctx   context.Context
	query string
	args  []any
	rows  int64
	err   error
}

func (database *rotationRevokeDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	return rotationRevokeRow{rows: database.rows, err: database.err}
}

type rotationRevokeRow struct {
	rows int64
	err  error
}

func (row rotationRevokeRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*(destinations[0].(*int64)) = row.rows
	return nil
}
