package registration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/moderation"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const manualReconciliationBackoff = time.Minute

type IdentityReconciliationResult struct {
	UserID, Revision, AuditID int64
	SyncState                 string
}

func ReconcileIdentity(ctx context.Context, beginner transactionBeginner, remote ControlGateway, clock func() time.Time, actor policy.AccessContext, targetUserID int64, reason string, requestID pgtype.UUID) (IdentityReconciliationResult, error) {
	if ctx == nil || beginner == nil || remote == nil || clock == nil || !policy.CanAdminister(actor) {
		return IdentityReconciliationResult{}, ErrDenied
	}
	if targetUserID <= 0 || targetUserID == actor.UserID || !validReason(reason) || !inputRequestID(requestID) {
		return IdentityReconciliationResult{}, ErrInput
	}
	now, err := observedAt(clock)
	if err != nil {
		return IdentityReconciliationResult{}, err
	}
	var begun db.BeginManualIdentityReconciliationRow
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, now); err != nil {
			return err
		}
		var beginErr error
		begun, beginErr = queries.BeginManualIdentityReconciliation(txctx, db.BeginManualIdentityReconciliationParams{
			UserID: targetUserID, ObservedAt: finiteTime(now), ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true},
			Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
		})
		if errors.Is(beginErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if beginErr != nil || begun.AdministrationRevision <= 1 || begun.AuditID <= 0 || (begun.AuthentikSyncState != "removal_required" && begun.AuthentikSyncState != "grant_required") {
			return fmt.Errorf("%w: begin manual identity reconciliation", ErrUnavailable)
		}
		return nil
	})
	if err != nil {
		return IdentityReconciliationResult{}, err
	}
	if _, parseErr := parseCanonicalUUID(begun.Subject); parseErr != nil {
		return IdentityReconciliationResult{}, fmt.Errorf("%w: malformed identity subject", ErrUnavailable)
	}
	var remoteErr error
	if begun.AuthentikSyncState == "removal_required" {
		remoteErr = applyIdentityRemoval(ctx, remote, begun.Subject)
	} else {
		remoteErr = applyIdentityGrant(ctx, remote, begun.Subject)
	}
	completedAt, clockErr := observedAt(clock)
	if clockErr != nil {
		return IdentityReconciliationResult{}, clockErr
	}
	if remoteErr != nil {
		failure := remoteFailureClass(remoteErr)
		if recordErr := recordManualIdentityFailure(ctx, beginner, actor.UserID, targetUserID, begun.AuthentikSyncState, begun.AdministrationRevision, reason, requestID, completedAt, failure); recordErr != nil {
			return IdentityReconciliationResult{}, recordErr
		}
		return IdentityReconciliationResult{}, fmt.Errorf("%w: %s", ErrRemote, failure)
	}
	result := IdentityReconciliationResult{UserID: targetUserID}
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, completedAt); err != nil {
			return err
		}
		if begun.AuthentikSyncState == "removal_required" {
			row, completeErr := queries.CompleteIdentityRemoval(txctx, db.CompleteIdentityRemovalParams{ObservedAt: finiteTime(completedAt), UserID: targetUserID, ExpectedRevision: begun.AdministrationRevision, ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID})
			if errors.Is(completeErr, pgx.ErrNoRows) {
				return ErrConflict
			}
			if completeErr != nil || row.AdministrationRevision != begun.AdministrationRevision+1 || row.AuditID <= 0 {
				return fmt.Errorf("%w: complete manual identity removal", ErrUnavailable)
			}
			result.Revision, result.AuditID, result.SyncState = row.AdministrationRevision, row.AuditID, "suspended"
			return nil
		}
		row, completeErr := queries.CompleteIdentityReinstatement(txctx, db.CompleteIdentityReinstatementParams{ActorUserID: actor.UserID, ActorRole: "administrator", ObservedAt: finiteTime(completedAt), UserID: targetUserID, ExpectedRevision: begun.AdministrationRevision, Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if completeErr != nil || row.UserID != targetUserID || row.AdministrationRevision != begun.AdministrationRevision+1 || row.ReconciliationAuditID <= 0 || row.ReinstatementAuditID <= 0 {
			return fmt.Errorf("%w: complete manual identity grant", ErrUnavailable)
		}
		result.Revision, result.AuditID, result.SyncState = row.AdministrationRevision, row.ReconciliationAuditID, "accepted"
		return nil
	})
	if err != nil {
		return IdentityReconciliationResult{}, fmt.Errorf("complete manual identity reconciliation: %w", err)
	}
	return result, nil
}

