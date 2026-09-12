package registration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/jackc/pgx/v5/pgtype"
)

type panicControlGateway struct{ panicGateway }

type stateControlGateway struct {
	panicControlGateway
	state authentikgateway.UserState
}

func (*stateControlGateway) AddUser(context.Context, string, string) error    { return nil }
func (*stateControlGateway) RemoveUser(context.Context, string, string) error { return nil }
func (gateway *stateControlGateway) User(context.Context, string) (authentikgateway.UserState, error) {
	return gateway.state, nil
}

func (panicControlGateway) PendingUsers(context.Context) ([]authentikgateway.User, bool, error) {
	panic("gateway must not be called")
}
func (panicControlGateway) CreateInvitation(context.Context, string, string, string, string) (authentikgateway.Invitation, error) {
	panic("gateway must not be called")
}
func (panicControlGateway) Invitations(context.Context) ([]authentikgateway.Invitation, bool, error) {
	panic("gateway must not be called")
}
func (panicControlGateway) Invitation(context.Context, string) (authentikgateway.Invitation, error) {
	panic("gateway must not be called")
}
func (panicControlGateway) DeleteInvitation(context.Context, string) error {
	panic("gateway must not be called")
}

func TestReconcileIdentityRejectsInvalidBoundaryBeforeSideEffects(t *testing.T) {
	actor := policy.AccessContext{Authenticated: true, UserID: 1, Role: policy.RoleAdministrator}
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	clock := func() time.Time { return time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC) }
	tests := []struct {
		name     string
		ctx      context.Context
		actor    policy.AccessContext
		targetID int64
		reason   string
		request  pgtype.UUID
	}{
		{name: "nil context", actor: actor, targetID: 2, reason: "Retry identity access", request: requestID},
		{name: "anonymous", ctx: context.Background(), targetID: 2, reason: "Retry identity access", request: requestID},
		{name: "self target", ctx: context.Background(), actor: actor, targetID: 1, reason: "Retry identity access", request: requestID},
		{name: "bad target", ctx: context.Background(), actor: actor, reason: "Retry identity access", request: requestID},
		{name: "bad reason", ctx: context.Background(), actor: actor, targetID: 2, reason: " padded ", request: requestID},
		{name: "bad request ID", ctx: context.Background(), actor: actor, targetID: 2, reason: "Retry identity access"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ReconcileIdentity(test.ctx, panicBeginner{}, panicControlGateway{}, clock, test.actor, test.targetID, test.reason, test.request)
			if err == nil || result != (IdentityReconciliationResult{}) {
				t.Fatalf("ReconcileIdentity() = (%+v, %v), want zero/error", result, err)
			}
		})
	}
}

func TestIdentityReconciliationRejectsWrongReadbackIdentity(t *testing.T) {
	const subject = "11111111-1111-4111-8111-111111111111"
	wrong := authentikgateway.User{UUID: "22222222-2222-4222-8222-222222222222", Active: true}
	for _, test := range []struct {
		name  string
		apply func(context.Context, ControlGateway, string) error
		state authentikgateway.UserState
	}{
		{name: "removal", apply: applyIdentityRemoval, state: authentikgateway.UserState{User: wrong, Suspended: true}},
		{name: "grant", apply: applyIdentityGrant, state: authentikgateway.UserState{User: wrong, Accepted: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.apply(context.Background(), &stateControlGateway{state: test.state}, subject)
			if !errors.Is(err, authentikgateway.ErrRemoteConflict) {
				t.Fatalf("identity reconciliation error = %v, want remote conflict", err)
			}
		})
	}
}

var _ ControlGateway = panicControlGateway{}
var _ ControlGateway = (*stateControlGateway)(nil)
