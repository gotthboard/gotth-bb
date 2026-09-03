package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
)

func TestRevokeSessionHashesExactCredentialAndReturnsOutcome(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 15, 30, 0, 123456789, time.FixedZone("offset", -5*60*60))
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, sessionTokenBytes))
	wantHash := sha256.Sum256([]byte(token))
	for _, rows := range []int64{0, 1} {
		rows := rows
		t.Run(time.Duration(rows).String(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			got, err := revokeSession(
				context.Background(),
				func(_ context.Context, params db.RevokeSessionParams) (int64, error) {
					calls++
					if !params.ObservedAt.Valid || !params.ObservedAt.Time.Equal(now.UTC().Truncate(time.Microsecond)) ||
						!bytes.Equal(params.TokenHash, wantHash[:]) {
						t.Fatalf("revoke params = %+v", params)
					}
					return rows, nil
				},
				func() time.Time { return now }, token,
			)
			if err != nil || got != (rows == 1) || calls != 1 {
				t.Fatalf("revokeSession() = (%t, %v, calls %d), want (%t, nil, 1)", got, err, calls, rows == 1)
			}
		})
	}
}

func TestRevokeSessionTreatsMissingAndMalformedCredentialsAsNoOp(t *testing.T) {
	t.Parallel()

	valid := base64.RawURLEncoding.EncodeToString(make([]byte, sessionTokenBytes))
	for _, token := range []string{"", valid[:len(valid)-1], valid[:len(valid)-1] + "*"} {
		token := token
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			got, err := revokeSession(
				context.Background(),
				func(context.Context, db.RevokeSessionParams) (int64, error) { panic("revoke must not run") },
				time.Now, token,
			)
			if err != nil || got {
				t.Fatalf("revokeSession(%q) = (%t, %v), want false/nil", token, got, err)
			}
		})
	}
}

func TestRevokeSessionRejectsInvalidDependenciesAndResults(t *testing.T) {
	t.Parallel()

	token := base64.RawURLEncoding.EncodeToString(make([]byte, sessionTokenBytes))
	validRevoke := func(context.Context, db.RevokeSessionParams) (int64, error) { return 0, nil }
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name   string
		ctx    context.Context
		revoke revokeSessionByHash
		clock  func() time.Time
	}{
		{name: "nil context", revoke: validRevoke, clock: time.Now},
		{name: "nil writer", ctx: context.Background(), clock: time.Now},
		{name: "nil clock", ctx: context.Background(), revoke: validRevoke},
		{name: "canceled context", ctx: canceled, revoke: validRevoke, clock: time.Now},
		{name: "zero clock", ctx: context.Background(), revoke: validRevoke, clock: func() time.Time { return time.Time{} }},
		{name: "negative rows", ctx: context.Background(), revoke: func(context.Context, db.RevokeSessionParams) (int64, error) { return -1, nil }, clock: time.Now},
		{name: "multiple rows", ctx: context.Background(), revoke: func(context.Context, db.RevokeSessionParams) (int64, error) { return 2, nil }, clock: time.Now},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := revokeSession(test.ctx, test.revoke, test.clock, token); err == nil || got {
				t.Fatalf("revokeSession() = (%t, %v), want false/error", got, err)
			}
		})
	}
}

func TestRevokeSessionRedactsDatabaseFailureAndPreservesCancellation(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-revocation-database-cause"
	token := base64.RawURLEncoding.EncodeToString(make([]byte, sessionTokenBytes))
	got, err := revokeSession(
		context.Background(),
		func(context.Context, db.RevokeSessionParams) (int64, error) { return 0, errors.New(secret) },
		time.Now, token,
	)
	if err == nil || got || strings.Contains(err.Error(), secret) {
		t.Fatalf("database failure = (%t, %v), want false/redacted error", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	got, err = revokeSession(
		ctx,
		func(context.Context, db.RevokeSessionParams) (int64, error) {
			cancel()
			return 0, errors.New(secret)
		},
		time.Now, token,
	)
	if !errors.Is(err, context.Canceled) || got || strings.Contains(err.Error(), secret) {
		t.Fatalf("canceled failure = (%t, %v), want false/canceled", got, err)
	}
}
