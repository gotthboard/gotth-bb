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

func TestGetActiveSessionBindsAndScansCurrentLocalAuthority(t *testing.T) {
	t.Parallel()

	observedAt := pgtype.Timestamptz{Time: time.Date(2026, time.September, 2, 0, 30, 0, 0, time.UTC), Valid: true}
	idleCutoff := pgtype.Timestamptz{Time: observedAt.Time.Add(-30 * time.Minute), Valid: true}
	tokenHash := bytes.Repeat([]byte{0x41}, 32)
	issuedAt := pgtype.Timestamptz{Time: observedAt.Time.Add(-time.Hour), Valid: true}
	lastSeenAt := pgtype.Timestamptz{Time: observedAt.Time.Add(-time.Minute), Valid: true}
	validatedAt := pgtype.Timestamptz{Time: observedAt.Time.Add(-10 * time.Minute), Valid: true}
	expiresAt := pgtype.Timestamptz{Time: observedAt.Time.Add(time.Hour), Valid: true}
	mutedUntil := pgtype.Timestamptz{Time: observedAt.Time.Add(5 * time.Minute), Valid: true}
	ctx := context.WithValue(context.Background(), activeSessionQueryContextKey{}, "preserved")
	database := &activeSessionQueryDBTX{row: activeSessionQueryRow{values: []any{
		int64(7), int64(42), issuedAt, lastSeenAt, validatedAt, expiresAt,
		"moderator", mutedUntil, []int64{3, 11},
	}}}

	got, err := New(database).GetActiveSession(ctx, GetActiveSessionParams{
		TokenHash: tokenHash, ObservedAt: observedAt, IdleCutoff: idleCutoff,
	})
	if err != nil || got.SessionID != 7 || got.UserID != 42 || got.Role != "moderator" ||
		!reflect.DeepEqual(got.IssuedAt, issuedAt) || !reflect.DeepEqual(got.LastSeenAt, lastSeenAt) ||
		!reflect.DeepEqual(got.ValidatedAt, validatedAt) || !reflect.DeepEqual(got.ExpiresAt, expiresAt) ||
		!reflect.DeepEqual(got.MutedUntil, mutedUntil) || !reflect.DeepEqual(got.GroupIds, []int64{3, 11}) {
		t.Fatalf("GetActiveSession() = (%+v, %v)", got, err)
	}
	if database.ctx != ctx || database.query != getActiveSession || len(database.args) != 3 ||
		!bytes.Equal(database.args[0].([]byte), tokenHash) ||
		!reflect.DeepEqual(database.args[1], observedAt) || !reflect.DeepEqual(database.args[2], idleCutoff) {
		t.Fatalf("query call = (context %v, query %q, args %#v)", database.ctx, database.query, database.args)
	}
	for _, required := range []string{
		"FROM public.forum_group_members AS membership",
		"membership.user_id = forum_user.id",
		"ORDER BY membership.group_id",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("active-session query lacks %q", required)
		}
	}
}

func TestGetActiveSessionReturnsScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	database := &activeSessionQueryDBTX{row: activeSessionQueryRow{err: cause}}
	got, err := New(database).GetActiveSession(context.Background(), GetActiveSessionParams{})
	if !errors.Is(err, cause) || !reflect.DeepEqual(got, GetActiveSessionRow{}) {
		t.Fatalf("GetActiveSession() = (%+v, %v), want zero/cause", got, err)
	}
}

type activeSessionQueryContextKey struct{}

type activeSessionQueryDBTX struct {
	DBTX
	ctx   context.Context
	query string
	args  []any
	row   pgx.Row
}

func (database *activeSessionQueryDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	return database.row
}

type activeSessionQueryRow struct {
	values []any
	err    error
}

func (row activeSessionQueryRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*(destinations[0].(*int64)) = row.values[0].(int64)
	*(destinations[1].(*int64)) = row.values[1].(int64)
	*(destinations[2].(*pgtype.Timestamptz)) = row.values[2].(pgtype.Timestamptz)
	*(destinations[3].(*pgtype.Timestamptz)) = row.values[3].(pgtype.Timestamptz)
	*(destinations[4].(*pgtype.Timestamptz)) = row.values[4].(pgtype.Timestamptz)
	*(destinations[5].(*pgtype.Timestamptz)) = row.values[5].(pgtype.Timestamptz)
	*(destinations[6].(*string)) = row.values[6].(string)
	*(destinations[7].(*pgtype.Timestamptz)) = row.values[7].(pgtype.Timestamptz)
	*(destinations[8].(*[]int64)) = append([]int64(nil), row.values[8].([]int64)...)
	return nil
}
