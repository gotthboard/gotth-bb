package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type createdRevalidatedSession struct {
	token     string
	userID    int64
	sessionID int64
	expiresAt time.Time
}

// rotateRevalidatedSession replaces one exact active browser session after a
// fresh OIDC verification. It locks the old session, local user, and stored
// identity; requires immutable issuer/subject continuity; refreshes only the
// approved profile snapshot; inserts the replacement; and revokes the old
// credential in one transaction. No replacement token escapes before commit.
//
// Complexity: local credential and timestamp work is tight Theta(1). The
// transaction performs one indexed locking read, two profile/verification
// writes, one session insert, one indexed revoke, and one commit. Database wait
// time is unbounded by this function and owned by the caller context.
func rotateRevalidatedSession(
	ctx context.Context,
	beginner transactionBeginner,
	entropy io.Reader,
	clock func() time.Time,
	maximumAge time.Duration,
	idleTimeout time.Duration,
	sessionID int64,
	oldToken string,
	claims verifiedIdentityClaims,
) (createdRevalidatedSession, error) {
	if ctx == nil {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation context is required")
	}
	if beginner == nil {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation transaction beginner is required")
	}
	if entropy == nil {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation entropy is required")
	}
	if clock == nil {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation clock is required")
	}
	if maximumAge < time.Microsecond {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation maximum age is below database precision")
	}
	if idleTimeout < time.Microsecond || idleTimeout > maximumAge {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation idle timeout is outside the session lifetime")
	}
	if sessionID <= 0 {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation session ID must be positive")
	}
	if claims.issuer == "" || claims.subject == "" || claims.displayName == "" {
		return createdRevalidatedSession{}, fmt.Errorf("verified identity claims are incomplete")
	}
	if err := ctx.Err(); err != nil {
		return createdRevalidatedSession{}, fmt.Errorf("rotate revalidated session: %w", err)
	}
	if len(oldToken) != sessionTokenEncodedBytes {
		return createdRevalidatedSession{}, fmt.Errorf("old session credential is invalid")
	}
	var oldEncoded [sessionTokenEncodedBytes]byte
	copy(oldEncoded[:], oldToken)
	defer clear(oldEncoded[:])
	var oldDecoded [sessionTokenBytes]byte
	decodedLength, err := base64.RawURLEncoding.Strict().Decode(oldDecoded[:], oldEncoded[:])
	defer clear(oldDecoded[:])
	if err != nil || decodedLength != sessionTokenBytes {
		return createdRevalidatedSession{}, fmt.Errorf("old session credential is invalid")
	}
	now := clock()
	if now.IsZero() {
		return createdRevalidatedSession{}, fmt.Errorf("session rotation clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	expiresAt := now.Add(maximumAge).UTC().Truncate(time.Microsecond)
	idleCutoff := now.Add(-idleTimeout).UTC().Truncate(time.Microsecond)
	oldTokenHash := sha256.Sum256(oldEncoded[:])
	defer clear(oldTokenHash[:])
	material, err := generateSessionMaterial(entropy)
	if err != nil {
		return createdRevalidatedSession{}, err
	}
	defer clear(material.tokenHash[:])
	toNullableText := func(value *string) pgtype.Text {
		if value == nil {
			return pgtype.Text{}
		}
		return pgtype.Text{String: *value, Valid: true}
	}
	observedAt := pgtype.Timestamptz{Time: now, Valid: true}
	created := createdRevalidatedSession{token: material.token, expiresAt: expiresAt}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		active, err := queries.GetActiveSessionForRotation(ctx, db.GetActiveSessionForRotationParams{
			SessionID: sessionID, TokenHash: oldTokenHash[:], ObservedAt: observedAt,
			IdleCutoff: pgtype.Timestamptz{Time: idleCutoff, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("lock active session for rotation: %w", err)
		}
		if active.UserID <= 0 || active.Issuer == "" || active.Subject == "" || !active.ExpiresAt.Valid ||
			!active.ExpiresAt.Time.After(now) {
			return fmt.Errorf("active session rotation row is incomplete")
		}
		if active.Issuer != claims.issuer || active.Subject != claims.subject {
			return fmt.Errorf("revalidated identity does not match the active session")
		}
		user, err := queries.UpdateUserFromOIDC(ctx, db.UpdateUserFromOIDCParams{
			DisplayName: claims.displayName,
			Email:       toNullableText(claims.email),
			AvatarUrl:   toNullableText(claims.avatarURL),
			LoginAt:     observedAt,
			UserID:      active.UserID,
		})
		if err != nil {
			return fmt.Errorf("refresh revalidated user profile: %w", err)
		}
		if user.ID != active.UserID {
			return fmt.Errorf("refreshed revalidated user does not match the locked session")
		}
		if err := queries.UpdateExternalIdentityVerification(ctx, db.UpdateExternalIdentityVerificationParams{
			VerifiedAt: observedAt, UserID: active.UserID,
		}); err != nil {
			return fmt.Errorf("refresh external identity verification: %w", err)
		}
		replacement, err := queries.InsertSession(ctx, db.InsertSessionParams{
			TokenHash: material.tokenHash[:], UserID: active.UserID, IssuedAt: observedAt,
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("insert rotated session: %w", err)
		}
		if replacement.ID <= 0 || replacement.UserID != active.UserID {
			return fmt.Errorf("insert rotated session returned an invalid row")
		}
		revoked, err := queries.RevokeSessionForRotation(ctx, db.RevokeSessionForRotationParams{
			ObservedAt: observedAt, SessionID: sessionID, TokenHash: oldTokenHash[:],
		})
		if err != nil {
			return fmt.Errorf("revoke old session after rotation: %w", err)
		}
		if revoked != 1 {
			return fmt.Errorf("revoke old session after rotation affected an invalid row count")
		}
		created.userID = active.UserID
		created.sessionID = replacement.ID
		return nil
	})
	if err != nil {
		return createdRevalidatedSession{}, fmt.Errorf("rotate revalidated identity and session: %w", err)
	}
	return created, nil
}
