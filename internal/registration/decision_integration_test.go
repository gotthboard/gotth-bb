//go:build integration

package registration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const decisionTestDatabase = "gotth_bb_beta109_registration_test"

type recordingGateway struct {
	operations  []string
	failNext    error
	pending     []authentikgateway.User
	state       authentikgateway.UserState
	invitation  authentikgateway.Invitation
	invitations []authentikgateway.Invitation
	createCalls int
	deleteCalls int
	afterUser   func() error
	userState   func(string) authentikgateway.UserState
}

func (gateway *recordingGateway) AddUser(_ context.Context, group, subject string) error {
	gateway.operations = append(gateway.operations, "add:"+group+":"+subject)
	return gateway.takeFailure()
}

func (gateway *recordingGateway) RemoveUser(_ context.Context, group, subject string) error {
	gateway.operations = append(gateway.operations, "remove:"+group+":"+subject)
	return gateway.takeFailure()
}

func (gateway *recordingGateway) takeFailure() error {
	err := gateway.failNext
	gateway.failNext = nil
	return err
}

func (gateway *recordingGateway) PendingUsers(context.Context) ([]authentikgateway.User, bool, error) {
	return gateway.pending, false, gateway.takeFailure()
}

func (gateway *recordingGateway) User(_ context.Context, subject string) (authentikgateway.UserState, error) {
	if err := gateway.takeFailure(); err != nil {
		return authentikgateway.UserState{}, err
	}
	if gateway.afterUser != nil {
		after := gateway.afterUser
		gateway.afterUser = nil
		if err := after(); err != nil {
			return authentikgateway.UserState{}, err
		}
	}
	if gateway.userState != nil {
		return gateway.userState(subject), nil
	}
	return gateway.state, nil
}
func (gateway *recordingGateway) CreateInvitation(_ context.Context, name, expires, email, displayName string) (authentikgateway.Invitation, error) {
	gateway.createCalls++
	result := gateway.invitation
	result.Name, result.Expires, result.Email, result.DisplayName, result.SingleUse = name, expires, email, displayName, true
	return result, gateway.takeFailure()
}
func (gateway *recordingGateway) Invitations(context.Context) ([]authentikgateway.Invitation, bool, error) {
	return gateway.invitations, false, gateway.takeFailure()
}
func (gateway *recordingGateway) Invitation(_ context.Context, uuid string) (authentikgateway.Invitation, error) {
	if err := gateway.takeFailure(); err != nil {
		return authentikgateway.Invitation{}, err
	}
	for _, invitation := range gateway.invitations {
		if invitation.UUID == uuid {
			return invitation, nil
		}
	}
	return authentikgateway.Invitation{}, authentikgateway.ErrAbsentObject
}
func (gateway *recordingGateway) DeleteInvitation(_ context.Context, uuid string) error {
	if err := gateway.takeFailure(); err != nil {
		return err
	}
	for index, invitation := range gateway.invitations {
		if invitation.UUID == uuid {
			gateway.invitations = append(gateway.invitations[:index], gateway.invitations[index+1:]...)
			gateway.deleteCalls++
			return nil
		}
	}
	return authentikgateway.ErrAbsentObject
}

