package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRevokeSessionForRotationBindsExactSessionAndReturnsRows(t *testing.T) {
	t.Parallel()

	observedAt := pgtype.Timestamptz{Time: time.Date(2026, time.September, 1, 20, 10, 0, 0, time.UTC), Valid: true}
	tokenHash := bytes.Repeat([]byte{0x62}, 32)
	ctx := context.WithValue(context.Background(), rotationRevokeContextKey{}, "preserved")
	for _, rows := range []int64{0, 1} {
		database := &rotationRevokeDBTX{tag: pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", rows))}
		got, err := New(database).RevokeSessionForRotation(ctx, RevokeSessionForRotationParams{
			ObservedAt: observedAt, SessionID: 73, TokenHash: tokenHash,
		})
		if err != nil || got != rows || database.ctx != ctx || database.query != revokeSessionForRotation || len(database.args) != 3 ||
			!reflect.DeepEqual(database.args[0], observedAt) || database.args[1] != int64(73) ||
			!bytes.Equal(database.args[2].([]byte), tokenHash) {
			t.Fatalf("RevokeSessionForRotation() = (rows %d, error %v, query %q, args %#v)", got, err, database.query, database.args)
		}
		for _, required := range []string{"WHERE id = $2", "token_hash = $3", "revoked_at IS NULL", "expires_at > $1"} {
			if !strings.Contains(database.query, required) {
				t.Fatalf("rotation revoke query lacks %q", required)
			}
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
	tag   pgconn.CommandTag
	err   error
}

func (database *rotationRevokeDBTX) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	return database.tag, database.err
}
