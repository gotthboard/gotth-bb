package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type createdInitialSession struct {
	token     string
	userID    int64
	sessionID int64
	expiresAt time.Time
}

// createInitialSession generates one opaque browser credential, serializes the
// verified external identity, creates or profile-refreshes its local user, and
// inserts the session in one transaction. Local authorization fields are never
// supplied to SQL, and no token is returned unless commit succeeds.
//
// Complexity: local work and credential size are tight Theta(1). The transaction
// performs one advisory lock, one indexed identity read, either two inserts or
// two profile/verification writes, one session insert, and one commit. Database
// wait time is unbounded by this function and owned by the caller context.
func createInitialSession(
	ctx context.Context,
	beginner transactionBeginner,
	entropy io.Reader,
	clock func() time.Time,
	maximumAge time.Duration,
	claims verifiedIdentityClaims,
) (createdInitialSession, error) {
	if ctx == nil {
		return createdInitialSession{}, fmt.Errorf("initial session context is required")
	}
	if beginner == nil {
		return createdInitialSession{}, fmt.Errorf("initial session transaction beginner is required")
	}
	if entropy == nil {
		return createdInitialSession{}, fmt.Errorf("initial session entropy is required")
	}
	if clock == nil {
		return createdInitialSession{}, fmt.Errorf("initial session clock is required")
	}
	if maximumAge <= 0 {
		return createdInitialSession{}, fmt.Errorf("initial session maximum age must be positive")
	}
	if claims.issuer == "" || claims.subject == "" || claims.displayName == "" {
		return createdInitialSession{}, fmt.Errorf("verified identity claims are incomplete")
	}
	if err := ctx.Err(); err != nil {
		return createdInitialSession{}, fmt.Errorf("create initial session: %w", err)
	}
	now := clock()
	if now.IsZero() {
		return createdInitialSession{}, fmt.Errorf("initial session clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	expiresAt := now.Add(maximumAge).UTC().Truncate(time.Microsecond)
	if !expiresAt.After(now) {
		return createdInitialSession{}, fmt.Errorf("initial session maximum age is below database precision")
	}
	material, err := generateSessionMaterial(entropy)
	if err != nil {
		return createdInitialSession{}, err
	}
	toNullableText := func(value *string) pgtype.Text {
		if value == nil {
			return pgtype.Text{}
		}
		return pgtype.Text{String: *value, Valid: true}
	}
	loginAt := pgtype.Timestamptz{Time: now, Valid: true}
	created := createdInitialSession{token: material.token, expiresAt: expiresAt}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		lockParams := db.LockExternalIdentityParams{Issuer: claims.issuer, Subject: claims.subject}
		locked, err := queries.LockExternalIdentity(ctx, lockParams)
		if err != nil {
			return fmt.Errorf("lock external identity: %w", err)
		}
		if !locked {
			return fmt.Errorf("external identity lock returned false")
		}
		user, err := queries.GetUserByExternalIdentity(ctx, db.GetUserByExternalIdentityParams(lockParams))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			user, err = queries.InsertUser(ctx, db.InsertUserParams{
				DisplayName: claims.displayName,
				Email:       toNullableText(claims.email),
				AvatarUrl:   toNullableText(claims.avatarURL),
				LoginAt:     loginAt,
			})
			if err != nil {
				return fmt.Errorf("insert local user: %w", err)
			}
			if err := queries.InsertExternalIdentity(ctx, db.InsertExternalIdentityParams{
				UserID: user.ID, Issuer: claims.issuer, Subject: claims.subject, VerifiedAt: loginAt,
			}); err != nil {
				return fmt.Errorf("insert external identity: %w", err)
			}
		case err != nil:
			return fmt.Errorf("load external identity: %w", err)
		default:
			user, err = queries.UpdateUserFromOIDC(ctx, db.UpdateUserFromOIDCParams{
				DisplayName: claims.displayName,
				Email:       toNullableText(claims.email),
				AvatarUrl:   toNullableText(claims.avatarURL),
				LoginAt:     loginAt,
				UserID:      user.ID,
			})
			if err != nil {
				return fmt.Errorf("refresh local user profile: %w", err)
			}
			if err := queries.UpdateExternalIdentityVerification(ctx, db.UpdateExternalIdentityVerificationParams{
				VerifiedAt: loginAt, UserID: user.ID,
			}); err != nil {
				return fmt.Errorf("refresh external identity verification: %w", err)
			}
		}
		session, err := queries.InsertSession(ctx, db.InsertSessionParams{
			TokenHash: material.tokenHash[:],
			UserID:    user.ID,
			IssuedAt:  loginAt,
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("insert initial session: %w", err)
		}
		created.userID = user.ID
		created.sessionID = session.ID
		return nil
	})
	if err != nil {
		return createdInitialSession{}, fmt.Errorf("create initial identity and session: %w", err)
	}
	return created, nil
}