func TestRegistrationDecisionsOnPostgreSQL17(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	connection := decisionTestConnection(t, ctx)

	var administratorID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, authentik_sync_state) VALUES ('Administrator', 'administrator', 'accepted') RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	actor := policy.AccessContext{Authenticated: true, UserID: administratorID, Role: policy.RoleAdministrator}
	key := [32]byte{0x41}
	baseTime := time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC)
	clock := func() time.Time { return baseTime }
	gateway := &recordingGateway{}
	invitationRequest := pgtype.UUID{Bytes: [16]byte{0xe5}, Valid: true}
	invitationExpiry := baseTime.Add(time.Hour)
	gateway.invitation = authentikgateway.Invitation{UUID: "66666666-6666-4666-8666-666666666666"}
	invitation, err := CreateInvitation(ctx, connection, gateway, nil, clock, actor, InvitationInput{
		Email: "invitee@example.test", DisplayName: "Invited Member", Reason: "Invite a verified participant",
		ExpiresAt: invitationExpiry, RequestID: invitationRequest,
	}, key, "99999999-9999-4999-8999-999999999999")
	if err != nil || invitation.Status != "active" || invitation.Delivery != "not_requested" || invitation.Revision != 2 || invitation.AuditID <= 0 || invitation.TokenUUID != gateway.invitation.UUID || gateway.createCalls != 1 {
		t.Fatalf("invitation creation = (%+v, %v, calls %d)", invitation, err, gateway.createCalls)
	}
	replayedInvitation, err := CreateInvitation(ctx, connection, gateway, nil, clock, actor, InvitationInput{
		Email: "changed@example.test", Reason: "Replay completed request", ExpiresAt: invitationExpiry, RequestID: invitationRequest,
	}, key, "99999999-9999-4999-8999-999999999999")
	if err != nil || !replayedInvitation.Completed || replayedInvitation.Status != "active" || replayedInvitation.TokenUUID != "" || gateway.createCalls != 1 {
		t.Fatalf("invitation replay = (%+v, %v, calls %d)", replayedInvitation, err, gateway.createCalls)
	}
	gateway.invitations = []authentikgateway.Invitation{{UUID: gateway.invitation.UUID, Name: invitationName(invitationRequest), Expires: invitationExpiry.Format(time.RFC3339), Email: "invitee@example.test", DisplayName: "Invited Member", SingleUse: true}}
	invitationPage, err := ListInvitations(ctx, db.New(connection), gateway, actor, baseTime, key)
	if err != nil || len(invitationPage.Invitations) != 1 || invitationPage.Invitations[0].Status != "active" || invitationPage.Invitations[0].Handle == "" {
		t.Fatalf("invitation page = (%+v, %v)", invitationPage, err)
	}
	revokeRequest := pgtype.UUID{Bytes: [16]byte{0xc7}, Valid: true}
	revoked, err := RevokeInvitation(ctx, connection, gateway, clock, actor, InvitationRevocationInput{Handle: invitationPage.Invitations[0].Handle, Reason: "Withdraw invitation", RequestID: revokeRequest}, key)
	if err != nil || revoked.Status != "revoked" || revoked.Result != "confirmed" || !revoked.Completed || revoked.Revision != 4 || revoked.AuditID <= 0 || gateway.deleteCalls != 1 {
		t.Fatalf("invitation revocation = (%+v, %v, deletes %d)", revoked, err, gateway.deleteCalls)
	}
	replayedRevocation, err := RevokeInvitation(ctx, connection, gateway, clock, actor, InvitationRevocationInput{Handle: invitationPage.Invitations[0].Handle, Reason: "Withdraw invitation", RequestID: revokeRequest}, key)
	if err != nil || replayedRevocation.Status != "revoked" || !replayedRevocation.Completed || gateway.deleteCalls != 1 {
		t.Fatalf("invitation revocation replay = (%+v, %v, deletes %d)", replayedRevocation, err, gateway.deleteCalls)
	}

	failedInvitationRequest := pgtype.UUID{Bytes: [16]byte{0xe6}, Valid: true}
	failedInvitationExpiry := baseTime.Add(2 * time.Hour)
	failedInvitationInput := InvitationInput{
		Email: "retry-invitee@example.test", DisplayName: "Retry Invitee", Reason: "Retry an uncertain invitation",
		ExpiresAt: failedInvitationExpiry, RequestID: failedInvitationRequest,
	}
	gateway.failNext = authentikgateway.ErrRemoteUnavailable
	failedInvitation, err := CreateInvitation(ctx, connection, gateway, nil, clock, actor, failedInvitationInput, key, "99999999-9999-4999-8999-999999999999")
	if !errors.Is(err, ErrRemote) || failedInvitation != (InvitationResult{}) || gateway.createCalls != 2 {
		t.Fatalf("failed invitation creation = (%+v, %v, calls %d)", failedInvitation, err, gateway.createCalls)
	}
	var failedInvitationUnknown bool
	var failedInvitationAudits int
	if err := connection.QueryRow(ctx, `SELECT
		transition_state = 'unknown' AND delivery_state = 'not_requested' AND failure_class = 'remote_unavailable' AND administration_revision = 2,
		(SELECT count(*) FROM public.moderation_actions WHERE request_id = $1 AND action_type IN ('request_create_invitation', 'record_invitation_result'))
		FROM public.registration_invitations WHERE idempotency_key = $1`, failedInvitationRequest).Scan(&failedInvitationUnknown, &failedInvitationAudits); err != nil || !failedInvitationUnknown || failedInvitationAudits != 2 {
		t.Fatalf("failed invitation persistence = (unknown %t, audits %d, %v)", failedInvitationUnknown, failedInvitationAudits, err)
	}
	const adoptedInvitationUUID = "67676767-6767-4767-8767-676767676767"
	gateway.invitations = []authentikgateway.Invitation{{
		UUID: adoptedInvitationUUID, Name: invitationName(failedInvitationRequest), Expires: failedInvitationExpiry.Format(time.RFC3339),
		Email: failedInvitationInput.Email, DisplayName: failedInvitationInput.DisplayName, SingleUse: true,
	}}
	adoptedInvitation, err := CreateInvitation(ctx, connection, gateway, nil, clock, actor, failedInvitationInput, key, "99999999-9999-4999-8999-999999999999")
	if err != nil || adoptedInvitation.Status != "active" || adoptedInvitation.Delivery != "unknown" || adoptedInvitation.TokenUUID != "" || adoptedInvitation.Revision != 3 || gateway.createCalls != 2 {
		t.Fatalf("adopted invitation retry = (%+v, %v, calls %d)", adoptedInvitation, err, gateway.createCalls)
	}
	terminalInvitation, err := CreateInvitation(ctx, connection, gateway, nil, clock, actor, InvitationInput{
		Email: "changed@example.test", Reason: "Replay completed uncertain invitation", ExpiresAt: failedInvitationExpiry, RequestID: failedInvitationRequest,
	}, key, "99999999-9999-4999-8999-999999999999")
	if err != nil || !terminalInvitation.Completed || terminalInvitation.Status != "active" || terminalInvitation.Delivery != "unknown" || terminalInvitation.TokenUUID != "" || gateway.createCalls != 2 {
		t.Fatalf("terminal invitation replay = (%+v, %v, calls %d)", terminalInvitation, err, gateway.createCalls)
	}

	controlledSubject := "33333333-3333-4333-8333-333333333333"
	var controlledUserID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, created_at, updated_at, last_login_at, authentik_sync_state)
		VALUES ('Controlled Member', 'member', $1, $1, $1, 'accepted') RETURNING id`, baseTime).Scan(&controlledUserID); err != nil {
		t.Fatalf("insert controlled member: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, 'https://auth.example.test/application/o/gotth-bb/', $2)`, controlledUserID, controlledSubject); err != nil {
		t.Fatalf("insert controlled identity: %v", err)
	}
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 303, UUID: controlledSubject, Username: "controlled", Name: "Controlled Member", Email: "controlled@example.test", Active: true}, Suspended: true}
	syncTime := baseTime.Add(4 * time.Hour)
	syncClock := func() time.Time { return syncTime }
	suspensionRequest := pgtype.UUID{Bytes: [16]byte{0xb6}, Valid: true}
	controlledSuspension, err := ChangeUserSuspension(ctx, connection, gateway, syncClock, actor, controlledUserID, true, "Contain controlled identity", suspensionRequest)
	if err != nil || !controlledSuspension.Suspended || controlledSuspension.Revision != 3 || controlledSuspension.AuditID <= 0 {
		t.Fatalf("controlled suspension = (%+v, %v)", controlledSuspension, err)
	}
	var suspended, removalConfirmed bool
	if err := connection.QueryRow(ctx, `SELECT suspended_at IS NOT NULL, authentik_sync_state = 'suspended' FROM public.users WHERE id = $1`, controlledUserID).Scan(&suspended, &removalConfirmed); err != nil || !suspended || !removalConfirmed {
		t.Fatalf("controlled suspension state = (%t, %t, %v)", suspended, removalConfirmed, err)
	}
	if got := gateway.operations[len(gateway.operations)-3:]; strings.Join(got, ",") != "remove:accepted:"+controlledSubject+",remove:pending:"+controlledSubject+",add:suspended:"+controlledSubject {
		t.Fatalf("suspension operations = %v", got)
	}

	gateway.state = authentikgateway.UserState{User: gateway.state.User, Suspended: true}
	gateway.failNext = authentikgateway.ErrRemoteUnavailable
	failedReinstatementRequest := pgtype.UUID{Bytes: [16]byte{0xa5}, Valid: true}
	if _, err := ChangeUserSuspension(ctx, connection, gateway, syncClock, actor, controlledUserID, false, "Restore controlled identity", failedReinstatementRequest); !errors.Is(err, ErrRemote) {
		t.Fatalf("failed controlled reinstatement error = %v", err)
	}
	var deniedAfterFailure bool
	if err := connection.QueryRow(ctx, `SELECT suspended_at IS NOT NULL AND authentik_sync_state = 'grant_required' AND authentik_sync_failure_class = 'remote_unavailable' FROM public.users WHERE id = $1`, controlledUserID).Scan(&deniedAfterFailure); err != nil || !deniedAfterFailure {
		t.Fatalf("failed reinstatement denial = (%t, %v)", deniedAfterFailure, err)
	}

	gateway.failNext = authentikgateway.ErrRemoteUnavailable
	failedReconciliationRequest := pgtype.UUID{Bytes: [16]byte{0x93}, Valid: true}
	if _, err := ReconcileIdentity(ctx, connection, gateway, syncClock, actor, controlledUserID, "Retry controlled identity", failedReconciliationRequest); !errors.Is(err, ErrRemote) {
		t.Fatalf("failed manual reconciliation error = %v", err)
	}
	if err := connection.QueryRow(ctx, `SELECT suspended_at IS NOT NULL AND authentik_sync_state = 'grant_required' AND authentik_sync_failure_class = 'remote_unavailable' AND administration_revision = 7 FROM public.users WHERE id = $1`, controlledUserID).Scan(&deniedAfterFailure); err != nil || !deniedAfterFailure {
		t.Fatalf("failed manual reconciliation denial = (%t, %v)", deniedAfterFailure, err)
	}

	gateway.state = authentikgateway.UserState{User: gateway.state.User, Accepted: true}
	reinstatementRequest := pgtype.UUID{Bytes: [16]byte{0x94}, Valid: true}
	controlledReconciliation, err := ReconcileIdentity(ctx, connection, gateway, syncClock, actor, controlledUserID, "Retry controlled identity", reinstatementRequest)
	if err != nil || controlledReconciliation.SyncState != "accepted" || controlledReconciliation.UserID != controlledUserID || controlledReconciliation.Revision != 9 || controlledReconciliation.AuditID <= 0 {
		t.Fatalf("controlled reconciliation = (%+v, %v)", controlledReconciliation, err)
	}
	var acceptedLocally bool
	if err := connection.QueryRow(ctx, `SELECT suspended_at IS NULL AND authentik_sync_state = 'accepted' AND authentik_sync_failure_class IS NULL FROM public.users WHERE id = $1`, controlledUserID).Scan(&acceptedLocally); err != nil || !acceptedLocally {
		t.Fatalf("controlled reconciliation state = (%t, %v)", acceptedLocally, err)
	}
	beforeConflict := len(gateway.operations)
	if _, err := ReconcileIdentity(ctx, connection, gateway, syncClock, actor, controlledUserID, "Retry controlled identity", pgtype.UUID{Bytes: [16]byte{0x95}, Valid: true}); !errors.Is(err, ErrConflict) || len(gateway.operations) != beforeConflict {
		t.Fatalf("terminal reconciliation retry = (error %v, operations %v)", err, gateway.operations[beforeConflict:])
	}

	actorRaceSubject := "abababab-abab-4bab-8bab-abababababab"
	var actorRaceUserID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, created_at, updated_at, last_login_at, suspended_at, suspension_reason, authentik_sync_state)
		VALUES ('Actor Race', 'member', $1, $1, $1, $2, 'Retry after actor revocation', 'grant_required') RETURNING id`, syncTime.Add(-2*time.Hour), syncTime.Add(-time.Hour)).Scan(&actorRaceUserID); err != nil {
		t.Fatalf("insert actor-race member: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, 'https://auth.example.test/application/o/gotth-bb/', $2)`, actorRaceUserID, actorRaceSubject); err != nil {
		t.Fatalf("insert actor-race identity: %v", err)
	}
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 313, UUID: actorRaceSubject, Username: "actor-race", Name: "Actor Race", Email: "actor-race@example.test", Active: true}, Accepted: true}
	gateway.afterUser = func() error {
		_, updateErr := connection.Exec(ctx, `UPDATE public.users SET authentik_sync_state = 'unknown' WHERE id = $1`, administratorID)
		return updateErr
	}
	actorRaceResult, err := ReconcileIdentity(ctx, connection, gateway, syncClock, actor, actorRaceUserID, "Retry after actor revocation", pgtype.UUID{Bytes: [16]byte{0x97}, Valid: true})
	if !errors.Is(err, ErrDenied) || actorRaceResult != (IdentityReconciliationResult{}) {
		t.Fatalf("actor-revoked reconciliation = (%+v, %v)", actorRaceResult, err)
	}
	var actorRaceRevision int64
	var actorRaceRequests, actorRaceResults int
	if err := connection.QueryRow(ctx, `SELECT administration_revision,
		(SELECT count(*) FROM public.moderation_actions WHERE target_user_id = $1 AND action_type = 'request_identity_reconciliation'),
		(SELECT count(*) FROM public.moderation_actions WHERE target_user_id = $1 AND action_type = 'reconcile_identity_access')
		FROM public.users WHERE id = $1 AND authentik_sync_state = 'grant_required'`, actorRaceUserID).Scan(&actorRaceRevision, &actorRaceRequests, &actorRaceResults); err != nil || actorRaceRevision != 2 || actorRaceRequests != 1 || actorRaceResults != 0 {
		t.Fatalf("actor-revoked state = (revision %d, requests %d, results %d, %v)", actorRaceRevision, actorRaceRequests, actorRaceResults, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.users SET authentik_sync_state = 'accepted' WHERE id = $1`, administratorID); err != nil {
		t.Fatalf("restore administrator identity: %v", err)
	}
	actorRaceResult, err = ReconcileIdentity(ctx, connection, gateway, syncClock, actor, actorRaceUserID, "Retry after actor revocation", pgtype.UUID{Bytes: [16]byte{0x98}, Valid: true})
	if err != nil || actorRaceResult.SyncState != "accepted" || actorRaceResult.Revision != 4 || actorRaceResult.AuditID <= 0 {
		t.Fatalf("actor-restored reconciliation = (%+v, %v)", actorRaceResult, err)
	}

	removalSubject := "77777777-7777-4777-8777-777777777777"
	var removalUserID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, created_at, updated_at, last_login_at, suspended_at, suspension_reason, authentik_sync_state)
		VALUES ('Removal Retry', 'member', $1, $1, $1, $1, 'Manual removal retry', 'removal_required') RETURNING id`, syncTime).Scan(&removalUserID); err != nil {
		t.Fatalf("insert removal retry member: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, 'https://auth.example.test/application/o/gotth-bb/', $2)`, removalUserID, removalSubject); err != nil {
		t.Fatalf("insert removal retry identity: %v", err)
	}
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 707, UUID: removalSubject, Username: "removal", Name: "Removal Retry", Email: "removal@example.test", Active: true}, Suspended: true}
	removalResult, err := ReconcileIdentity(ctx, connection, gateway, syncClock, actor, removalUserID, "Retry identity removal", pgtype.UUID{Bytes: [16]byte{0x96}, Valid: true})
	if err != nil || removalResult.SyncState != "suspended" || removalResult.UserID != removalUserID || removalResult.Revision != 3 || removalResult.AuditID <= 0 {
		t.Fatalf("removal reconciliation = (%+v, %v)", removalResult, err)
	}

	expiredSubject := "88888888-8888-4888-8888-888888888888"
	var expiredUserID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, created_at, updated_at, last_login_at, suspended_at, suspended_until, suspension_reason, authentik_sync_state)
		VALUES ('Expired Member', 'member', $1, $1, $1, $2, $3, 'Finite suspension', 'suspended') RETURNING id`, syncTime.Add(-2*time.Hour), syncTime.Add(-time.Hour), syncTime.Add(-time.Minute)).Scan(&expiredUserID); err != nil {
		t.Fatalf("insert expired member: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, 'https://auth.example.test/application/o/gotth-bb/', $2)`, expiredUserID, expiredSubject); err != nil {
		t.Fatalf("insert expired identity: %v", err)
	}
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 808, UUID: expiredSubject, Username: "expired", Name: "Expired Member", Email: "expired@example.test", Active: true}, Accepted: true}
	expiryResult, err := ReconcileExpiredSuspensions(ctx, connection, gateway, syncClock, strings.NewReader(strings.Repeat("r", 16)))
	if err != nil || expiryResult != (ExpiryReconciliationResult{Claimed: 1, Completed: 1}) {
		t.Fatalf("expiry reconciliation = (%+v, %v)", expiryResult, err)
	}
	var expiryAccepted bool
	if err := connection.QueryRow(ctx, `SELECT suspended_at IS NULL AND suspended_until IS NULL AND authentik_sync_state = 'accepted' AND administration_revision = 3 FROM public.users WHERE id = $1`, expiredUserID).Scan(&expiryAccepted); err != nil || !expiryAccepted {
		t.Fatalf("expiry reconciliation state = (%t, %v)", expiryAccepted, err)
	}

	failedExpirySubject := "99999999-9999-4999-8999-999999999999"
	var failedExpiryUserID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, created_at, updated_at, last_login_at, suspended_at, suspended_until, suspension_reason, authentik_sync_state)
		VALUES ('Failed Expiry', 'member', $1, $1, $1, $2, $3, 'Finite suspension', 'suspended') RETURNING id`, syncTime.Add(-2*time.Hour), syncTime.Add(-time.Hour), syncTime.Add(-time.Minute)).Scan(&failedExpiryUserID); err != nil {
		t.Fatalf("insert failed expiry member: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, 'https://auth.example.test/application/o/gotth-bb/', $2)`, failedExpiryUserID, failedExpirySubject); err != nil {
		t.Fatalf("insert failed expiry identity: %v", err)
	}
	gateway.failNext = authentikgateway.ErrRemoteUnavailable
	expiryResult, err = ReconcileExpiredSuspensions(ctx, connection, gateway, syncClock, strings.NewReader(strings.Repeat("s", 16)))
	if err != nil || expiryResult != (ExpiryReconciliationResult{Claimed: 1, Failed: 1}) {
		t.Fatalf("failed expiry reconciliation = (%+v, %v)", expiryResult, err)
	}
	var expiryDenied bool
	var expiryNextAttempt time.Time
	if err := connection.QueryRow(ctx, `SELECT suspended_at IS NOT NULL AND authentik_sync_state = 'grant_required' AND authentik_sync_failure_class = 'remote_unavailable' AND administration_revision = 3, authentik_sync_next_attempt_at FROM public.users WHERE id = $1`, failedExpiryUserID).Scan(&expiryDenied, &expiryNextAttempt); err != nil || !expiryDenied || !expiryNextAttempt.Equal(syncTime.Add(time.Minute)) {
		t.Fatalf("failed expiry denial = (%t, next %s, %v)", expiryDenied, expiryNextAttempt, err)
	}

	raceSubject := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var raceUserID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, created_at, updated_at, last_login_at, suspended_at, suspended_until, suspension_reason, authentik_sync_state)
		VALUES ('Concurrent Expiry', 'member', $1, $1, $1, $2, $3, 'Finite suspension', 'suspended') RETURNING id`, syncTime.Add(-2*time.Hour), syncTime.Add(-time.Hour), syncTime.Add(-time.Minute)).Scan(&raceUserID); err != nil {
		t.Fatalf("insert concurrent expiry member: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, 'https://auth.example.test/application/o/gotth-bb/', $2)`, raceUserID, raceSubject); err != nil {
		t.Fatalf("insert concurrent expiry identity: %v", err)
	}
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 909, UUID: raceSubject, Username: "concurrent", Name: "Concurrent Expiry", Email: "concurrent@example.test", Active: true}, Accepted: true}
	gateway.afterUser = func() error {
		_, updateErr := connection.Exec(ctx, `UPDATE public.users
			SET suspended_at = NULL, suspended_until = NULL, suspension_reason = NULL,
			    authentik_sync_state = 'accepted', administration_revision = administration_revision + 1
			WHERE id = $1`, raceUserID)
		return updateErr
	}
	expiryResult, err = ReconcileExpiredSuspensions(ctx, connection, gateway, syncClock, strings.NewReader(strings.Repeat("t", 16)))
	if err != nil || expiryResult != (ExpiryReconciliationResult{Claimed: 1, Superseded: 1}) {
		t.Fatalf("concurrent expiry reconciliation = (%+v, %v)", expiryResult, err)
	}
	var raceAccepted bool
	var requestAudits, resultAudits, supersededAudits int
	if err := connection.QueryRow(ctx, `SELECT
			suspended_at IS NULL AND suspended_until IS NULL AND authentik_sync_state = 'accepted' AND administration_revision = 3,
			(SELECT count(*) FROM public.moderation_actions WHERE target_user_id = $1 AND action_type = 'request_identity_reconciliation'),
			(SELECT count(*) FROM public.moderation_actions WHERE target_user_id = $1 AND action_type = 'reconcile_identity_access'),
			(SELECT count(*) FROM public.moderation_actions WHERE target_user_id = $1 AND action_type = 'reconcile_identity_access' AND resulting_state->>'result' = 'superseded')
		FROM public.users WHERE id = $1`, raceUserID).Scan(&raceAccepted, &requestAudits, &resultAudits, &supersededAudits); err != nil || !raceAccepted || requestAudits != 1 || resultAudits != 1 || supersededAudits != 1 {
		t.Fatalf("concurrent expiry state = (accepted %t, request %d, result %d, superseded %d, %v)", raceAccepted, requestAudits, resultAudits, supersededAudits, err)
	}

	workerConfig, err := pgx.ParseConfig(os.Getenv("GOTTH_BB_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse worker database URL: %v", err)
	}
	workerConfig.Database = decisionTestDatabase
	workerConnection, err := pgx.ConnectConfig(ctx, workerConfig)
	if err != nil {
		t.Fatalf("connect second expiry worker: %v", err)
	}
	t.Cleanup(func() { _ = workerConnection.Close(context.Background()) })
	guard, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin advisory-lock guard: %v", err)
	}
	if _, err := guard.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('gotth-bb'), pg_catalog.hashtext('expired-identity-reconciliation'))`); err != nil {
		_ = guard.Rollback(ctx)
		t.Fatalf("hold expiry advisory lock: %v", err)
	}
	lockedResult, err := ReconcileExpiredSuspensions(ctx, workerConnection, gateway, syncClock, strings.NewReader(strings.Repeat("u", 16)))
	if rollbackErr := guard.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release expiry advisory lock: %v", rollbackErr)
	}
	if err != nil || lockedResult != (ExpiryReconciliationResult{}) {
		t.Fatalf("advisory-locked expiry reconciliation = (%+v, %v)", lockedResult, err)
	}

	batchSubjects := []string{
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		"ffffffff-ffff-4fff-8fff-ffffffffffff",
		"12121212-1212-4212-8212-121212121212",
	}
	for index, subject := range batchSubjects {
		var userID int64
		if err := connection.QueryRow(ctx, `INSERT INTO public.users
			(display_name, role, created_at, updated_at, last_login_at, suspended_at, suspended_until, suspension_reason, authentik_sync_state)
			VALUES ($1, 'member', $2, $2, $2, $3, $4, 'Finite suspension batch', 'suspended') RETURNING id`, fmt.Sprintf("Expiry Batch %d", index), syncTime.Add(-2*time.Hour), syncTime.Add(-time.Hour), syncTime.Add(-time.Minute)).Scan(&userID); err != nil {
			t.Fatalf("insert expiry batch member %d: %v", index, err)
		}
		if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, 'https://auth.example.test/application/o/gotth-bb/', $2)`, userID, subject); err != nil {
			t.Fatalf("insert expiry batch identity %d: %v", index, err)
		}
	}
	gateway.userState = func(subject string) authentikgateway.UserState {
		return authentikgateway.UserState{User: authentikgateway.User{UUID: subject, Active: true}, Accepted: true}
	}
	firstBatchRandom := "0000000000000001" + "0000000000000002" + "0000000000000003" + "0000000000000004" + "0000000000000005"
	firstBatch, err := ReconcileExpiredSuspensions(ctx, connection, gateway, syncClock, strings.NewReader(firstBatchRandom))
	if err != nil || firstBatch != (ExpiryReconciliationResult{Claimed: 5, Completed: 5}) {
		t.Fatalf("first bounded expiry batch = (%+v, %v)", firstBatch, err)
	}
	secondBatch, err := ReconcileExpiredSuspensions(ctx, connection, gateway, syncClock, strings.NewReader("0000000000000006"))
	if err != nil || secondBatch != (ExpiryReconciliationResult{Claimed: 1, Completed: 1}) {
		t.Fatalf("second bounded expiry batch = (%+v, %v)", secondBatch, err)
	}
	emptyBatch, err := ReconcileExpiredSuspensions(ctx, connection, gateway, syncClock, strings.NewReader(strings.Repeat("x", 16)))
	if err != nil || emptyBatch != (ExpiryReconciliationResult{}) {
		t.Fatalf("empty expiry batch = (%+v, %v)", emptyBatch, err)
	}
	gateway.userState = nil
	adoptionUser := authentikgateway.User{ID: 105, UUID: "55555555-5555-4555-8555-555555555555", Username: "orphan", Name: "Recovered Member", Email: "recovered@example.test", Active: true}
	gateway.state = authentikgateway.UserState{User: adoptionUser, Pending: true}
	adoptionHandle, err := issueAdoptionHandle(baseTime, key, adoptionUser.ID, adoptionUser.UUID)
	if err != nil {
		t.Fatal(err)
	}
	adoptionRequest := pgtype.UUID{Bytes: [16]byte{0xd4}, Valid: true}
	adopted, err := Adopt(ctx, connection, gateway, clock, actor, AdoptionInput{Handle: adoptionHandle, Reason: "Recover verified pending identity", RequestID: adoptionRequest}, key)
	if err != nil || !adopted.Inserted || adopted.RegistrationID <= 0 || adopted.Revision != 1 || adopted.AuditID <= 0 {
		t.Fatalf("adoption = (%+v, %v)", adopted, err)
	}
	replayedAdoption, err := Adopt(ctx, connection, gateway, clock, actor, AdoptionInput{Handle: adoptionHandle, Reason: "Recover verified pending identity", RequestID: adoptionRequest}, key)
	if err != nil || replayedAdoption.Inserted || replayedAdoption.RegistrationID != adopted.RegistrationID || replayedAdoption.Revision != 1 || replayedAdoption.AuditID != 0 {
		t.Fatalf("adoption replay = (%+v, %v)", replayedAdoption, err)
	}
	var adoptionAuditCount int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions WHERE action_type = 'adopt_pending_registration'`).Scan(&adoptionAuditCount); err != nil || adoptionAuditCount != 1 {
		t.Fatalf("adoption audit count = (%d, %v)", adoptionAuditCount, err)
	}
	intakeSubject := "44444444-4444-4444-8444-444444444444"
	intake := Intake{AuthentikUserID: 104, Subject: intakeSubject, Username: "intake", DisplayName: "Intake Member", VerifiedEmail: "intake@example.test"}
	if err := AcceptIntake(ctx, pgxIntakeStore{connection}, clock, intake); err != nil {
		t.Fatalf("accept intake: %v", err)
	}
	intake.DisplayName = "Refreshed Member"
	if err := AcceptIntake(ctx, pgxIntakeStore{connection}, clock, intake); err != nil {
		t.Fatalf("refresh pending intake: %v", err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.pending_registrations
		SET status = 'rejected', decided_at = $1, deciding_administrator_id = $2
		WHERE authentik_subject = $3`, baseTime, administratorID, intakeSubject); err != nil {
		t.Fatalf("make intake terminal: %v", err)
	}
	intake.DisplayName = "Must Not Reopen"
	if err := AcceptIntake(ctx, pgxIntakeStore{connection}, clock, intake); err != nil {
		t.Fatalf("observe terminal intake: %v", err)
	}
	var terminalName, terminalStatus string
	if err := connection.QueryRow(ctx, `SELECT display_name, status FROM public.pending_registrations WHERE authentik_subject = $1`, intakeSubject).Scan(&terminalName, &terminalStatus); err != nil || terminalName != "Refreshed Member" || terminalStatus != "rejected" {
		t.Fatalf("terminal intake = (%q, %q, %v)", terminalName, terminalStatus, err)
	}
	intake.AuthentikUserID = 999
	if err := AcceptIntake(ctx, pgxIntakeStore{connection}, clock, intake); err == nil {
		t.Fatal("coordinate-conflicting intake accepted")
	}
	gateway.operations = nil

	approveSubject := "11111111-1111-4111-8111-111111111111"
	approveID := insertPendingRegistration(t, ctx, connection, 101, approveSubject, "Approve Me", "approve@example.test")
	page, err := ListPending(ctx, db.New(connection), actor, baseTime, 0)
	if err != nil || len(page.Registrations) != 2 || page.Registrations[1].ID != approveID || page.Registrations[1].DisplayName != "Approve Me" || page.Registrations[1].VerifiedEmail != "approve@example.test" || page.Registrations[1].Status != "pending" || page.Registrations[1].Revision != 1 {
		t.Fatalf("pending list = (%+v, %v)", page, err)
	}
	approveRequest := pgtype.UUID{Bytes: [16]byte{0xa1}, Valid: true}
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 101, UUID: approveSubject, Username: "approve", Name: "Approve Me", Email: "approve@example.test", Active: true}, Accepted: true}
	approved, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: approveID, Revision: 1, Decision: Approve,
		Reason: "Verified applicant", RequestID: approveRequest,
	}, key)
	if err != nil || approved.Status != "approved" || approved.Revision != 3 || approved.AuditID <= 0 {
		t.Fatalf("approve = (%+v, %v)", approved, err)
	}
	wantApproval := []string{"add:accepted:" + approveSubject, "remove:pending:" + approveSubject}
	if strings.Join(gateway.operations, "|") != strings.Join(wantApproval, "|") {
		t.Fatalf("approval operations = %v", gateway.operations)
	}
	beforeRetry := len(gateway.operations)
	retried, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: approveID, Revision: 1, Decision: Approve,
		Reason: "Verified applicant", RequestID: approveRequest,
	}, key)
	if err != nil || retried.Status != "approved" || retried.Revision != 3 || len(gateway.operations) != beforeRetry {
		t.Fatalf("terminal retry = (%+v, %v, operations %v)", retried, err, gateway.operations)
	}

	rejectSubject := "22222222-2222-4222-8222-222222222222"
	rejectID := insertPendingRegistration(t, ctx, connection, 102, rejectSubject, "Reject Me", "reject@example.test")
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 102, UUID: rejectSubject, Username: "reject", Name: "Reject Me", Email: "reject@example.test", Active: true}}
	rejected, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: rejectID, Revision: 1, Decision: Reject,
		Reason: "Application rejected", RequestID: pgtype.UUID{Bytes: [16]byte{0xb2}, Valid: true},
	}, key)
	if err != nil || rejected.Status != "rejected" || rejected.Revision != 3 {
		t.Fatalf("reject = (%+v, %v)", rejected, err)
	}
	lastTwo := gateway.operations[len(gateway.operations)-2:]
	wantRejection := []string{"remove:accepted:" + rejectSubject, "remove:pending:" + rejectSubject}
	if strings.Join(lastTwo, "|") != strings.Join(wantRejection, "|") {
		t.Fatalf("rejection operations = %v", lastTwo)
	}

	retrySubject := "33333333-3333-4333-8333-333333333333"
	retryID := insertPendingRegistration(t, ctx, connection, 103, retrySubject, "Retry Me", "retry@example.test")
	retryRequest := pgtype.UUID{Bytes: [16]byte{0xc3}, Valid: true}
	gateway.failNext = authentikgateway.ErrRemoteUnavailable
	failed, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: retryID, Revision: 1, Decision: Approve,
		Reason: "Retry a bounded failure", RequestID: retryRequest,
	}, key)
	if !errors.Is(err, ErrRemote) || failed != (DecisionResult{}) {
		t.Fatalf("remote failure = (%+v, %v)", failed, err)
	}
	assertRegistrationState(t, ctx, connection, retryID, "approval_required", 3, "remote_unavailable")
	gateway.state = authentikgateway.UserState{User: authentikgateway.User{ID: 103, UUID: retrySubject, Username: "retry", Name: "Retry Me", Email: "retry@example.test", Active: true}, Accepted: true}
	recovered, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: retryID, Revision: 1, Decision: Approve,
		Reason: "Retry a bounded failure", RequestID: retryRequest,
	}, key)
	if err != nil || recovered.Status != "approved" || recovered.Revision != 4 {
		t.Fatalf("recovered decision = (%+v, %v)", recovered, err)
	}

	for _, forbidden := range []string{"approve@example.test", "Approve Me", failedInvitationInput.Email, failedInvitationInput.DisplayName, adoptedInvitationUUID, approveSubject, controlledSubject, actorRaceSubject, removalSubject, "authentik_user_id"} {
		var count int
		if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions
			WHERE previous_state::text LIKE '%' || $1 || '%' OR resulting_state::text LIKE '%' || $1 || '%'`, forbidden).Scan(&count); err != nil || count != 0 {
			t.Fatalf("audit leaked %q: count=%d error=%v", forbidden, count, err)
		}
	}
	var actionCount int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions
		WHERE action_type IN ('request_registration_approval', 'approve_registration',
		'request_registration_rejection', 'reject_registration', 'record_registration_transition_result')`).Scan(&actionCount); err != nil || actionCount != 7 {
		t.Fatalf("decision audit count = (%d, %v), want 7", actionCount, err)
	}
}

