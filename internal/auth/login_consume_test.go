package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const validEncodedLoginState = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestConsumeLoginAttemptAtomicallyRecoversValidatedInitialAttempt(t *testing.T) {
	t.Parallel()

	material, protected := testProtectedLoginMaterial(t)
	now := time.Date(2026, time.September, 1, 10, 45, 0, 987654321, time.FixedZone("test", -5*60*60))
	wantConsumedAt := now.UTC().Truncate(time.Microsecond)
	ctx := context.WithValue(context.Background(), consumeLoginContextKey{}, "preserved")
	consumeCalls := 0
	consume := func(gotContext context.Context, params db.ConsumeOIDCLoginAttemptParams) (db.OidcLoginAttempt, error) {
		consumeCalls++
		wantHash := sha256.Sum256([]byte(material.state))
		if gotContext != ctx || gotContext.Value(consumeLoginContextKey{}) != "preserved" ||
			!params.ConsumedAt.Valid || !params.ConsumedAt.Time.Equal(wantConsumedAt) ||
			!bytes.Equal(params.StateHash, wantHash[:]) {
			t.Fatal("consume received incorrect context or parameters")
		}
		return db.OidcLoginAttempt{
			StateHash:              append([]byte(nil), protected.stateHash[:]...),
			NonceCiphertext:        append([]byte(nil), protected.nonceCiphertext[:]...),
			PkceVerifierCiphertext: append([]byte(nil), protected.pkceVerifierCiphertext[:]...),
			Purpose:                "login",
			ReturnPath:             "/bb/topics/42",
		}, nil
	}
	validateCalls := 0
	validate := func(raw string) (string, error) {
		validateCalls++
		if raw != "/bb/topics/42" {
			t.Fatalf("validator input = %q", raw)
		}
		return raw, nil
	}

	got, err := consumeLoginAttempt(ctx, consume, func() time.Time { return now }, validate, "login", material.state)
	if err != nil || got.material != material || got.returnPath != "/bb/topics/42" || consumeCalls != 1 || validateCalls != 1 {
		t.Fatalf("consumeLoginAttempt() = (%+v, %v), consume calls = %d, validate calls = %d", got, err, consumeCalls, validateCalls)
	}
}

func TestConsumeLoginAttemptRecoversRevalidationSessionBinding(t *testing.T) {
	t.Parallel()

	material, protected := testProtectedLoginMaterial(t)
	row := db.OidcLoginAttempt{
		StateHash:              append([]byte(nil), protected.stateHash[:]...),
		NonceCiphertext:        append([]byte(nil), protected.nonceCiphertext[:]...),
		PkceVerifierCiphertext: append([]byte(nil), protected.pkceVerifierCiphertext[:]...),
		Purpose:                "revalidate",
		SessionID:              pgtype.Int8{Int64: 73, Valid: true},
		ReturnPath:             "/bb/topics/42",
	}
	got, err := consumeLoginAttempt(
		context.Background(), returningLoginAttempt(row), time.Now,
		func(raw string) (string, error) { return raw, nil }, "revalidate", material.state,
	)
	if err != nil || got.material != material || got.returnPath != row.ReturnPath || got.sessionID != 73 {
		t.Fatalf("consumeLoginAttempt() = (%+v, %v)", got, err)
	}
}

