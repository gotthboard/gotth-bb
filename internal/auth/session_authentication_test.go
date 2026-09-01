package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAuthenticateSessionReturnsCurrentLocalAccessWithoutTouch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 14, 0, 0, 123456000, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x73}, 32))
	wantHash := sha256.Sum256([]byte(token))
	mutedUntil := now.Add(10 * time.Minute)
	queryCalls, touchCalls := 0, 0
	result, err := authenticateSession(
		context.Background(),
		func(_ context.Context, params db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			queryCalls++
			if !bytes.Equal(params.TokenHash, wantHash[:]) || !params.ObservedAt.Time.Equal(now) ||
				!params.IdleCutoff.Time.Equal(now.Add(-30*time.Minute)) {
				t.Fatalf("query params = %+v", params)
			}
			return activeSessionRow(now, "moderator", now.Add(-time.Minute), now.Add(-10*time.Minute), pgtype.Timestamptz{Time: mutedUntil, Valid: true}), nil
		},
		func(context.Context, db.TouchSessionParams) (int64, error) {
			touchCalls++
			return 0, nil
		},
		func() time.Time { return now }, 30*time.Minute, 30*time.Minute, token,
	)
	if err != nil || queryCalls != 1 || touchCalls != 0 || result.SessionID != 7 || result.RequiresRevalidation ||
		!result.Access.Authenticated || result.Access.UserID != 42 || result.Access.Role != RoleModerator ||
		result.Access.Suspended || len(result.Access.GroupIDs) != 0 || result.Access.MutedUntil == nil ||
		!result.Access.MutedUntil.Equal(mutedUntil) || !result.Access.ValidatedAt.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("authenticateSession() = (%+v, query %d, touch %d, %v)", result, queryCalls, touchCalls, err)
	}
}

func TestAuthenticateSessionTouchesAndRequiresRevalidationAtExactBoundaries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 14, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x27}, 32))
	for _, touchedRows := range []int64{0, 1} {
		touchedRows := touchedRows
		t.Run(time.Duration(touchedRows).String(), func(t *testing.T) {
			t.Parallel()
			touchCalls := 0
			result, err := authenticateSession(
				context.Background(),
				func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
					return activeSessionRow(now, "administrator", now.Add(-sessionLastSeenWriteInterval), now.Add(-time.Hour), pgtype.Timestamptz{Time: now, Valid: true}), nil
				},
				func(_ context.Context, params db.TouchSessionParams) (int64, error) {
					touchCalls++
					if params.SessionID != 7 || !params.ObservedAt.Time.Equal(now) ||
						!params.TouchBefore.Time.Equal(now.Add(-sessionLastSeenWriteInterval)) {
						t.Fatalf("touch params = %+v", params)
					}
					return touchedRows, nil
				},
				func() time.Time { return now }, 2*time.Hour, time.Hour, token,
			)
			if err != nil || touchCalls != 1 || result.SessionID != 7 || !result.Access.Authenticated || result.Access.Role != RoleAdministrator ||
				result.Access.MutedUntil != nil || !result.RequiresRevalidation {
				t.Fatalf("authenticateSession() = (%+v, touch %d, %v)", result, touchCalls, err)
			}
		})
	}
}

func TestAuthenticateSessionReducesTouchIntervalBelowShortIdleTimeout(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 14, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x28}, 32))
	touchCalls := 0
	result, err := authenticateSession(
		context.Background(),
		func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			return activeSessionRow(now, "member", now.Add(-time.Minute), now, pgtype.Timestamptz{}), nil
		},
		func(_ context.Context, params db.TouchSessionParams) (int64, error) {
			touchCalls++
			if !params.TouchBefore.Time.Equal(now.Add(-time.Minute)) {
				t.Fatalf("short-idle touch threshold = %s", params.TouchBefore.Time)
			}
			return 1, nil
		},
		func() time.Time { return now }, 2*time.Minute, time.Hour, token,
	)
	if err != nil || touchCalls != 1 || !result.Access.Authenticated {
		t.Fatalf("authenticateSession() = (%+v, touch %d, %v)", result, touchCalls, err)
	}
}

