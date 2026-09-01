package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type consumeOIDCLoginAttempt func(context.Context, db.ConsumeOIDCLoginAttemptParams) (db.OidcLoginAttempt, error)

type consumedInitialLogin struct {
	material   loginMaterial
	returnPath string
}

// consumeInitialLogin validates and hashes the callback state, atomically
// consumes one live initial-login row, revalidates its stored navigation
// target, and authenticates its protected nonce and PKCE verifier. The row
// remains consumed if any post-consumption validation fails.
//
// Complexity: local time and auxiliary space are tight Theta(1) because state
// and protected values have fixed bounds. Return-path validation and one
// database update/return round trip are delegated; no retry occurs.
func consumeInitialLogin(
	ctx context.Context,
	consume consumeOIDCLoginAttempt,
	clock func() time.Time,
	validateReturnPath func(string) (string, error),
	stateValue string,
) (consumedInitialLogin, error) {
	if ctx == nil {
		return consumedInitialLogin{}, fmt.Errorf("login context is required")
	}
	if consume == nil {
		return consumedInitialLogin{}, fmt.Errorf("login-attempt consumer is required")
	}
	if clock == nil {
		return consumedInitialLogin{}, fmt.Errorf("login clock is required")
	}
	if validateReturnPath == nil {
		return consumedInitialLogin{}, fmt.Errorf("login return-path validator is required")
	}
	if err := ctx.Err(); err != nil {
		return consumedInitialLogin{}, fmt.Errorf("consume login: %w", err)
	}
	decodedState, err := base64.RawURLEncoding.Strict().DecodeString(stateValue)
	defer clear(decodedState)
	if err != nil || len(decodedState) != loginSecretBytes {
		return consumedInitialLogin{}, fmt.Errorf("login state has an invalid encoding or length")
	}
	stateValueBytes := []byte(stateValue)
	stateHash := sha256.Sum256(stateValueBytes)
	clear(stateValueBytes)
	now := clock()
	if now.IsZero() {
		return consumedInitialLogin{}, fmt.Errorf("login clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	attempt, err := consume(ctx, db.ConsumeOIDCLoginAttemptParams{
		ConsumedAt: pgtype.Timestamptz{Time: now, Valid: true},
		StateHash:  stateHash[:],
	})
	if err != nil {
		return consumedInitialLogin{}, fmt.Errorf("consume login attempt: %w", err)
	}
	if attempt.Purpose != "login" || attempt.SessionID.Valid {
		return consumedInitialLogin{}, fmt.Errorf("consumed login attempt has invalid purpose or session metadata")
	}
	returnPath, err := validateReturnPath(attempt.ReturnPath)
	if err != nil {
		return consumedInitialLogin{}, fmt.Errorf("validate consumed login return path: %w", err)
	}
	if returnPath == "" {
		return consumedInitialLogin{}, fmt.Errorf("login return-path validator returned an empty path")
	}
	material, err := recoverLoginMaterial(
		stateValue,
		attempt.StateHash,
		attempt.NonceCiphertext,
		attempt.PkceVerifierCiphertext,
	)
	if err != nil {
		return consumedInitialLogin{}, fmt.Errorf("recover consumed login attempt: %w", err)
	}
	return consumedInitialLogin{material: material, returnPath: returnPath}, nil
}
