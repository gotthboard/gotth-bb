package db

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGetActiveSessionForRotationBindsAndScansExactRow(t *testing.T) {
	t.Parallel()

	observedAt := pgtype.Timestamptz{Time: time.Date(2026, time.September, 1, 19, 55, 0, 0, time.UTC), Valid: true}
	idleCutoff := pgtype.Timestamptz{Time: observedAt.Time.Add(-30 * time.Minute), Valid: true}
	tokenHash := bytes.Repeat([]byte{0x51}, 32)
	wantExpiry := pgtype.Timestamptz{Time: observedAt.Time.Add(time.Hour), Valid: true}
	ctx := context.WithValue(context.Background(), rotationQueryContextKey{}, "preserved")
	database := &rotationQueryDBTX{row: rotationQueryRow{values: []any{int64(42), "https://auth.example/application/o/gotth-bb/", "subject-7", wantExpiry}}}
	got, err := New(database).GetActiveSessionForRotation(ctx, GetActiveSessionForRotationParams{
		SessionID: 73, TokenHash: tokenHash, ObservedAt: observedAt, IdleCutoff: idleCutoff,
	})
	if err != nil || got.UserID != 42 || got.Issuer != "https://auth.example/application/o/gotth-bb/" ||
		got.Subject != "subject-7" || !got.ExpiresAt.Time.Equal(wantExpiry.Time) {
		t.Fatalf("GetActiveSessionForRotation() = (%+v, %v)", got, err)
	}
	if database.ctx != ctx || database.query != getActiveSessionForRotation || len(database.args) != 4 ||
		database.args[0] != int64(73) || !bytes.Equal(database.args[1].([]byte), tokenHash) ||
		!reflect.DeepEqual(database.args[2], observedAt) || !reflect.DeepEqual(database.args[3], idleCutoff) {
		t.Fatalf("query call = (context %v, query %q, args %#v)", database.ctx, database.query, database.args)
	}
	for _, required := range []string{
		"FOR UPDATE OF session, forum_user, identity",
		"session.revoked_at IS NULL",
		"session.last_seen_at <= $3",
		"session.validated_at <= $3",
		"forum_user.suspended_at IS NULL",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("rotation query lacks %q", required)
		}
	}
}

func TestGetActiveSessionForRotationReturnsScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	database := &rotationQueryDBTX{row: rotationQueryRow{err: cause}}
	got, err := New(database).GetActiveSessionForRotation(context.Background(), GetActiveSessionForRotationParams{})
	if !errors.Is(err, cause) || got != (GetActiveSessionForRotationRow{}) {
		t.Fatalf("GetActiveSessionForRotation() = (%+v, %v), want zero/cause", got, err)
	}
}

type rotationQueryContextKey struct{}

type rotationQueryDBTX struct {
	DBTX
	ctx   context.Context
	query string
	args  []any
	row   pgx.Row
}

func (database *rotationQueryDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	return database.row
}

type rotationQueryRow struct {
	values []any
	err    error
}

func (row rotationQueryRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*(destinations[0].(*int64)) = row.values[0].(int64)
	*(destinations[1].(*string)) = row.values[1].(string)
	*(destinations[2].(*string)) = row.values[2].(string)
	*(destinations[3].(*pgtype.Timestamptz)) = row.values[3].(pgtype.Timestamptz)
	return nil
}
