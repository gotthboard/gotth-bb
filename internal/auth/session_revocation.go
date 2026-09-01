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

type revokeSessionByHash func(context.Context, db.RevokeSessionParams) (int64, error)

// revokeSession validates and hashes one opaque browser credential, then marks
// at most its exact server-side session revoked. Missing and malformed tokens
// are idempotent no-ops. Database causes are redacted while context cancellation
// remains inspectable by the process boundary.
//
// Complexity: credential validation and SHA-256 hashing are tight Theta(1) time
// and auxiliary space over fixed 43/32-byte material. One delegated indexed
// update has cost R; total time is O(R), Omega(1), with no retry or detached
// work. Fixed credential/hash buffers are cleared before return.
func revokeSession(
	ctx context.Context,
	revoke revokeSessionByHash,
	clock func() time.Time,
	token string,
) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("session revocation context is required")
	}
	if revoke == nil {
		return false, fmt.Errorf("session revocation writer is required")
	}
	if clock == nil {
		return false, fmt.Errorf("session revocation clock is required")
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("revoke session: %w", err)
	}
	if len(token) != sessionTokenEncodedBytes {
		return false, nil
	}
	var encoded [sessionTokenEncodedBytes]byte
	copy(encoded[:], token)
	defer clear(encoded[:])
	var decoded [sessionTokenBytes]byte
	decodedLength, err := base64.RawURLEncoding.Strict().Decode(decoded[:], encoded[:])
	defer clear(decoded[:])
	if err != nil || decodedLength != sessionTokenBytes {
		return false, nil
	}
	now := clock()
	if now.IsZero() {
		return false, fmt.Errorf("session revocation clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	tokenHash := sha256.Sum256(encoded[:])
	defer clear(tokenHash[:])
	revoked, err := revoke(ctx, db.RevokeSessionParams{
		ObservedAt: pgtype.Timestamptz{Time: now, Valid: true},
		TokenHash:  tokenHash[:],
	})
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return false, fmt.Errorf("revoke session: %w", contextError)
		}
		return false, fmt.Errorf("revoke session failed")
	}
	if revoked < 0 || revoked > 1 {
		return false, fmt.Errorf("revoke session returned an invalid row count")
	}
	return revoked == 1, nil
}
