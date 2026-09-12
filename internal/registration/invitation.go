package registration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type InvitationMailer interface {
	SendInvitation(context.Context, string, string, string, time.Time) (string, error)
}

type InvitationInput struct {
	Email, DisplayName, Reason string
	ExpiresAt                  time.Time
	Deliver                    bool
	RequestID                  pgtype.UUID
}

type InvitationResult struct {
	Status, Delivery, TokenUUID string
	Revision, AuditID           int64
	Completed                   bool
}

type InvitationRevocationInput struct {
	Handle, Reason string
	RequestID      pgtype.UUID
}

type InvitationRevocationResult struct {
	Status, Result    string
	Revision, AuditID int64
	Completed         bool
}

type InvitationSummary struct {
	Name, Status, Delivery, Failure, Handle string
	ExpiresAt, CreatedAt                    time.Time
	Revision                                int64
}

type InvitationPage struct {
	Invitations []InvitationSummary
	More        bool
}

type invitationListQuerier interface {
	ListRegistrationInvitationsForAdministration(context.Context, db.ListRegistrationInvitationsForAdministrationParams) ([]db.ListRegistrationInvitationsForAdministrationRow, error)
}

const (
	invitationHandleLifetime        = 5 * time.Minute
	invitationHandleRawBytes        = 65
	maximumInvitationHandleRevision = int64(9223372036854775805)
)

func ListInvitations(ctx context.Context, querier invitationListQuerier, remote ControlGateway, actor policy.AccessContext, now time.Time, key [32]byte) (InvitationPage, error) {
	if ctx == nil || querier == nil || remote == nil || key == ([32]byte{}) || !policy.CanAdminister(actor) {
		return InvitationPage{}, ErrDenied
	}
	if err := ctx.Err(); err != nil || now.IsZero() {
		return InvitationPage{}, ErrInput
	}
	rows, err := querier.ListRegistrationInvitationsForAdministration(ctx, db.ListRegistrationInvitationsForAdministrationParams{ActorUserID: actor.UserID, ObservedAt: finiteTime(now.UTC().Truncate(time.Microsecond))})
	if err != nil || len(rows) == 0 {
		return InvitationPage{}, fmt.Errorf("%w: list invitations", ErrUnavailable)
	}
	if !rows[0].ActorPresent {
		return InvitationPage{}, ErrDenied
	}
	if len(rows) > 51 {
		return InvitationPage{}, fmt.Errorf("%w: oversized invitation page", ErrUnavailable)
	}
	remoteInvitations, remoteMore, remoteErr := remote.Invitations(ctx)
	if remoteErr != nil || len(remoteInvitations) > 51 {
		return InvitationPage{}, fmt.Errorf("%w: reconcile invitations", ErrRemote)
	}
	remoteByName := make(map[string]authentikgateway.Invitation, len(remoteInvitations))
	for _, invitation := range remoteInvitations {
		expires, parseErr := time.Parse(time.RFC3339, invitation.Expires)
		_, uuidErr := parseCanonicalUUID(invitation.UUID)
		if parseErr != nil || uuidErr != nil || !invitation.SingleUse || invitation.Name == "" || expires.IsZero() {
			return InvitationPage{}, fmt.Errorf("%w: malformed remote invitation", ErrRemote)
		}
		if _, duplicate := remoteByName[invitation.Name]; duplicate {
			return InvitationPage{}, fmt.Errorf("%w: duplicate remote invitation", ErrRemote)
		}
		remoteByName[invitation.Name] = invitation
	}
	page := InvitationPage{Invitations: make([]InvitationSummary, 0, min(len(rows), 50)), More: len(rows) == 51 || remoteMore}
	for index, row := range rows {
		if !row.ActorPresent {
			return InvitationPage{}, ErrDenied
		}
		if !row.InvitationPresent {
			if len(rows) != 1 || index != 0 || row.IdempotencyKey.Valid || row.InvitationName != "" || row.TransitionState != "" || row.DeliveryState != "" || row.ExpiresAt.Valid || row.CreatedAt.Valid || row.AdministrationRevision != 0 || row.FailureClass.Valid {
				return InvitationPage{}, fmt.Errorf("%w: malformed invitation page", ErrUnavailable)
			}
			return page, nil
		}
		if index == 50 {
			continue
		}
		summary := InvitationSummary{Name: row.InvitationName, Status: row.TransitionState, Delivery: row.DeliveryState, Revision: row.AdministrationRevision}
		if row.ExpiresAt.Valid {
			summary.ExpiresAt = row.ExpiresAt.Time.UTC().Truncate(time.Microsecond)
		}
		if row.CreatedAt.Valid {
			summary.CreatedAt = row.CreatedAt.Time.UTC().Truncate(time.Microsecond)
		}
		if row.FailureClass.Valid {
			summary.Failure = row.FailureClass.String
		}
		if !validInvitationSummary(summary) || !row.IdempotencyKey.Valid || row.IdempotencyKey.Bytes == ([16]byte{}) {
			return InvitationPage{}, fmt.Errorf("%w: malformed invitation page", ErrUnavailable)
		}
		if remoteInvitation, present := remoteByName[summary.Name]; present {
			if summary.Status == "revoked" || summary.Status == "absent" {
				return InvitationPage{}, fmt.Errorf("%w: terminal invitation remains remote", ErrRemote)
			}
			remoteExpiry, _ := time.Parse(time.RFC3339, remoteInvitation.Expires)
			if !remoteExpiry.Equal(summary.ExpiresAt) {
				return InvitationPage{}, fmt.Errorf("%w: invitation identity mismatch", ErrRemote)
			}
			delete(remoteByName, summary.Name)
		}
		if summary.Status != "revoked" && summary.Status != "absent" {
			handle, handleErr := issueInvitationHandle(now, key, row.IdempotencyKey, summary.Revision)
			if handleErr != nil {
				return InvitationPage{}, fmt.Errorf("%w: issue invitation handle", ErrUnavailable)
			}
			summary.Handle = handle
		}
		page.Invitations = append(page.Invitations, summary)
	}
	if !page.More && len(remoteByName) != 0 {
		return InvitationPage{}, fmt.Errorf("%w: untracked remote invitation", ErrRemote)
	}
	return page, nil
}