// ChangeUserSuspension preserves local denial before any remote operation.
// Reinstatement does the inverse: it grants and verifies Authentik first and
// clears the local suspension only in the final audited transaction.
func ChangeUserSuspension(ctx context.Context, beginner transactionBeginner, remote ControlGateway, clock func() time.Time, actor policy.AccessContext, targetUserID int64, suspend bool, reason string, requestID pgtype.UUID) (moderation.UserSuspensionResult, error) {
	if ctx == nil || beginner == nil || remote == nil || clock == nil || !validModerationActor(actor) {
		return moderation.UserSuspensionResult{}, moderation.ErrUserModerationDenied
	}
	if targetUserID <= 0 || targetUserID == actor.UserID || !validReason(reason) || !inputRequestID(requestID) {
		return moderation.UserSuspensionResult{}, moderation.ErrUserModerationInput
	}
	if suspend {
		return suspendUserWithControl(ctx, beginner, remote, clock, actor, targetUserID, reason, requestID)
	}
	return reinstateUserWithControl(ctx, beginner, remote, clock, actor, targetUserID, reason, requestID)
}

func suspendUserWithControl(ctx context.Context, beginner transactionBeginner, remote ControlGateway, clock func() time.Time, actor policy.AccessContext, targetUserID int64, reason string, requestID pgtype.UUID) (moderation.UserSuspensionResult, error) {
	local, err := moderation.ChangeUserSuspension(ctx, beginner, clock, actor, targetUserID, true, reason, requestID)
	if err != nil {
		return moderation.UserSuspensionResult{}, err
	}
	at, err := observedAt(clock)
	if err != nil {
		return moderation.UserSuspensionResult{}, err
	}
	var target db.LoadSuspendedIdentityTargetRow
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		var loadErr error
		target, loadErr = queries.LoadSuspendedIdentityTarget(txctx, db.LoadSuspendedIdentityTargetParams{UserID: targetUserID, ExpectedRevision: local.Revision, ObservedAt: finiteTime(at)})
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if loadErr != nil || target.AuthentikSyncState != "removal_required" || target.AdministrationRevision != local.Revision {
			return fmt.Errorf("%w: load suspended identity", ErrUnavailable)
		}
		return nil
	})
	if err != nil {
		return moderation.UserSuspensionResult{}, fmt.Errorf("load suspended identity: %w", err)
	}
	if _, parseErr := parseCanonicalUUID(target.Subject); parseErr != nil {
		return moderation.UserSuspensionResult{}, fmt.Errorf("%w: malformed identity subject", ErrUnavailable)
	}
	remoteErr := applyIdentityRemoval(ctx, remote, target.Subject)
	completedAt, clockErr := observedAt(clock)
	if clockErr != nil {
		return moderation.UserSuspensionResult{}, clockErr
	}
	if remoteErr != nil {
		if recordErr := recordManualIdentityFailure(ctx, beginner, actor.UserID, targetUserID, "removal_required", target.AdministrationRevision, reason, requestID, completedAt, remoteFailureClass(remoteErr)); recordErr != nil {
			return moderation.UserSuspensionResult{}, fmt.Errorf("%w: record identity removal failure", ErrUnavailable)
		}
		return moderation.UserSuspensionResult{}, fmt.Errorf("%w: %s", ErrRemote, remoteFailureClass(remoteErr))
	}
	var completed db.CompleteIdentityRemovalRow
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		var completeErr error
		completed, completeErr = queries.CompleteIdentityRemoval(txctx, db.CompleteIdentityRemovalParams{
			ObservedAt: finiteTime(completedAt), UserID: targetUserID, ExpectedRevision: target.AdministrationRevision,
			ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if completeErr != nil || completed.AdministrationRevision != target.AdministrationRevision+1 || completed.AuditID <= 0 {
			return fmt.Errorf("%w: complete identity removal", ErrUnavailable)
		}
		return nil
	})
	if err != nil {
		return moderation.UserSuspensionResult{}, fmt.Errorf("complete identity removal: %w", err)
	}
	local.Revision = completed.AdministrationRevision
	return local, nil
}

