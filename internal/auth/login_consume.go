package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type consumeOIDCLoginAttempt func(context.Context, db.ConsumeOIDCLoginAttemptParams) (db.OidcLoginAttempt, error)

type consumedInitialLogin struct {
	material   loginMaterial
	returnPath string
}

type consumedLoginAttempt struct {
	material   loginMaterial
	returnPath string
	sessionID  int64
}

// consumeLoginAttempt validates and hashes the callback state, atomically
// consumes one live row of the required purpose, validates its session binding,
// revalidates its stored navigation target, and authenticates its protected
// nonce and PKCE verifier. The row remains consumed if any post-consumption
// validation fails.
//
// Complexity: local time and auxiliary space are tight Theta(1) because state
// and protected values have fixed bounds. Return-path validation and one
// database update/return round trip are delegated; no retry occurs.
func consumeLoginAttempt(
	ctx context.Context,
	consume consumeOIDCLoginAttempt,
	clock func() time.Time,
	validateReturnPath func(string) (string, error),
	expectedPurpose string,
	stateValue string,
) (consumedLoginAttempt, error) {
	if ctx == nil {
		return consumedLoginAttempt{}, fmt.Errorf("login context is required")
	}
	if consume == nil {
		return consumedLoginAttempt{}, fmt.Errorf("login-attempt consumer is required")
	}
	if clock == nil {
		return consumedLoginAttempt{}, fmt.Errorf("login clock is required")
	}
	if validateReturnPath == nil {
		return consumedLoginAttempt{}, fmt.Errorf("login return-path validator is required")
	}
	if expectedPurpose != "login" && expectedPurpose != "revalidate" {
		return consumedLoginAttempt{}, fmt.Errorf("login-attempt purpose is invalid")
	}
	if err := ctx.Err(); err != nil {
		return consumedLoginAttempt{}, fmt.Errorf("consume login: %w", err)
	}
	decodedState, err := base64.RawURLEncoding.Strict().DecodeString(stateValue)
	defer clear(decodedState)
	if err != nil || len(decodedState) != loginSecretBytes {
		return consumedLoginAttempt{}, fmt.Errorf("login state has an invalid encoding or length")
	}
	stateValueBytes := []byte(stateValue)
	stateHash := sha256.Sum256(stateValueBytes)
	clear(stateValueBytes)
	now := clock()
	if now.IsZero() {
		return consumedLoginAttempt{}, fmt.Errorf("login clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	attempt, err := consume(ctx, db.ConsumeOIDCLoginAttemptParams{
		ConsumedAt: pgtype.Timestamptz{Time: now, Valid: true},
		StateHash:  stateHash[:],
	})
	if err != nil {
		return consumedLoginAttempt{}, fmt.Errorf("consume login attempt: %w", err)
	}
	if attempt.Purpose != expectedPurpose {
		return consumedLoginAttempt{}, fmt.Errorf("consumed login attempt has an unexpected purpose")
	}
	sessionID := int64(0)
	if expectedPurpose == "login" {
		if attempt.SessionID.Valid {
			return consumedLoginAttempt{}, fmt.Errorf("consumed initial-login attempt has session metadata")
		}
	} else {
		if !attempt.SessionID.Valid || attempt.SessionID.Int64 <= 0 {
			return consumedLoginAttempt{}, fmt.Errorf("consumed revalidation attempt lacks a valid session")
		}
		sessionID = attempt.SessionID.Int64
	}
	returnPath, err := validateReturnPath(attempt.ReturnPath)
	if err != nil {
		return consumedLoginAttempt{}, fmt.Errorf("validate consumed login return path: %w", err)
	}
	if returnPath == "" {
		return consumedLoginAttempt{}, fmt.Errorf("login return-path validator returned an empty path")
	}
	material, err := recoverLoginMaterial(
		stateValue,
		attempt.StateHash,
		attempt.NonceCiphertext,
		attempt.PkceVerifierCiphertext,
	)
	if err != nil {
		return consumedLoginAttempt{}, fmt.Errorf("recover consumed login attempt: %w", err)
	}
	return consumedLoginAttempt{material: material, returnPath: returnPath, sessionID: sessionID}, nil
}
