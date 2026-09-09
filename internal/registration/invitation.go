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
		failure := remoteFailureClass(remoteErr)
		if errors.Is(remoteErr, authentikgateway.ErrRemoteConflict) {
			failure = "operator_required"
		}
		if recordErr := recordInvitationFailure(ctx, beginner, actor.UserID, input, reserved, reference, failure, "not_requested", now); recordErr != nil {
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
		if err != nil || (delivery != "queued" && delivery != "failed" && delivery != "unknown") {
			delivery = "unknown"
		}
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
	return err == nil && invitation.Name == name && invitation.Email == email && invitation.DisplayName == displayName && invitation.SingleUse && parsed.Equal(expires)
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