func validInvitationSummary(summary InvitationSummary) bool {
	status := summary.Status == "creating" || summary.Status == "active" || summary.Status == "revoke_required" || summary.Status == "revoked" || summary.Status == "absent" || summary.Status == "unknown"
	delivery := summary.Delivery == "not_requested" || summary.Delivery == "queued" || summary.Delivery == "failed" || summary.Delivery == "unknown"
	failure := summary.Failure == "" || validIntakeText(summary.Failure, 1, 128, 128)
	return len(summary.Name) >= 1 && len(summary.Name) <= 200 && validIntakeText(summary.Name, 1, 200, 200) && status && delivery && summary.Revision > 0 && !summary.ExpiresAt.IsZero() && !summary.CreatedAt.IsZero() && summary.ExpiresAt.After(summary.CreatedAt) && failure
}

func CreateInvitation(ctx context.Context, beginner transactionBeginner, remote ControlGateway, mailer InvitationMailer, clock func() time.Time, actor policy.AccessContext, input InvitationInput, key [32]byte, flowIdentity string) (InvitationResult, error) {
	if ctx == nil || beginner == nil || remote == nil || clock == nil || !policy.CanAdminister(actor) {
		return InvitationResult{}, ErrDenied
	}
	now, err := observedAt(clock)
	if err != nil {
		return InvitationResult{}, err
	}
	expires := input.ExpiresAt.UTC().Truncate(time.Second)
	if !input.RequestID.Valid || input.RequestID.Bytes == ([16]byte{}) || key == ([32]byte{}) || !validReason(input.Reason) ||
		!validIntakeText(input.Email, 3, 320, 320) || !intakeAddressOnly(input.Email) ||
		(input.DisplayName != "" && !validIntakeText(input.DisplayName, 1, 320, 80)) ||
		input.ExpiresAt != expires || expires.Before(now.Add(15*time.Minute)) || expires.After(now.Add(7*24*time.Hour)) ||
		input.Deliver && mailer == nil {
		return InvitationResult{}, ErrInput
	}
	if _, err := parseCanonicalUUID(flowIdentity); err != nil {
		return InvitationResult{}, ErrInput
	}
	name := invitationName(input.RequestID)
	fingerprint := invitationFingerprint(key, input.Email, input.DisplayName, expires, input.Deliver)
	reference := invitationReference(key, input.RequestID)
	var reserved db.ReserveRegistrationInvitationRow
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, now); err != nil {
			return err
		}
		var reserveErr error
		reserved, reserveErr = queries.ReserveRegistrationInvitation(txctx, db.ReserveRegistrationInvitationParams{
			IdempotencyKey: input.RequestID, InvitationName: name, FlowIdentity: flowIdentity,
			ExpiresAt: finiteTime(expires), ObservedAt: finiteTime(now), ActorUserID: actor.UserID,
			RequestFingerprint: fingerprint[:],
		})
		if reserveErr != nil {
			return fmt.Errorf("%w: reserve invitation", ErrUnavailable)
		}
		if reserved.Inserted {
			auditID, auditErr := queries.RecordRegistrationInvitationRequest(txctx, db.RecordRegistrationInvitationRequestParams{
				ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, Reason: pgtype.Text{String: input.Reason, Valid: true},
				InvitationRef: reference, RequestID: input.RequestID, ObservedAt: finiteTime(now),
			})
			if auditErr != nil || auditID <= 0 {
				return fmt.Errorf("%w: audit invitation request", ErrUnavailable)
			}
		}
		return nil
	})
	if err != nil {
		return InvitationResult{}, fmt.Errorf("reserve invitation: %w", err)
	}
	if reserved.TransitionState == "active" || reserved.TransitionState == "revoked" || reserved.TransitionState == "absent" {
		return InvitationResult{Status: reserved.TransitionState, Delivery: reserved.DeliveryState, Revision: reserved.AdministrationRevision, Completed: true}, nil
	}
	if (reserved.TransitionState != "creating" && reserved.TransitionState != "unknown") || reserved.AdministrationRevision <= 0 ||
		reserved.AuthentikInvitationName != name || reserved.FlowIdentity != flowIdentity || !reserved.ExpiresAt.Valid || !reserved.ExpiresAt.Time.Equal(expires) || !hmac.Equal(reserved.RequestFingerprint, fingerprint[:]) {
		return InvitationResult{}, ErrConflict
	}

	invitation, adopted, remoteErr := resolveInvitation(ctx, remote, reserved, input)
	if remoteErr != nil {
		failureAt, clockErr := observedAt(clock)
		if clockErr != nil {
			return InvitationResult{}, clockErr
		}
		failure := remoteFailureClass(remoteErr)
		if errors.Is(remoteErr, authentikgateway.ErrRemoteConflict) {
			failure = "operator_required"
		}
		if recordErr := recordInvitationFailure(ctx, beginner, actor.UserID, input, reserved, reference, failure, "not_requested", failureAt); recordErr != nil {
			return InvitationResult{}, fmt.Errorf("%w: remote %s; record failure", ErrUnavailable, failure)
		}
		return InvitationResult{}, fmt.Errorf("%w: %s", ErrRemote, failure)
	}
	delivery := "not_requested"
	resultClass := "created"
	if adopted {
		delivery, resultClass = "unknown", "adopted"
	} else if input.Deliver {
		delivery, err = mailer.SendInvitation(ctx, input.Email, input.DisplayName, invitation.UUID, expires)
		delivery = classifyInvitationDelivery(delivery, err)
	}
	resultAt, err := observedAt(clock)
	if err != nil {
		return InvitationResult{}, err
	}
	var result InvitationResult
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, resultAt); err != nil {
			return err
		}
		row, completeErr := queries.CompleteRegistrationInvitation(txctx, db.CompleteRegistrationInvitationParams{
			DeliveryState: delivery, ObservedAt: finiteTime(resultAt), IdempotencyKey: input.RequestID,
			ExpectedRevision: reserved.AdministrationRevision, ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true},
			Reason: pgtype.Text{String: input.Reason, Valid: true}, InvitationRef: reference,
			PreviousState: reserved.TransitionState, ResultClass: resultClass, RequestID: input.RequestID,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if completeErr != nil || row.AdministrationRevision != reserved.AdministrationRevision+1 || row.AuditID <= 0 {
			return fmt.Errorf("%w: complete invitation", ErrUnavailable)
		}
		result = InvitationResult{Status: "active", Delivery: delivery, Revision: row.AdministrationRevision, AuditID: row.AuditID, Completed: true}
		if !adopted {
			result.TokenUUID = invitation.UUID
		}
		return nil
	})
	if err != nil {
		return InvitationResult{}, fmt.Errorf("complete invitation: %w", err)
	}
	return result, nil
}

