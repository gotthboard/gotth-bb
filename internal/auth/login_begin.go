package auth

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

const initialLoginAttemptLifetime = 5 * time.Minute

type insertOIDCLoginAttempt func(context.Context, db.InsertOIDCLoginAttemptParams) error

// beginInitialLogin validates the navigation target before touching entropy,
// generates and protects one login attempt, synchronously persists its exact
// database representation, and returns no browser material unless insertion
// succeeds.
//
// Complexity: local cryptographic work, material sizes, and timestamp work are
// tight Theta(1). Return-path validation and one database insert are delegated;
// no retry or background work occurs.
func beginInitialLogin(
	ctx context.Context,
	insert insertOIDCLoginAttempt,
	entropy io.Reader,
	clock func() time.Time,
	validateReturnPath func(string) (string, error),
	rawReturnPath string,
) (loginMaterial, error) {
	if ctx == nil {
		return loginMaterial{}, fmt.Errorf("login context is required")
	}
	if insert == nil {
		return loginMaterial{}, fmt.Errorf("login-attempt insert is required")
	}
	if clock == nil {
		return loginMaterial{}, fmt.Errorf("login clock is required")
	}
	if validateReturnPath == nil {
		return loginMaterial{}, fmt.Errorf("login return-path validator is required")
	}
	if err := ctx.Err(); err != nil {
		return loginMaterial{}, fmt.Errorf("begin login: %w", err)
	}
	returnPath, err := validateReturnPath(rawReturnPath)
	if err != nil {
		return loginMaterial{}, fmt.Errorf("validate login return path: %w", err)
	}
	if returnPath == "" {
		return loginMaterial{}, fmt.Errorf("login return-path validator returned an empty path")
	}
	now := clock()
	if now.IsZero() {
		return loginMaterial{}, fmt.Errorf("login clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	material, err := generateLoginMaterial(entropy)
	if err != nil {
		return loginMaterial{}, err
	}
	protected, err := protectLoginMaterial(material, entropy)
	if err != nil {
		return loginMaterial{}, err
	}
	if err := insert(ctx, db.InsertOIDCLoginAttemptParams{
		StateHash:              protected.stateHash[:],
		NonceCiphertext:        protected.nonceCiphertext[:],
		PkceVerifierCiphertext: protected.pkceVerifierCiphertext[:],
		Purpose:                "login",
		SessionID:              pgtype.Int8{},
		ReturnPath:             returnPath,
		CreatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
		ExpiresAt:              pgtype.Timestamptz{Time: now.Add(initialLoginAttemptLifetime), Valid: true},
	}); err != nil {
		return loginMaterial{}, fmt.Errorf("insert login attempt: %w", err)
	}
	return material, nil
}