func decisionTestConnection(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+decisionTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+decisionTestDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+decisionTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = decisionTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	return connection
}

func insertPendingRegistration(t *testing.T, ctx context.Context, connection *pgx.Conn, remoteID int64, subject, name, email string) int64 {
	t.Helper()
	var id int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.pending_registrations
		(authentik_user_id, authentik_subject, display_name, verified_email, intake_at)
		VALUES ($1, $2, $3, $4, '2026-09-09T17:00:00Z') RETURNING id`, remoteID, subject, name, email).Scan(&id); err != nil {
		t.Fatalf("insert pending registration %s: %v", subject, err)
	}
	return id
}

func assertRegistrationState(t *testing.T, ctx context.Context, connection *pgx.Conn, id int64, status string, revision int64, failure string) {
	t.Helper()
	var gotStatus, gotFailure string
	var gotRevision int64
	if err := connection.QueryRow(ctx, `SELECT status, administration_revision, reconciliation_class
		FROM public.pending_registrations WHERE id = $1`, id).Scan(&gotStatus, &gotRevision, &gotFailure); err != nil || gotStatus != status || gotRevision != revision || gotFailure != failure {
		t.Fatalf("registration state = (%q, %d, %q, %v), want (%q, %d, %q)", gotStatus, gotRevision, gotFailure, err, status, revision, failure)
	}
}

var _ Gateway = (*recordingGateway)(nil)

type pgxIntakeStore struct{ connection *pgx.Conn }

func (store pgxIntakeStore) UpsertPendingRegistrationIntake(ctx context.Context, params db.UpsertPendingRegistrationIntakeParams) (bool, error) {
	return db.New(store.connection).UpsertPendingRegistrationIntake(ctx, params)
}

var _ IntakeStore = pgxIntakeStore{}
