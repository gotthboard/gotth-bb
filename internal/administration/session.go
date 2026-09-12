package administration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const administrationSessionQueryLimit int32 = 51

type SessionSummary struct {
	ID                                           int64
	IssuedAt, LastSeenAt, ValidatedAt, ExpiresAt time.Time
}

type SessionPage struct {
	DisplayName string
	Revision    int64
	Sessions    []SessionSummary
	More        bool
}

type SessionMutationResult struct {
	TargetUserID int64
	SessionID    int64
	Revoked      int64
	AuditID      int64
}

type sessionAdministrationQuerier interface {
	ListSessionsForAdministration(context.Context, db.ListSessionsForAdministrationParams) ([]db.ListSessionsForAdministrationRow, error)
}

func ListSessions(ctx context.Context, querier sessionAdministrationQuerier, actor policy.AccessContext, observedAt time.Time, targetUserID int64) (SessionPage, error) {
	if ctx == nil || querier == nil {
		return SessionPage{}, fmt.Errorf("session administration loader is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return SessionPage{}, ErrAccountAdministrationDenied
	}
	if observedAt.IsZero() || targetUserID <= 0 {
		return SessionPage{}, ErrAccountAdministrationInput
	}
	if err := ctx.Err(); err != nil {
		return SessionPage{}, fmt.Errorf("load session administration: %w", err)
	}
	now := observedAt.UTC().Truncate(time.Microsecond)
	rows, err := querier.ListSessionsForAdministration(ctx, db.ListSessionsForAdministrationParams{
		ActorUserID: actor.UserID, ObservedAt: administrationTime(now),
		TargetUserID: targetUserID, PageLimit: administrationSessionQueryLimit,
	})
	if err != nil {
		return SessionPage{}, fmt.Errorf("%w: list sessions", ErrAccountAdministrationUnavailable)
	}
	if len(rows) == 0 || len(rows) > int(administrationSessionQueryLimit) {
		return SessionPage{}, malformedAdministrationRows("sessions")
	}
	first := rows[0]
	if !first.ActorPresent {
		return SessionPage{}, ErrAccountAdministrationDenied
	}
	if !first.SettingsPresent {
		return SessionPage{}, malformedAdministrationRows("session settings")
	}
	if !first.TargetPresent {
		if len(rows) != 1 || first.TargetUserID != 0 || first.DisplayName != "" || first.TargetRevision != 0 || first.SessionPresent {
			return SessionPage{}, malformedAdministrationRows("session target")
		}
		return SessionPage{}, ErrAccountAdministrationNotFound
	}
	if first.TargetUserID != targetUserID || !validAdministrationDisplayName(first.DisplayName) || first.TargetRevision <= 0 {
		return SessionPage{}, malformedAdministrationRows("session target")
	}
	page := SessionPage{
		DisplayName: first.DisplayName, Revision: first.TargetRevision,
		Sessions: make([]SessionSummary, 0, min(len(rows), administrationPageSize)),
		More:     len(rows) == int(administrationSessionQueryLimit),
	}
	previousID := int64(^uint64(0) >> 1)
	for index, row := range rows {
		if !row.ActorPresent || !row.TargetPresent || !row.SettingsPresent ||
			row.TargetUserID != targetUserID || row.DisplayName != page.DisplayName ||
			row.TargetRevision != page.Revision {
			return SessionPage{}, malformedAdministrationRows("sessions")
		}
		if !row.SessionPresent {
			if len(rows) != 1 || index != 0 || row.SessionID != 0 || row.IssuedAt.Valid ||
				row.LastSeenAt.Valid || row.ValidatedAt.Valid || row.ExpiresAt.Valid {
				return SessionPage{}, malformedAdministrationRows("sessions")
			}
			return page, nil
		}
		session := SessionSummary{ID: row.SessionID}
		if finiteAdministrationTime(row.IssuedAt) {
			session.IssuedAt = row.IssuedAt.Time.UTC().Truncate(time.Microsecond)
		}
		if finiteAdministrationTime(row.LastSeenAt) {
			session.LastSeenAt = row.LastSeenAt.Time.UTC().Truncate(time.Microsecond)
		}
		if finiteAdministrationTime(row.ValidatedAt) {
			session.ValidatedAt = row.ValidatedAt.Time.UTC().Truncate(time.Microsecond)
		}
		if finiteAdministrationTime(row.ExpiresAt) {
			session.ExpiresAt = row.ExpiresAt.Time.UTC().Truncate(time.Microsecond)
		}
		if session.ID <= 0 || session.ID >= previousID || session.IssuedAt.After(now) ||
			session.LastSeenAt.Before(session.IssuedAt) || session.LastSeenAt.After(now) ||
			session.ValidatedAt.Before(session.IssuedAt) || session.ValidatedAt.After(now) ||
			!session.ExpiresAt.After(now) || session.LastSeenAt.After(session.ExpiresAt) ||
			session.ValidatedAt.After(session.ExpiresAt) {
			return SessionPage{}, malformedAdministrationRows("sessions")
		}
		previousID = session.ID
		if index < administrationPageSize {
			page.Sessions = append(page.Sessions, session)
		}
	}
	return page, nil
}

func RevokeOneSession(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, targetUserID, sessionID int64, reason string, requestID pgtype.UUID) (SessionMutationResult, error) {
	if targetUserID <= 0 || sessionID <= 0 {
		return SessionMutationResult{}, ErrAccountAdministrationInput
	}
	return revokeSessions(ctx, beginner, clock, actor, targetUserID, sessionID, 0, reason, requestID)
}

func RevokeAllSessions(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, targetUserID, revision int64, reason string, requestID pgtype.UUID) (SessionMutationResult, error) {
	if targetUserID <= 0 || revision <= 0 {
		return SessionMutationResult{}, ErrAccountAdministrationInput
	}
	return revokeSessions(ctx, beginner, clock, actor, targetUserID, 0, revision, reason, requestID)
}

func revokeSessions(ctx context.Context, beginner accountTransactionBeginner, clock func() time.Time, actor policy.AccessContext, targetUserID, sessionID, revision int64, reason string, requestID pgtype.UUID) (SessionMutationResult, error) {
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, reason, requestID); err != nil {
		return SessionMutationResult{}, err
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return SessionMutationResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancel()
	result := SessionMutationResult{TargetUserID: targetUserID, SessionID: sessionID}
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		lockedActor, lockedTarget, err := lockAdministrationUsers(mutationContext, queries, actor.UserID, targetUserID)
		if err != nil {
			return err
		}
		if !validLockedAdministrator(lockedActor, actor, observedAt) {
			return ErrAccountAdministrationDenied
		}
		if !validLockedAdministrationUser(lockedTarget, targetUserID) {
			return fmt.Errorf("session target returned invalid state")
		}
		if sessionID == 0 {
			if lockedTarget.AdministrationRevision != revision {
				return ErrAccountAdministrationConflict
			}
			changed, changeErr := queries.RevokeAllSessionsForAdministrationAndAudit(mutationContext, db.RevokeAllSessionsForAdministrationAndAuditParams{
				ObservedAt: administrationTime(observedAt), TargetUserID: targetUserID,
				ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true},
				Reason:      pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
			})
			if errors.Is(changeErr, pgx.ErrNoRows) {
				return ErrAccountAdministrationConflict
			}
			if changeErr != nil {
				return mapAccountWriteError("revoke all sessions", changeErr)
			}
			if changed.RevokedCount <= 0 || changed.AuditID <= 0 {
				return fmt.Errorf("revoke all sessions returned invalid state")
			}
			result.Revoked, result.AuditID = changed.RevokedCount, changed.AuditID
			return nil
		}
		changed, changeErr := queries.RevokeOneSessionForAdministrationAndAudit(mutationContext, db.RevokeOneSessionForAdministrationAndAuditParams{
			ObservedAt: administrationTime(observedAt), SessionID: sessionID,
			TargetUserID: targetUserID, ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true},
			Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
		})
		if errors.Is(changeErr, pgx.ErrNoRows) {
			return ErrAccountAdministrationConflict
		}
		if changeErr != nil {
			return mapAccountWriteError("revoke session", changeErr)
		}
		if changed.RevokedCount != 1 || changed.AuditID <= 0 {
			return fmt.Errorf("revoke session returned invalid state")
		}
		result.Revoked, result.AuditID = changed.RevokedCount, changed.AuditID
		return nil
	})
	if err != nil {
		return SessionMutationResult{}, fmt.Errorf("session administration transaction: %w", err)
	}
	return result, nil
}
