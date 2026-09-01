package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	sessionTokenEncodedBytes     = (sessionTokenBytes*8 + 5) / 6
	sessionLastSeenWriteInterval = 5 * time.Minute
)

// Role is the closed local forum role ordering. Authorization callers use the
// named constants rather than database strings or numeric literals.
type Role uint8

const (
	RoleMember Role = iota + 1
	RoleModerator
	RoleAdministrator
)

// AccessContext contains only current local facts used by authorization.
type AccessContext struct {
	Authenticated bool
	UserID        int64
	Role          Role
	GroupIDs      []int64
	Suspended     bool
	MutedUntil    *time.Time
	ValidatedAt   time.Time
}

// SessionAuthentication carries the local access facts plus the freshness
// decision that protected HTTP routes must enforce.
type SessionAuthentication struct {
	Access               AccessContext
	RequiresRevalidation bool
}

// authenticateSession validates and hashes one opaque browser credential,
// loads one current local access snapshot, and performs a conditional activity
// write only at the fixed throttle boundary. Missing credentials and no-row
// lookups are anonymous; database failures fail closed and are redacted.
//
// Complexity: credential validation, hashing, row validation, and result
// projection are tight Theta(1) time and auxiliary space over fixed 43/32-byte
// material. With one delegated indexed lookup cost L and optional conditional
// touch cost T, total time is O(L+T), Omega(L), and auxiliary space O(1) beyond
// driver state. No operation is retried or detached.
func authenticateSession(
	ctx context.Context,
	load func(context.Context, db.GetActiveSessionParams) (db.GetActiveSessionRow, error),
	touch func(context.Context, db.TouchSessionParams) (int64, error),
	clock func() time.Time,
	idleTimeout time.Duration,
	revalidationInterval time.Duration,
	token string,
) (SessionAuthentication, error) {
	if ctx == nil {
		return SessionAuthentication{}, fmt.Errorf("session authentication context is required")
	}
	if load == nil {
		return SessionAuthentication{}, fmt.Errorf("active-session loader is required")
	}
	if touch == nil {
		return SessionAuthentication{}, fmt.Errorf("session activity writer is required")
	}
	if clock == nil {
		return SessionAuthentication{}, fmt.Errorf("session authentication clock is required")
	}
	if idleTimeout < time.Second {
		return SessionAuthentication{}, fmt.Errorf("session idle timeout is below supported precision")
	}
	if revalidationInterval < time.Second {
		return SessionAuthentication{}, fmt.Errorf("session revalidation interval is below supported precision")
	}
	if err := ctx.Err(); err != nil {
		return SessionAuthentication{}, fmt.Errorf("authenticate session: %w", err)
	}
	if len(token) != sessionTokenEncodedBytes {
		return SessionAuthentication{}, nil
	}
	var encoded [sessionTokenEncodedBytes]byte
	copy(encoded[:], token)
	defer clear(encoded[:])
	var decoded [sessionTokenBytes]byte
	decodedLength, err := base64.RawURLEncoding.Strict().Decode(decoded[:], encoded[:])
	defer clear(decoded[:])
	if err != nil || decodedLength != sessionTokenBytes {
		return SessionAuthentication{}, nil
	}
	now := clock()
	if now.IsZero() {
		return SessionAuthentication{}, fmt.Errorf("session authentication clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	idleCutoff := now.Add(-idleTimeout)
	touchInterval := sessionLastSeenWriteInterval
	if halfIdleTimeout := idleTimeout / 2; halfIdleTimeout < touchInterval {
		touchInterval = halfIdleTimeout
	}
	tokenHash := sha256.Sum256(encoded[:])
	defer clear(tokenHash[:])
	row, err := load(ctx, db.GetActiveSessionParams{
		TokenHash:  tokenHash[:],
		ObservedAt: pgtype.Timestamptz{Time: now, Valid: true},
		IdleCutoff: pgtype.Timestamptz{Time: idleCutoff, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SessionAuthentication{}, nil
		}
		if contextError := ctx.Err(); contextError != nil {
			return SessionAuthentication{}, fmt.Errorf("load active session: %w", contextError)
		}
		return SessionAuthentication{}, fmt.Errorf("load active session failed")
	}
	if row.SessionID <= 0 || row.UserID <= 0 || !row.IssuedAt.Valid || !row.LastSeenAt.Valid ||
		!row.ValidatedAt.Valid || !row.ExpiresAt.Valid || row.IssuedAt.Time.After(now) ||
		row.LastSeenAt.Time.Before(row.IssuedAt.Time) || row.LastSeenAt.Time.After(now) ||
		row.ValidatedAt.Time.Before(row.IssuedAt.Time) || row.ValidatedAt.Time.After(now) ||
		!row.ExpiresAt.Time.After(now) || row.LastSeenAt.Time.After(row.ExpiresAt.Time) ||
		row.ValidatedAt.Time.After(row.ExpiresAt.Time) || !row.LastSeenAt.Time.After(idleCutoff) {
		return SessionAuthentication{}, fmt.Errorf("active-session loader returned an invalid row")
	}
	var role Role
	switch row.Role {
	case "member":
		role = RoleMember
	case "moderator":
		role = RoleModerator
	case "administrator":
		role = RoleAdministrator
	default:
		return SessionAuthentication{}, fmt.Errorf("active-session loader returned an invalid role")
	}
	if !row.LastSeenAt.Time.After(now.Add(-touchInterval)) {
		touched, touchErr := touch(ctx, db.TouchSessionParams{
			ObservedAt:  pgtype.Timestamptz{Time: now, Valid: true},
			SessionID:   row.SessionID,
			TouchBefore: pgtype.Timestamptz{Time: now.Add(-touchInterval), Valid: true},
		})
		if touchErr != nil {
			if contextError := ctx.Err(); contextError != nil {
				return SessionAuthentication{}, fmt.Errorf("touch active session: %w", contextError)
			}
			return SessionAuthentication{}, fmt.Errorf("touch active session failed")
		}
		if touched < 0 || touched > 1 {
			return SessionAuthentication{}, fmt.Errorf("touch active session returned an invalid row count")
		}
	}
	var mutedUntil *time.Time
	if row.MutedUntil.Valid && row.MutedUntil.Time.After(now) {
		muted := row.MutedUntil.Time.UTC().Truncate(time.Microsecond)
		mutedUntil = &muted
	}
	return SessionAuthentication{
		Access: AccessContext{
			Authenticated: true,
			UserID:        row.UserID,
			Role:          role,
			MutedUntil:    mutedUntil,
			ValidatedAt:   row.ValidatedAt.Time.UTC().Truncate(time.Microsecond),
		},
		RequiresRevalidation: !row.ValidatedAt.Time.After(now.Add(-revalidationInterval)),
	}, nil
}