func classifyInvitationDelivery(delivery string, err error) string {
	if err == nil && delivery == "queued" || err != nil && (delivery == "failed" || delivery == "unknown") {
		return delivery
	}
	return "unknown"
}

func RevokeInvitation(ctx context.Context, beginner transactionBeginner, remote ControlGateway, clock func() time.Time, actor policy.AccessContext, input InvitationRevocationInput, key [32]byte) (InvitationRevocationResult, error) {
	if ctx == nil || beginner == nil || remote == nil || clock == nil || key == ([32]byte{}) || !policy.CanAdminister(actor) {
		return InvitationRevocationResult{}, ErrDenied
	}
	if !input.RequestID.Valid || input.RequestID.Bytes == ([16]byte{}) || !validReason(input.Reason) {
		return InvitationRevocationResult{}, ErrInput
	}
	now, err := observedAt(clock)
	if err != nil {
		return InvitationRevocationResult{}, err
	}
	idempotencyKey, handleRevision, err := verifyInvitationHandle(input.Handle, now, key)
	if err != nil {
		return InvitationRevocationResult{}, ErrInput
	}
	reference := invitationReference(key, idempotencyKey)
	var reserved db.BeginRegistrationInvitationRevocationRow
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, now); err != nil {
			return err
		}
		var beginErr error
		reserved, beginErr = queries.BeginRegistrationInvitationRevocation(txctx, db.BeginRegistrationInvitationRevocationParams{
			IdempotencyKey: idempotencyKey, ExpectedRevision: handleRevision, ObservedAt: finiteTime(now),
			ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, Reason: pgtype.Text{String: input.Reason, Valid: true},
			InvitationRef: reference, RequestID: input.RequestID,
		})
		if errors.Is(beginErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if beginErr != nil || reserved.AuthentikInvitationName == "" || !reserved.ExpiresAt.Valid || reserved.AdministrationRevision <= 0 || reserved.Inserted && reserved.AuditID <= 0 || !reserved.Inserted && reserved.AuditID != 0 {
			return fmt.Errorf("%w: begin invitation revocation", ErrUnavailable)
		}
		return nil
	})
	if err != nil {
		return InvitationRevocationResult{}, fmt.Errorf("begin invitation revocation: %w", err)
	}
	if reserved.TransitionState == "revoked" || reserved.TransitionState == "absent" {
		return InvitationRevocationResult{Status: reserved.TransitionState, Result: "completed", Revision: reserved.AdministrationRevision, Completed: true}, nil
	}
	if reserved.TransitionState != "revoke_required" {
		return InvitationRevocationResult{}, ErrConflict
	}

	resultState, resultClass, failureClass := revokeRemoteInvitation(ctx, remote, reserved.AuthentikInvitationName, reserved.ExpiresAt.Time)
	actionType := "record_invitation_result"
	if resultState == "revoked" {
		actionType = "revoke_invitation"
	} else if resultState == "absent" {
		actionType = "record_invitation_absent"
	}
	resultAt, clockErr := observedAt(clock)
	if clockErr != nil {
		return InvitationRevocationResult{}, clockErr
	}
	var result InvitationRevocationResult
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, resultAt); err != nil {
			return err
		}
		failure := pgtype.Text{}
		if failureClass != "" {
			failure = pgtype.Text{String: failureClass, Valid: true}
		}
		row, completeErr := queries.CompleteRegistrationInvitationRevocation(txctx, db.CompleteRegistrationInvitationRevocationParams{
			ResultingState: resultState, ObservedAt: finiteTime(resultAt), FailureClass: failure,
			IdempotencyKey: idempotencyKey, ExpectedRevision: reserved.AdministrationRevision,
			ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, ActionType: actionType,
			Reason: pgtype.Text{String: input.Reason, Valid: true}, InvitationRef: reference,
			ResultClass: resultClass, RequestID: input.RequestID,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if completeErr != nil || row.AdministrationRevision != reserved.AdministrationRevision+1 || row.AuditID <= 0 {
			return fmt.Errorf("%w: complete invitation revocation", ErrUnavailable)
		}
		result = InvitationRevocationResult{Status: resultState, Result: resultClass, Revision: row.AdministrationRevision, AuditID: row.AuditID, Completed: resultState == "revoked" || resultState == "absent"}
		return nil
	})
	if err != nil {
		return InvitationRevocationResult{}, fmt.Errorf("complete invitation revocation: %w", err)
	}
	if result.Status == "unknown" {
		return result, fmt.Errorf("%w: %s", ErrRemote, failureClass)
	}
	return result, nil
}

