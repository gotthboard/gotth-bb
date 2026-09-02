package moderation

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrUserModerationInput     = errors.New("invalid user moderation input")
	ErrUserModerationDenied    = errors.New("user moderation denied")
	ErrUserModerationConflict  = errors.New("user moderation state conflict")
	ErrAdministratorContinuity = errors.New("administrator continuity required")
)

type UserSuspensionResult struct {
	UserID    int64
	Suspended bool
	AuditID   int64
}

// ChangeUserSuspension indefinitely suspends or explicitly reinstates one
// local account and appends exactly one immutable audit in the same
// transaction. Active moderators may act on members; active administrators
// may act on any other account, but may not remove the final active
// administrator.
//
// Complexity: for g actor-group IDs, r bounded reason bytes, and delegated
// database work D, local time is O(g+r), Omega(1), and auxiliary space is
// tight Theta(1). Total time is O(g+r+D), Omega(1), without one tight bound
// because validation may return early and database work varies. One
// transaction performs at most five application statements plus begin/commit,
// with no retry or detached work.
func ChangeUserSuspension(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	targetUserID int64,
	suspend bool,
	reason string,
	requestID pgtype.UUID,
) (UserSuspensionResult, error) {
	if ctx == nil {
		return UserSuspensionResult{}, fmt.Errorf("user moderation context is required")
	}
	if beginner == nil {
		return UserSuspensionResult{}, fmt.Errorf("user moderation transaction beginner is required")
	}
	if clock == nil {
		return UserSuspensionResult{}, fmt.Errorf("user moderation clock is required")
	}
	if !actor.Valid() || !actor.Authenticated {
		return UserSuspensionResult{}, fmt.Errorf("user moderation actor is invalid")
	}
	if actor.Suspended || actor.MutedUntil != nil || actor.Role != policy.RoleModerator && actor.Role != policy.RoleAdministrator {
		return UserSuspensionResult{}, ErrUserModerationDenied
	}
	if targetUserID <= 0 {
		return UserSuspensionResult{}, fmt.Errorf("%w: target", ErrUserModerationInput)
	}
	if targetUserID == actor.UserID {
		return UserSuspensionResult{}, ErrUserModerationDenied
	}
	if !validSuspensionReason(reason) {
		return UserSuspensionResult{}, fmt.Errorf("%w: reason", ErrUserModerationInput)
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return UserSuspensionResult{}, fmt.Errorf("user moderation request ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return UserSuspensionResult{}, fmt.Errorf("moderate user: %w", err)
	}
	now := clock()
	if now.IsZero() {
		return UserSuspensionResult{}, fmt.Errorf("user moderation clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)

	result := UserSuspensionResult{}
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		governanceLocked, err := queries.LockGovernanceState(ctx)
		if err != nil {
			return fmt.Errorf("lock user moderation governance state: %w", err)
		}
		if !governanceLocked {
			return fmt.Errorf("user moderation governance singleton is missing")
		}
		firstUserID, secondUserID := actor.UserID, targetUserID
		if secondUserID < firstUserID {
			firstUserID, secondUserID = secondUserID, firstUserID
		}
		firstUser, err := queries.LockUserForSuspension(ctx, firstUserID)
		if err != nil {
			return fmt.Errorf("lock first user for moderation: %w", err)
		}
		secondUser, err := queries.LockUserForSuspension(ctx, secondUserID)
		if err != nil {
			return fmt.Errorf("lock second user for moderation: %w", err)
		}
		actorUser, target := firstUser, secondUser
		if firstUser.ID == targetUserID {
			actorUser, target = secondUser, firstUser
		}
		if !validSuspensionTarget(actorUser, actor.UserID) || !validSuspensionTarget(target, targetUserID) {
			return fmt.Errorf("user moderation lock returned an invalid result")
		}
		observedAt := pgtype.Timestamptz{Time: now, Valid: true}
		actorRole, roleValid := roleFromStorage(actorUser.Role)
		if !roleValid || actorRole != actor.Role || userSuspendedAt(actorUser, now) ||
			actorUser.MutedUntil.Valid && actorUser.MutedUntil.Time.After(now) {
			return ErrUserModerationDenied
		}
		if actor.Role == policy.RoleModerator && target.Role != "member" {
			return ErrUserModerationDenied
		}
		currentlySuspended := userSuspendedAt(target, now)
		if suspend == currentlySuspended {
			return ErrUserModerationConflict
		}
		if suspend && target.Role == "administrator" {
			administrators, countErr := queries.CountActiveAdministrators(ctx, observedAt)
			if countErr != nil {
				return fmt.Errorf("count active administrators for suspension: %w", countErr)
			}
			if administrators <= 1 {
				return ErrAdministratorContinuity
			}
		}
		actorID := pgtype.Int8{Int64: actor.UserID, Valid: true}
		updatedAt := now
		if target.UpdatedAt.Time.After(updatedAt) {
			updatedAt = target.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
		}
		updatedAtTime := pgtype.Timestamptz{Time: updatedAt, Valid: true}
		if suspend {
			suspendedAt := now
			if target.CreatedAt.Time.After(suspendedAt) {
				suspendedAt = target.CreatedAt.Time.UTC().Truncate(time.Microsecond)
			}
			suspendedAtTime := pgtype.Timestamptz{Time: suspendedAt, Valid: true}
			changed, changeErr := queries.SuspendUserAndAudit(ctx, db.SuspendUserAndAuditParams{
				ObservedAt: observedAt, SuspendedAt: suspendedAtTime, UpdatedAt: updatedAtTime,
				Reason: pgtype.Text{String: reason, Valid: true}, UserID: targetUserID,
				ActorUserID: actorID, PreviousSuspendedAt: target.SuspendedAt,
				PreviousSuspendedUntil: target.SuspendedUntil, PreviousSuspensionReason: target.SuspensionReason,
				RequestID: requestID,
			})
			if changeErr != nil {
				return fmt.Errorf("suspend user and audit: %w", changeErr)
			}
			if changed.UserID != targetUserID || !changed.SuspendedAt.Valid || changed.SuspendedAt.InfinityModifier != pgtype.Finite ||
				!changed.SuspendedAt.Time.Equal(suspendedAt) || changed.SuspendedUntil.Valid || !changed.SuspensionReason.Valid ||
				changed.SuspensionReason.String != reason || !changed.UpdatedAt.Valid || changed.UpdatedAt.InfinityModifier != pgtype.Finite ||
				!changed.UpdatedAt.Time.Equal(updatedAt) || changed.AuditID <= 0 {
				return fmt.Errorf("user suspension returned an invalid result")
			}
			result = UserSuspensionResult{UserID: changed.UserID, Suspended: true, AuditID: changed.AuditID}
			return nil
		}
		changed, changeErr := queries.ReinstateUserAndAudit(ctx, db.ReinstateUserAndAuditParams{
			ObservedAt: observedAt, UpdatedAt: updatedAtTime, UserID: targetUserID, ActorUserID: actorID,
			Reason: pgtype.Text{String: reason, Valid: true}, PreviousSuspendedAt: target.SuspendedAt,
			PreviousSuspendedUntil: target.SuspendedUntil, PreviousSuspensionReason: target.SuspensionReason.String,
			RequestID: requestID,
		})
		if changeErr != nil {
			return fmt.Errorf("reinstate user and audit: %w", changeErr)
		}
		if changed.UserID != targetUserID || changed.SuspendedAt.Valid || changed.SuspendedUntil.Valid || changed.SuspensionReason.Valid ||
			!changed.UpdatedAt.Valid || changed.UpdatedAt.InfinityModifier != pgtype.Finite || !changed.UpdatedAt.Time.Equal(updatedAt) || changed.AuditID <= 0 {
			return fmt.Errorf("user reinstatement returned an invalid result")
		}
		result = UserSuspensionResult{UserID: changed.UserID, AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return UserSuspensionResult{}, fmt.Errorf("user moderation transaction: %w", err)
	}
	return result, nil
}

// validSuspensionReason accepts canonical nonblank single-line UTF-8 prose
// through both the users table's 500-character limit and the audit table's
// 2,000-byte service bound.
//
// Complexity: for r <= 2,000 bytes, time is O(r), Omega(1), and tight Theta(r)
// for valid input. Auxiliary space is tight Theta(1).
func validSuspensionReason(reason string) bool {
	return len(reason) <= 2_000 && validReason(reason) && utf8.RuneCountInString(reason) <= 500
}

// validSuspensionTarget closes the locked database row before policy use.
//
// Complexity: for r <= 2,000 suspension-reason bytes, time is O(r), Omega(1),
// and auxiliary space is tight Theta(1).
func validSuspensionTarget(target db.LockUserForSuspensionRow, expectedID int64) bool {
	if target.ID != expectedID || target.ID <= 0 || !validUserRole(target.Role) ||
		!finiteTimestamp(target.CreatedAt) || !finiteTimestamp(target.UpdatedAt) ||
		target.UpdatedAt.Time.Before(target.CreatedAt.Time) ||
		target.MutedUntil.Valid && (!finiteTimestamp(target.MutedUntil) || !target.MutedUntil.Time.After(target.CreatedAt.Time)) {
		return false
	}
	if !target.SuspendedAt.Valid {
		return !target.SuspendedUntil.Valid && !target.SuspensionReason.Valid
	}
	return finiteTimestamp(target.SuspendedAt) && !target.SuspendedAt.Time.Before(target.CreatedAt.Time) &&
		target.SuspensionReason.Valid && validSuspensionReason(target.SuspensionReason.String) &&
		(!target.SuspendedUntil.Valid || finiteTimestamp(target.SuspendedUntil) && target.SuspendedUntil.Time.After(target.SuspendedAt.Time))
}

// userSuspendedAt evaluates the schema's effective local suspension state at
// one transaction-owned instant.
//
// Complexity: time and auxiliary space are tight Theta(1).
func userSuspendedAt(target db.LockUserForSuspensionRow, at time.Time) bool {
	return target.SuspendedAt.Valid && !target.SuspendedAt.Time.After(at) &&
		(!target.SuspendedUntil.Valid || target.SuspendedUntil.Time.After(at))
}

// validUserRole closes the persisted role before authorization comparison.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validUserRole(role string) bool {
	_, valid := roleFromStorage(role)
	return valid
}

// roleFromStorage maps the closed persisted role to the policy enum.
//
// Complexity: time and auxiliary space are tight Theta(1).
func roleFromStorage(role string) (policy.Role, bool) {
	switch role {
	case "member":
		return policy.RoleMember, true
	case "moderator":
		return policy.RoleModerator, true
	case "administrator":
		return policy.RoleAdministrator, true
	default:
		return 0, false
	}
}

// finiteTimestamp rejects null, infinite, and zero database timestamps.
//
// Complexity: time and auxiliary space are tight Theta(1).
func finiteTimestamp(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite && !value.Time.IsZero()
}
