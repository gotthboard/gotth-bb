package governance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type IdentityRebindResult struct {
	UserID                 int64
	AuditID                int64
	RevokedSessions        int64
	DiscardedLoginAttempts int64
}

var ErrExternalIdentityRebindDenied = errors.New("external identity rebind target is absent or replacement identity is already bound")

// RebindExternalIdentity atomically moves one existing forum user to an exact
// replacement issuer/subject, revokes every session, discards every pending
// login attempt, and records one operator audit. Both identity advisory locks
// are acquired in lexical order so OIDC login and another operator cannot race
// the cutover or deadlock each other. The caller must stop the application
// first; the locks are the final database-side guard, not an online-migration
// promise.
//
// Complexity: for bounded identity and operator strings, local time and space
// are tight Theta(1). Database work is O(S+A), where S is the target user's
// session count and A is the deployment-wide pending login-attempt count. Lock
// wait time is caller-context-owned and unbounded; no retry occurs.
func RebindExternalIdentity(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	oldIssuer string,
	oldSubject string,
	newIssuer string,
	newSubject string,
	operatorIdentifier string,
	requestID pgtype.UUID,
) (IdentityRebindResult, error) {
	if ctx == nil {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind context is required")
	}
	if beginner == nil {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind transaction beginner is required")
	}
	if clock == nil {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind clock is required")
	}
	if !validBootstrapText(oldIssuer, 2048) || !validBootstrapText(newIssuer, 2048) ||
		!validBootstrapText(oldSubject, 512) || !validBootstrapText(newSubject, 512) {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind identity is invalid")
	}
	if oldIssuer == newIssuer && oldSubject == newSubject {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind replacement is unchanged")
	}
	if !validBootstrapText(operatorIdentifier, 200) {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind operator identifier is invalid")
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind request ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return IdentityRebindResult{}, fmt.Errorf("rebind external identity: %w", err)
	}
	now := clock()
	if now.IsZero() {
		return IdentityRebindResult{}, fmt.Errorf("external identity rebind clock returned a zero time")
	}
	atTime := pgtype.Timestamptz{Time: now.UTC().Truncate(time.Microsecond), Valid: true}
	firstIssuer, firstSubject := oldIssuer, oldSubject
	secondIssuer, secondSubject := newIssuer, newSubject
	if secondIssuer < firstIssuer || secondIssuer == firstIssuer && secondSubject < firstSubject {
		firstIssuer, secondIssuer = secondIssuer, firstIssuer
		firstSubject, secondSubject = secondSubject, firstSubject
	}
	result := IdentityRebindResult{}
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		for _, identity := range [][2]string{{firstIssuer, firstSubject}, {secondIssuer, secondSubject}} {
			locked, err := queries.LockExternalIdentity(ctx, db.LockExternalIdentityParams{
				Issuer: identity[0], Subject: identity[1],
			})
			if err != nil {
				return fmt.Errorf("lock external identity: %w", err)
			}
			if !locked {
				return fmt.Errorf("external identity lock returned false")
			}
		}
		rebound, err := queries.RebindExternalIdentityAndAudit(ctx, db.RebindExternalIdentityAndAuditParams{
			OldIssuer: oldIssuer, OldSubject: oldSubject,
			NewIssuer: newIssuer, NewSubject: newSubject,
			AtTime: atTime, OperatorIdentifier: pgtype.Text{String: operatorIdentifier, Valid: true},
			RequestID: requestID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrExternalIdentityRebindDenied
		}
		if err != nil {
			return fmt.Errorf("write external identity rebind and audit: %w", err)
		}
		if rebound.UserID <= 0 || rebound.AuditID <= 0 || rebound.RevokedSessions < 0 || rebound.DiscardedLoginAttempts < 0 {
			return fmt.Errorf("external identity rebind returned an invalid result")
		}
		result = IdentityRebindResult{
			UserID: rebound.UserID, AuditID: rebound.AuditID,
			RevokedSessions:        rebound.RevokedSessions,
			DiscardedLoginAttempts: rebound.DiscardedLoginAttempts,
		}
		return nil
	})
	if err != nil {
		return IdentityRebindResult{}, fmt.Errorf("rebind external identity: %w", err)
	}
	return result, nil
}