func revokeRemoteInvitation(ctx context.Context, remote ControlGateway, name string, expiresAt time.Time) (string, string, string) {
	invitations, more, err := remote.Invitations(ctx)
	if err != nil {
		return "unknown", remoteFailureClass(err), remoteFailureClass(err)
	}
	var found *authentikgateway.Invitation
	for index := range invitations {
		if invitations[index].Name != name {
			continue
		}
		if found != nil {
			return "unknown", "operator_required", "operator_required"
		}
		found = &invitations[index]
	}
	if found == nil {
		if more {
			return "unknown", "operator_required", "operator_required"
		}
		return "absent", "already_absent", ""
	}
	remoteExpiry, parseErr := time.Parse(time.RFC3339, found.Expires)
	if parseErr != nil || !found.SingleUse || !remoteExpiry.Equal(expiresAt) {
		return "unknown", "operator_required", "operator_required"
	}
	if err := remote.DeleteInvitation(ctx, found.UUID); err != nil {
		if errors.Is(err, authentikgateway.ErrAbsentObject) {
			return "absent", "already_absent", ""
		}
		failure := remoteFailureClass(err)
		return "unknown", failure, failure
	}
	_, err = remote.Invitation(ctx, found.UUID)
	if errors.Is(err, authentikgateway.ErrAbsentObject) {
		return "revoked", "confirmed", ""
	}
	if err == nil {
		return "unknown", "remote_conflict", "remote_conflict"
	}
	failure := remoteFailureClass(err)
	return "unknown", failure, failure
}