func TestAuthenticateSessionTreatsMissingOrInvalidCredentialsAsAnonymous(t *testing.T) {
	t.Parallel()

	valid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))
	for _, token := range []string{"", valid[:len(valid)-1], valid[:len(valid)-1] + "*"} {
		token := token
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			result, err := authenticateSession(
				context.Background(),
				func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
					panic("query must not run")
				},
				func(context.Context, db.TouchSessionParams) (int64, error) { panic("touch must not run") },
				time.Now, time.Hour, time.Hour, token,
			)
			if err != nil || !reflect.DeepEqual(result, SessionAuthentication{}) {
				t.Fatalf("authenticateSession(%q) = (%+v, %v), want anonymous", token, result, err)
			}
		})
	}
	result, err := authenticateSession(
		context.Background(),
		func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			return db.GetActiveSessionRow{}, pgx.ErrNoRows
		},
		func(context.Context, db.TouchSessionParams) (int64, error) { panic("touch must not run") },
		time.Now, time.Hour, time.Hour, valid,
	)
	if err != nil || !reflect.DeepEqual(result, SessionAuthentication{}) {
		t.Fatalf("missing authenticateSession() = (%+v, %v), want anonymous", result, err)
	}
}

func TestAuthenticateSessionRejectsInvalidDependenciesAndDatabaseRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 14, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x15}, 32))
	validLoad := func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
		return activeSessionRow(now, "member", now.Add(-time.Minute), now.Add(-time.Minute), pgtype.Timestamptz{}), nil
	}
	validTouch := func(context.Context, db.TouchSessionParams) (int64, error) { return 0, nil }
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name       string
		ctx        context.Context
		load       func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error)
		touch      func(context.Context, db.TouchSessionParams) (int64, error)
		clock      func() time.Time
		idle       time.Duration
		revalidate time.Duration
	}{
		{name: "nil context", load: validLoad, touch: validTouch, clock: func() time.Time { return now }, idle: time.Hour, revalidate: time.Hour},
		{name: "nil load", ctx: context.Background(), touch: validTouch, clock: func() time.Time { return now }, idle: time.Hour, revalidate: time.Hour},
		{name: "nil touch", ctx: context.Background(), load: validLoad, clock: func() time.Time { return now }, idle: time.Hour, revalidate: time.Hour},
		{name: "nil clock", ctx: context.Background(), load: validLoad, touch: validTouch, idle: time.Hour, revalidate: time.Hour},
		{name: "zero idle", ctx: context.Background(), load: validLoad, touch: validTouch, clock: func() time.Time { return now }, revalidate: time.Hour},
		{name: "subsecond idle", ctx: context.Background(), load: validLoad, touch: validTouch, clock: func() time.Time { return now }, idle: time.Second - time.Nanosecond, revalidate: time.Hour},
		{name: "zero revalidation", ctx: context.Background(), load: validLoad, touch: validTouch, clock: func() time.Time { return now }, idle: time.Hour},
		{name: "subsecond revalidation", ctx: context.Background(), load: validLoad, touch: validTouch, clock: func() time.Time { return now }, idle: time.Hour, revalidate: time.Second - time.Nanosecond},
		{name: "zero clock", ctx: context.Background(), load: validLoad, touch: validTouch, clock: func() time.Time { return time.Time{} }, idle: time.Hour, revalidate: time.Hour},
		{name: "canceled context", ctx: canceledContext, load: validLoad, touch: validTouch, clock: func() time.Time { return now }, idle: time.Hour, revalidate: time.Hour},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := authenticateSession(test.ctx, test.load, test.touch, test.clock, test.idle, test.revalidate, token); err == nil || !reflect.DeepEqual(got, SessionAuthentication{}) {
				t.Fatalf("authenticateSession() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
	for _, mutate := range []func(*db.GetActiveSessionRow){
		func(row *db.GetActiveSessionRow) { row.SessionID = 0 },
		func(row *db.GetActiveSessionRow) { row.UserID = 0 },
		func(row *db.GetActiveSessionRow) { row.Role = "owner" },
		func(row *db.GetActiveSessionRow) { row.IssuedAt.Valid = false },
		func(row *db.GetActiveSessionRow) { row.LastSeenAt.Time = row.IssuedAt.Time.Add(-time.Nanosecond) },
		func(row *db.GetActiveSessionRow) { row.ValidatedAt.Time = row.IssuedAt.Time.Add(-time.Nanosecond) },
		func(row *db.GetActiveSessionRow) { row.ExpiresAt.Time = now },
	} {
		mutate := mutate
		row := activeSessionRow(now, "member", now.Add(-time.Minute), now.Add(-time.Minute), pgtype.Timestamptz{})
		mutate(&row)
		got, err := authenticateSession(
			context.Background(),
			func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) { return row, nil },
			validTouch, func() time.Time { return now }, time.Hour, time.Hour, token,
		)
		if err == nil || !reflect.DeepEqual(got, SessionAuthentication{}) {
			t.Fatalf("invalid-row authenticateSession() = (%+v, %v), want zero/error", got, err)
		}
	}
}

func TestAuthenticateSessionRedactsDatabaseFailuresAndPreservesCancellation(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-session-database-cause"
	now := time.Date(2026, time.September, 1, 14, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x19}, 32))
	for _, test := range []struct {
		name  string
		load  func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error)
		touch func(context.Context, db.TouchSessionParams) (int64, error)
	}{
		{name: "load", load: func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			return db.GetActiveSessionRow{}, errors.New(secret)
		}, touch: func(context.Context, db.TouchSessionParams) (int64, error) { panic("touch must not run") }},
		{name: "touch", load: func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			return activeSessionRow(now, "member", now.Add(-sessionLastSeenWriteInterval), now, pgtype.Timestamptz{}), nil
		}, touch: func(context.Context, db.TouchSessionParams) (int64, error) { return 0, errors.New(secret) }},
		{name: "invalid touch count", load: func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			return activeSessionRow(now, "member", now.Add(-sessionLastSeenWriteInterval), now, pgtype.Timestamptz{}), nil
		}, touch: func(context.Context, db.TouchSessionParams) (int64, error) { return 2, nil }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := authenticateSession(context.Background(), test.load, test.touch, func() time.Time { return now }, time.Hour, time.Hour, token)
			if err == nil || !reflect.DeepEqual(got, SessionAuthentication{}) || strings.Contains(err.Error(), secret) {
				t.Fatalf("authenticateSession() = (%+v, %v), want zero/redacted error", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	got, err := authenticateSession(
		ctx,
		func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			cancel()
			return db.GetActiveSessionRow{}, errors.New(secret)
		},
		func(context.Context, db.TouchSessionParams) (int64, error) { panic("touch must not run") },
		func() time.Time { return now }, time.Hour, time.Hour, token,
	)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, SessionAuthentication{}) || strings.Contains(err.Error(), secret) {
		t.Fatalf("canceled authenticateSession() = (%+v, %v)", got, err)
	}
	touchContext, cancelTouch := context.WithCancel(context.Background())
	got, err = authenticateSession(
		touchContext,
		func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error) {
			return activeSessionRow(now, "member", now.Add(-sessionLastSeenWriteInterval), now, pgtype.Timestamptz{}), nil
		},
		func(context.Context, db.TouchSessionParams) (int64, error) {
			cancelTouch()
			return 0, errors.New(secret)
		},
		func() time.Time { return now }, time.Hour, time.Hour, token,
	)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, SessionAuthentication{}) || strings.Contains(err.Error(), secret) {
		t.Fatalf("touch-canceled authenticateSession() = (%+v, %v)", got, err)
	}
}

func activeSessionRow(now time.Time, role string, lastSeenAt, validatedAt time.Time, mutedUntil pgtype.Timestamptz) db.GetActiveSessionRow {
	return db.GetActiveSessionRow{
		SessionID: 7, UserID: 42,
		IssuedAt:    pgtype.Timestamptz{Time: now.Add(-2 * time.Hour), Valid: true},
		LastSeenAt:  pgtype.Timestamptz{Time: lastSeenAt, Valid: true},
		ValidatedAt: pgtype.Timestamptz{Time: validatedAt, Valid: true},
		ExpiresAt:   pgtype.Timestamptz{Time: now.Add(2 * time.Hour), Valid: true},
		Role:        role, MutedUntil: mutedUntil,
	}
}
