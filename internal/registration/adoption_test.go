package registration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
)

type adoptionGatewayStub struct {
	users []authentikgateway.User
	more  bool
	err   error
	state authentikgateway.UserState
}

func (stub *adoptionGatewayStub) PendingUsers(context.Context) ([]authentikgateway.User, bool, error) {
	return stub.users, stub.more, stub.err
}
func (stub *adoptionGatewayStub) User(context.Context, string) (authentikgateway.UserState, error) {
	return stub.state, stub.err
}
func (*adoptionGatewayStub) AddUser(context.Context, string, string) error    { return nil }
func (*adoptionGatewayStub) RemoveUser(context.Context, string, string) error { return nil }
func (*adoptionGatewayStub) CreateInvitation(context.Context, string, string, string, string) (authentikgateway.Invitation, error) {
	return authentikgateway.Invitation{}, nil
}
func (*adoptionGatewayStub) Invitations(context.Context) ([]authentikgateway.Invitation, bool, error) {
	return nil, false, nil
}
func (*adoptionGatewayStub) Invitation(context.Context, string) (authentikgateway.Invitation, error) {
	return authentikgateway.Invitation{}, nil
}
func (*adoptionGatewayStub) DeleteInvitation(context.Context, string) error { return nil }

func TestPendingAdministrationIssuesOnlyUnmatchedAdoptionHandles(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	key := [32]byte{0x61}
	user := authentikgateway.User{ID: 17, UUID: "77777777-7777-4777-8777-777777777777", Username: "pending", Name: "Pending Member", Email: "pending@example.test", Active: true}
	queries := &pendingQueryStub{
		rows:    []db.ListPendingRegistrationsForAdministrationRow{{}},
		matches: []db.MatchPendingRegistrationCoordinatesRow{{ActorPresent: true}},
	}
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	page, err := ListPendingAdministration(context.Background(), queries, &adoptionGatewayStub{users: []authentikgateway.User{user}, more: true}, actor, now, 0, key)
	if err != nil || len(page.Orphans) != 1 || page.Orphans[0].DisplayName != "Pending Member" || page.Orphans[0].VerifiedEmail != "pending@example.test" || !page.RemoteMore || page.RemoteUnavailable {
		t.Fatalf("orphan page = (%+v, %v)", page, err)
	}
	userID, subject, err := verifyAdoptionHandle(page.Orphans[0].Handle, now, key)
	if err != nil || userID != 17 || subject != user.UUID || len(queries.matchParams.AuthentikUserIds) != 1 || queries.matchParams.AuthentikUserIds[0] != 17 {
		t.Fatalf("adoption handle/query = (%d, %q, %v, %+v)", userID, subject, err, queries.matchParams)
	}

	queries.matches = []db.MatchPendingRegistrationCoordinatesRow{{ActorPresent: true, RegistrationPresent: true, ID: 3, AuthentikUserID: 17, AuthentikSubject: user.UUID}}
	page, err = ListPendingAdministration(context.Background(), queries, &adoptionGatewayStub{users: []authentikgateway.User{user}}, actor, now, 0, key)
	if err != nil || len(page.Orphans) != 0 || page.RemoteUnavailable {
		t.Fatalf("matched identity page = (%+v, %v)", page, err)
	}

	queries.matches[0].AuthentikSubject = "88888888-8888-4888-8888-888888888888"
	page, err = ListPendingAdministration(context.Background(), queries, &adoptionGatewayStub{users: []authentikgateway.User{user}}, actor, now, 0, key)
	if err != nil || !page.RemoteUnavailable || len(page.Orphans) != 0 {
		t.Fatalf("coordinate drift page = (%+v, %v)", page, err)
	}
}

func TestPendingAdministrationPreservesLocalPageOnRemoteFailure(t *testing.T) {
	now := time.Now()
	queries := &pendingQueryStub{rows: []db.ListPendingRegistrationsForAdministrationRow{{}}}
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	page, err := ListPendingAdministration(context.Background(), queries, &adoptionGatewayStub{err: errors.New("unavailable")}, actor, now, 0, [32]byte{1})
	if err != nil || !page.RemoteUnavailable || len(page.Registrations) != 0 {
		t.Fatalf("remote failure page = (%+v, %v)", page, err)
	}
}

func TestAdoptionHandleRejectsTamperAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	key := [32]byte{0x62}
	handle, err := issueAdoptionHandle(now, key, 17, "77777777-7777-4777-8777-777777777777")
	if err != nil {
		t.Fatal(err)
	}
	tampered := handle[:len(handle)-1] + "A"
	if _, _, err := verifyAdoptionHandle(tampered, now, key); err == nil {
		t.Fatal("tampered adoption handle accepted")
	}
	if _, _, err := verifyAdoptionHandle(handle, now.Add(adoptionHandleLifetime+time.Second), key); err == nil {
		t.Fatal("expired adoption handle accepted")
	}
	if _, _, err := verifyAdoptionHandle(handle, now, [32]byte{0x63}); err == nil {
		t.Fatal("wrong adoption key accepted")
	}
}

var _ ControlGateway = (*adoptionGatewayStub)(nil)