func resolveInvitation(ctx context.Context, remote ControlGateway, reserved db.ReserveRegistrationInvitationRow, input InvitationInput) (authentikgateway.Invitation, bool, error) {
	if !reserved.Inserted {
		invitations, more, err := remote.Invitations(ctx)
		if err != nil {
			return authentikgateway.Invitation{}, false, err
		}
		for _, invitation := range invitations {
			if invitation.Name == reserved.AuthentikInvitationName {
				if !invitationMatches(invitation, reserved.AuthentikInvitationName, input.Email, input.DisplayName, input.ExpiresAt) {
					return authentikgateway.Invitation{}, false, authentikgateway.ErrRemoteConflict
				}
				return invitation, true, nil
			}
		}
		if more {
			return authentikgateway.Invitation{}, false, authentikgateway.ErrRemoteConflict
		}
	}
	invitation, err := remote.CreateInvitation(ctx, reserved.AuthentikInvitationName, input.ExpiresAt.Format(time.RFC3339), input.Email, input.DisplayName)
	if err != nil {
		return authentikgateway.Invitation{}, false, err
	}
	if !invitationMatches(invitation, reserved.AuthentikInvitationName, input.Email, input.DisplayName, input.ExpiresAt) {
		return authentikgateway.Invitation{}, false, authentikgateway.ErrRemoteInvalid
	}
	return invitation, false, nil
}

func invitationMatches(invitation authentikgateway.Invitation, name, email, displayName string, expires time.Time) bool {
	parsed, err := time.Parse(time.RFC3339, invitation.Expires)
	_, uuidErr := parseCanonicalUUID(invitation.UUID)
	return err == nil && uuidErr == nil && invitation.Name == name && invitation.Email == email && invitation.DisplayName == displayName && invitation.SingleUse && parsed.Equal(expires)
}