func TestConsumeLoginAttemptRejectsDependenciesAndStateBeforeDatabase(t *testing.T) {
	t.Parallel()

	validConsume := func(context.Context, db.ConsumeOIDCLoginAttemptParams) (db.OidcLoginAttempt, error) {
		panic("consume must not be called")
	}
	validClock := func() time.Time { return time.Date(2026, time.September, 1, 10, 45, 0, 0, time.UTC) }
	validValidate := func(raw string) (string, error) { return raw, nil }
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name      string
		ctx       context.Context
		consume   consumeOIDCLoginAttempt
		clock     func() time.Time
		validate  func(string) (string, error)
		state     string
		wantCause error
	}{
		{name: "nil context", consume: validConsume, clock: validClock, validate: validValidate},
		{name: "nil consume", ctx: context.Background(), clock: validClock, validate: validValidate},
		{name: "nil clock", ctx: context.Background(), consume: validConsume, validate: validValidate},
		{name: "nil validator", ctx: context.Background(), consume: validConsume, clock: validClock},
		{name: "canceled context", ctx: canceledContext, consume: validConsume, clock: validClock, validate: validValidate, state: validEncodedLoginState, wantCause: context.Canceled},
		{name: "short state", ctx: context.Background(), consume: validConsume, clock: validClock, validate: validValidate, state: "short"},
		{name: "noncanonical state", ctx: context.Background(), consume: validConsume, clock: validClock, validate: validValidate, state: "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"},
		{name: "zero clock", ctx: context.Background(), consume: validConsume, clock: func() time.Time { return time.Time{} }, validate: validValidate, state: validEncodedLoginState},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := consumeLoginAttempt(test.ctx, test.consume, test.clock, test.validate, "login", test.state)
			if err == nil || got != (consumedLoginAttempt{}) {
				t.Fatalf("consumeLoginAttempt() returned zero result = %v, error = %v", got == (consumedLoginAttempt{}), err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
	if got, err := consumeLoginAttempt(context.Background(), validConsume, validClock, validValidate, "other", validEncodedLoginState); err == nil || got != (consumedLoginAttempt{}) {
		t.Fatalf("consumeLoginAttempt(invalid purpose) = (%+v, %v), want zero/error", got, err)
	}
}

func TestConsumeLoginAttemptConsumesFailuresWithoutReturningMaterial(t *testing.T) {
	t.Parallel()

	material, protected := testProtectedLoginMaterial(t)
	now := time.Date(2026, time.September, 1, 10, 45, 0, 0, time.UTC)
	consumeCause := errors.New("consume failed")
	validationCause := errors.New("stored return path rejected")
	baseRow := db.OidcLoginAttempt{
		StateHash:              append([]byte(nil), protected.stateHash[:]...),
		NonceCiphertext:        append([]byte(nil), protected.nonceCiphertext[:]...),
		PkceVerifierCiphertext: append([]byte(nil), protected.pkceVerifierCiphertext[:]...),
		Purpose:                "login",
		ReturnPath:             "/bb/",
	}
	for _, test := range []struct {
		name      string
		consume   consumeOIDCLoginAttempt
		validate  func(string) (string, error)
		wantCause error
	}{
		{name: "missing or replayed attempt", consume: func(context.Context, db.ConsumeOIDCLoginAttemptParams) (db.OidcLoginAttempt, error) {
			return db.OidcLoginAttempt{}, pgx.ErrNoRows
		}, validate: panicReturnPathValidator, wantCause: pgx.ErrNoRows},
		{name: "consume failure", consume: func(context.Context, db.ConsumeOIDCLoginAttemptParams) (db.OidcLoginAttempt, error) {
			return db.OidcLoginAttempt{}, consumeCause
		}, validate: panicReturnPathValidator, wantCause: consumeCause},
		{name: "wrong purpose", consume: returningLoginAttempt(withAttemptPurpose(baseRow, "revalidate")), validate: panicReturnPathValidator},
		{name: "unexpected session", consume: returningLoginAttempt(withAttemptSession(baseRow, 9)), validate: panicReturnPathValidator},
		{name: "stored return path rejected", consume: returningLoginAttempt(baseRow), validate: func(string) (string, error) { return "", validationCause }, wantCause: validationCause},
		{name: "validator empty result", consume: returningLoginAttempt(baseRow), validate: func(string) (string, error) { return "", nil }},
		{name: "protected material rejected", consume: returningLoginAttempt(withAttemptNonce(baseRow, []byte("tampered"))), validate: func(raw string) (string, error) { return raw, nil }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := consumeLoginAttempt(context.Background(), test.consume, func() time.Time { return now }, test.validate, "login", material.state)
			if err == nil || got != (consumedLoginAttempt{}) {
				t.Fatalf("consumeLoginAttempt() returned zero result = %v, error = %v", got == (consumedLoginAttempt{}), err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
	for _, session := range []pgtype.Int8{{}, {Valid: true}, {Int64: -1, Valid: true}} {
		row := withAttemptPurpose(baseRow, "revalidate")
		row.SessionID = session
		got, err := consumeLoginAttempt(
			context.Background(), returningLoginAttempt(row), func() time.Time { return now },
			panicReturnPathValidator, "revalidate", material.state,
		)
		if err == nil || got != (consumedLoginAttempt{}) {
			t.Fatalf("consumeLoginAttempt(revalidation session %#v) = (%+v, %v), want zero/error", session, got, err)
		}
	}
}

func testProtectedLoginMaterial(t *testing.T) (loginMaterial, protectedLoginMaterial) {
	t.Helper()
	material, err := generateLoginMaterial(bytes.NewReader(sequentialBytes(96)))
	if err != nil {
		t.Fatalf("generateLoginMaterial() returned error: %v", err)
	}
	protected, err := protectLoginMaterial(material, bytes.NewReader(sequentialBytes(24)))
	if err != nil {
		t.Fatalf("protectLoginMaterial() returned error: %v", err)
	}
	return material, protected
}

func returningLoginAttempt(row db.OidcLoginAttempt) consumeOIDCLoginAttempt {
	return func(context.Context, db.ConsumeOIDCLoginAttemptParams) (db.OidcLoginAttempt, error) { return row, nil }
}

func withAttemptPurpose(row db.OidcLoginAttempt, purpose string) db.OidcLoginAttempt {
	row.Purpose = purpose
	return row
}

func withAttemptSession(row db.OidcLoginAttempt, sessionID int64) db.OidcLoginAttempt {
	row.SessionID = pgtype.Int8{Int64: sessionID, Valid: true}
	return row
}

func withAttemptNonce(row db.OidcLoginAttempt, nonce []byte) db.OidcLoginAttempt {
	row.NonceCiphertext = nonce
	return row
}

func panicReturnPathValidator(string) (string, error) {
	panic("validator must not be called")
}

type consumeLoginContextKey struct{}