func reinstateUserWithControl(ctx context.Context, beginner transactionBeginner, remote ControlGateway, clock func() time.Time, actor policy.AccessContext, targetUserID int64, reason string, requestID pgtype.UUID) (moderation.UserSuspensionResult, error) {
	at, err := observedAt(clock)
	if err != nil {
		return moderation.UserSuspensionResult{}, err
	}
	role, ok := moderationRole(actor.Role)
	if !ok {
		return moderation.UserSuspensionResult{}, moderation.ErrUserModerationDenied
	}
	var begun db.BeginIdentityReinstatementRow
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		var beginErr error
		begun, beginErr = queries.BeginIdentityReinstatement(txctx, db.BeginIdentityReinstatementParams{
			ActorUserID: actor.UserID, ActorRole: role, ObservedAt: finiteTime(at), UserID: targetUserID,
			Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
		})
		if errors.Is(beginErr, pgx.ErrNoRows) {
			return moderation.ErrUserModerationConflict
		}
		if beginErr != nil || begun.AdministrationRevision <= 0 || begun.AuditID <= 0 {
			return fmt.Errorf("%w: begin identity reinstatement", ErrUnavailable)
		}
		return nil
	})
	if err != nil {
		return moderation.UserSuspensionResult{}, fmt.Errorf("begin identity reinstatement: %w", err)
	}
	if _, parseErr := parseCanonicalUUID(begun.Subject); parseErr != nil {
		return moderation.UserSuspensionResult{}, fmt.Errorf("%w: malformed identity subject", ErrUnavailable)
	}
	remoteErr := applyIdentityGrant(ctx, remote, begun.Subject)
	completedAt, clockErr := observedAt(clock)
	if clockErr != nil {
		return moderation.UserSuspensionResult{}, clockErr
	}
	if remoteErr != nil {
		if recordErr := recordManualIdentityFailure(ctx, beginner, actor.UserID, targetUserID, "grant_required", begun.AdministrationRevision, reason, requestID, completedAt, remoteFailureClass(remoteErr)); recordErr != nil {
			return moderation.UserSuspensionResult{}, fmt.Errorf("%w: record identity grant failure", ErrUnavailable)
		}
		return moderation.UserSuspensionResult{}, fmt.Errorf("%w: %s", ErrRemote, remoteFailureClass(remoteErr))
	}
	var completed db.CompleteIdentityReinstatementRow
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		var completeErr error
		completed, completeErr = queries.CompleteIdentityReinstatement(txctx, db.CompleteIdentityReinstatementParams{
			ActorUserID: actor.UserID, ActorRole: role, ObservedAt: finiteTime(completedAt), UserID: targetUserID,
			ExpectedRevision: begun.AdministrationRevision, Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return moderation.ErrUserModerationConflict
		}
		if completeErr != nil || completed.UserID != targetUserID || completed.AdministrationRevision != begun.AdministrationRevision+1 || completed.ReconciliationAuditID <= 0 || completed.ReinstatementAuditID <= 0 {
			return fmt.Errorf("%w: complete identity reinstatement", ErrUnavailable)
		}
		return nil
	})
	if err != nil {
		return moderation.UserSuspensionResult{}, fmt.Errorf("complete identity reinstatement: %w", err)
	}
	return moderation.UserSuspensionResult{UserID: targetUserID, Revision: completed.AdministrationRevision, AuditID: completed.ReinstatementAuditID}, nil
}

func applyIdentityRemoval(ctx context.Context, remote ControlGateway, subject string) error {
	if err := remote.RemoveUser(ctx, "accepted", subject); err != nil {
		return err
	}
	if err := remote.RemoveUser(ctx, "pending", subject); err != nil {
		return err
	}
	if err := remote.AddUser(ctx, "suspended", subject); err != nil {
		return err
	}
	state, err := remote.User(ctx, subject)
	if err != nil {
		return err
	}
	if state.UUID != subject || !state.Active || state.Accepted || state.Pending || !state.Suspended {
		return authentikgateway.ErrRemoteConflict
	}
	return nil
}

func applyIdentityGrant(ctx context.Context, remote ControlGateway, subject string) error {
	if err := remote.RemoveUser(ctx, "suspended", subject); err != nil {
		return err
	}
	if err := remote.RemoveUser(ctx, "pending", subject); err != nil {
		return err
	}
	if err := remote.AddUser(ctx, "accepted", subject); err != nil {
		return err
	}
	state, err := remote.User(ctx, subject)
	if err != nil {
		return err
	}
	if state.UUID != subject || !state.Active || !state.Accepted || state.Pending || state.Suspended {
		return authentikgateway.ErrRemoteConflict
	}
	return nil
}

func recordManualIdentityFailure(ctx context.Context, beginner transactionBeginner, actorID, targetID int64, state string, revision int64, reason string, requestID pgtype.UUID, at time.Time, failure string) error {
	return inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		row, err := queries.RecordIdentityReconciliationFailure(txctx, db.RecordIdentityReconciliationFailureParams{
			ObservedAt: finiteTime(at), NextAttemptAt: finiteTime(at.Add(manualReconciliationBackoff)), FailureClass: pgtype.Text{String: failure, Valid: true},
			UserID: targetID, RequiredState: state, ExpectedRevision: revision,
			ActorUserID: pgtype.Int8{Int64: actorID, Valid: true}, Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil || row.AdministrationRevision != revision+1 || row.AuditID <= 0 {
			return fmt.Errorf("%w: record identity reconciliation failure", ErrUnavailable)
		}
		return nil
	})
}

func validModerationActor(actor policy.AccessContext) bool {
	return actor.Valid() && actor.Authenticated && !actor.Suspended && actor.MutedUntil == nil && (actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator)
}

func moderationRole(role policy.Role) (string, bool) {
	switch role {
	case policy.RoleModerator:
		return "moderator", true
	case policy.RoleAdministrator:
		return "administrator", true
	default:
		return "", false
	}
}

func inputRequestID(requestID pgtype.UUID) bool {
	return requestID.Valid && requestID.Bytes != ([16]byte{})
}