func recordInvitationFailure(ctx context.Context, beginner transactionBeginner, actorID int64, input InvitationInput, reserved db.ReserveRegistrationInvitationRow, reference, failure, delivery string, at time.Time) error {
	return inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actorID, at); err != nil {
			return err
		}
		row, err := queries.RecordRegistrationInvitationFailure(txctx, db.RecordRegistrationInvitationFailureParams{
			DeliveryState: delivery, ObservedAt: finiteTime(at), FailureClass: pgtype.Text{String: failure, Valid: true},
			IdempotencyKey: input.RequestID, ExpectedRevision: reserved.AdministrationRevision,
			ActorUserID: pgtype.Int8{Int64: actorID, Valid: true}, Reason: pgtype.Text{String: input.Reason, Valid: true},
			InvitationRef: reference, PreviousState: reserved.TransitionState, RequestID: input.RequestID,
		})
		if err != nil || row.AdministrationRevision != reserved.AdministrationRevision+1 || row.AuditID <= 0 {
			return fmt.Errorf("record invitation failure")
		}
		return nil
	})
}

func invitationName(requestID pgtype.UUID) string {
	return fmt.Sprintf("gotth-bb-%x", requestID.Bytes)
}

func invitationFingerprint(key [32]byte, email, displayName string, expires time.Time, deliver bool) [32]byte {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("gotth-bb/invitation-request/v1\x00"))
	for _, value := range []string{email, displayName, expires.UTC().Format(time.RFC3339)} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(value))
	}
	if deliver {
		_, _ = mac.Write([]byte{1})
	} else {
		_, _ = mac.Write([]byte{0})
	}
	var result [32]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func invitationReference(key [32]byte, requestID pgtype.UUID) string {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("gotth-bb/invitation-audit/v1\x00"))
	_, _ = mac.Write(requestID.Bytes[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func issueInvitationHandle(now time.Time, key [32]byte, idempotencyKey pgtype.UUID, revision int64) (string, error) {
	if now.IsZero() || key == ([32]byte{}) || !idempotencyKey.Valid || idempotencyKey.Bytes == ([16]byte{}) || revision <= 0 || revision > maximumInvitationHandleRevision {
		return "", ErrInput
	}
	var raw [invitationHandleRawBytes]byte
	raw[0] = 1
	binary.BigEndian.PutUint64(raw[1:9], uint64(now.UTC().Truncate(time.Second).Add(invitationHandleLifetime).Unix()))
	copy(raw[9:25], idempotencyKey.Bytes[:])
	binary.BigEndian.PutUint64(raw[25:33], uint64(revision))
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("gotth-bb/invitation-revoke-handle/v1\x00"))
	_, _ = mac.Write(raw[:33])
	copy(raw[33:], mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func verifyInvitationHandle(encoded string, now time.Time, key [32]byte) (pgtype.UUID, int64, error) {
	if len(encoded) != base64.RawURLEncoding.EncodedLen(invitationHandleRawBytes) || now.IsZero() || key == ([32]byte{}) {
		return pgtype.UUID{}, 0, ErrInput
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != invitationHandleRawBytes || base64.RawURLEncoding.EncodeToString(raw) != encoded || raw[0] != 1 {
		return pgtype.UUID{}, 0, ErrInput
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("gotth-bb/invitation-revoke-handle/v1\x00"))
	_, _ = mac.Write(raw[:33])
	if !hmac.Equal(raw[33:], mac.Sum(nil)) {
		return pgtype.UUID{}, 0, ErrInput
	}
	expires := int64(binary.BigEndian.Uint64(raw[1:9]))
	revision := int64(binary.BigEndian.Uint64(raw[25:33]))
	if expires <= 0 || revision <= 0 || revision > maximumInvitationHandleRevision || now.UTC().Unix() > expires || time.Unix(expires, 0).After(now.UTC().Add(invitationHandleLifetime)) {
		return pgtype.UUID{}, 0, ErrInput
	}
	var idBytes [16]byte
	copy(idBytes[:], raw[9:25])
	id := pgtype.UUID{Bytes: idBytes, Valid: idBytes != ([16]byte{})}
	if !id.Valid {
		return pgtype.UUID{}, 0, ErrInput
	}
	return id, revision, nil
}
