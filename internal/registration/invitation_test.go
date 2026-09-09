package registration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type invitationQueryStub struct {
	rows []db.ListRegistrationInvitationsForAdministrationRow
	err  error
}

func (stub *invitationQueryStub) ListRegistrationInvitationsForAdministration(context.Context, db.ListRegistrationInvitationsForAdministrationParams) ([]db.ListRegistrationInvitationsForAdministrationRow, error) {
	return stub.rows, stub.err
}

type invitationGatewayStub struct {
	invitations []authentikgateway.Invitation
	more        bool
	listErr     error
	retrieve    authentikgateway.Invitation
	retrieveErr error
	deleteErr   error
}

func (*invitationGatewayStub) AddUser(context.Context, string, string) error    { return nil }
func (*invitationGatewayStub) RemoveUser(context.Context, string, string) error { return nil }
func (*invitationGatewayStub) PendingUsers(context.Context) ([]authentikgateway.User, bool, error) {
	return nil, false, nil
}
func (*invitationGatewayStub) User(context.Context, string) (authentikgateway.UserState, error) {
	return authentikgateway.UserState{}, nil
}
func (*invitationGatewayStub) CreateInvitation(context.Context, string, string, string, string) (authentikgateway.Invitation, error) {
	return authentikgateway.Invitation{}, nil
}
func (stub *invitationGatewayStub) Invitations(context.Context) ([]authentikgateway.Invitation, bool, error) {
	return stub.invitations, stub.more, stub.listErr
}
func (stub *invitationGatewayStub) Invitation(context.Context, string) (authentikgateway.Invitation, error) {
	return stub.retrieve, stub.retrieveErr
}
func (stub *invitationGatewayStub) DeleteInvitation(context.Context, string) error {
	return stub.deleteErr
}

func TestInvitationListReconcilesRemoteAndIssuesFiniteHandle(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	id := pgtype.UUID{Bytes: [16]byte{0x17}, Valid: true}
	key := [32]byte{0x51}
	queries := &invitationQueryStub{rows: []db.ListRegistrationInvitationsForAdministrationRow{{
		ActorPresent: true, InvitationPresent: true, IdempotencyKey: id,
		InvitationName: "gotth-bb-invitation", TransitionState: "active", DeliveryState: "queued",
		ExpiresAt: finiteTime(expires), CreatedAt: finiteTime(now), AdministrationRevision: 2,
	}}}
	remote := &invitationGatewayStub{invitations: []authentikgateway.Invitation{{
		UUID: "66666666-6666-4666-8666-666666666666", Name: "gotth-bb-invitation",
		Expires: expires.Format(time.RFC3339), SingleUse: true,
	}}}
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	page, err := ListInvitations(context.Background(), queries, remote, actor, now, key)
	if err != nil || len(page.Invitations) != 1 || page.Invitations[0].Handle == "" || page.Invitations[0].Status != "active" || page.More {
		t.Fatalf("invitation page = (%+v, %v)", page, err)
	}
	gotID, revision, err := verifyInvitationHandle(page.Invitations[0].Handle, now, key)
	if err != nil || gotID != id || revision != 2 {
		t.Fatalf("invitation handle = (%+v, %d, %v)", gotID, revision, err)
	}

	remote.invitations[0].Expires = expires.Add(time.Minute).Format(time.RFC3339)
	if _, err := ListInvitations(context.Background(), queries, remote, actor, now, key); !errors.Is(err, ErrRemote) {
		t.Fatalf("mismatched remote expiry error = %v", err)
	}
	remote.invitations[0].Expires = expires.Format(time.RFC3339)
	queries.rows[0].TransitionState = "revoked"
	if _, err := ListInvitations(context.Background(), queries, remote, actor, now, key); !errors.Is(err, ErrRemote) {
		t.Fatalf("terminal remote error = %v", err)
	}
	queries.rows[0].TransitionState = "active"
	remote.invitations = append(remote.invitations, authentikgateway.Invitation{UUID: "77777777-7777-4777-8777-777777777777", Name: "untracked", Expires: expires.Format(time.RFC3339), SingleUse: true})
	if _, err := ListInvitations(context.Background(), queries, remote, actor, now, key); !errors.Is(err, ErrRemote) {
		t.Fatalf("untracked remote error = %v", err)
	}
}

func TestInvitationHandleRejectsTamperingAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	key := [32]byte{0x61}
	id := pgtype.UUID{Bytes: [16]byte{0x31}, Valid: true}
	handle, err := issueInvitationHandle(now, key, id, 9)
	if err != nil {
		t.Fatal(err)
	}
	tampered := handle[:len(handle)-1] + "B"
	if _, _, err := verifyInvitationHandle(tampered, now, key); err == nil {
		t.Fatal("tampered invitation handle accepted")
	}
	if _, _, err := verifyInvitationHandle(handle, now.Add(invitationHandleLifetime+time.Second), key); err == nil {
		t.Fatal("expired invitation handle accepted")
	}
}

func TestRemoteInvitationRevocationClassifiesConfirmedAbsentAndAmbiguous(t *testing.T) {
	expires := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
	present := authentikgateway.Invitation{UUID: "66666666-6666-4666-8666-666666666666", Name: "gotth-bb-invitation", Expires: expires.Format(time.RFC3339), SingleUse: true}
	for _, test := range []struct {
		name                  string
		gateway               *invitationGatewayStub
		wantState, wantResult string
		wantFailure           string
	}{
		{name: "confirmed", gateway: &invitationGatewayStub{invitations: []authentikgateway.Invitation{present}, retrieveErr: authentikgateway.ErrAbsentObject}, wantState: "revoked", wantResult: "confirmed"},
		{name: "already absent", gateway: &invitationGatewayStub{}, wantState: "absent", wantResult: "already_absent"},
		{name: "unbounded absence", gateway: &invitationGatewayStub{more: true}, wantState: "unknown", wantResult: "operator_required", wantFailure: "operator_required"},
		{name: "delete unavailable", gateway: &invitationGatewayStub{invitations: []authentikgateway.Invitation{present}, deleteErr: authentikgateway.ErrRemoteUnavailable}, wantState: "unknown", wantResult: "remote_unavailable", wantFailure: "remote_unavailable"},
		{name: "readback present", gateway: &invitationGatewayStub{invitations: []authentikgateway.Invitation{present}, retrieve: present}, wantState: "unknown", wantResult: "remote_conflict", wantFailure: "remote_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, result, failure := revokeRemoteInvitation(context.Background(), test.gateway, present.Name, expires)
			if state != test.wantState || result != test.wantResult || failure != test.wantFailure {
				t.Fatalf("classification = (%q, %q, %q)", state, result, failure)
			}
		})
	}
}
