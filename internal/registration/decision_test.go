package registration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type panicBeginner struct{}

func (panicBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	panic("transaction must not begin")
}

type panicGateway struct{}

func (panicGateway) AddUser(context.Context, string, string) error {
	panic("gateway must not be called")
}
func (panicGateway) RemoveUser(context.Context, string, string) error {
	panic("gateway must not be called")
}

func TestDecideRejectsInvalidBoundaryBeforeSideEffects(t *testing.T) {
	actor := policy.AccessContext{Authenticated: true, UserID: 1, Role: policy.RoleAdministrator}
	valid := DecisionInput{
		RegistrationID: 1, Revision: 1, Decision: Approve, Reason: "Verified applicant",
		RequestID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	clock := func() time.Time { return time.Date(2026, 9, 9, 18, 0, 0, 0, time.UTC) }
	key := [32]byte{1}
	tests := []struct {
		name  string
		ctx   context.Context
		actor policy.AccessContext
		input DecisionInput
		key   [32]byte
	}{
		{"nil context", nil, actor, valid, key},
		{"anonymous", context.Background(), policy.AccessContext{}, valid, key},
		{"bad registration", context.Background(), actor, DecisionInput{Revision: 1, Decision: Approve, Reason: valid.Reason, RequestID: valid.RequestID}, key},
		{"bad revision", context.Background(), actor, DecisionInput{RegistrationID: 1, Decision: Approve, Reason: valid.Reason, RequestID: valid.RequestID}, key},
		{"bad decision", context.Background(), actor, DecisionInput{RegistrationID: 1, Revision: 1, Reason: valid.Reason, RequestID: valid.RequestID}, key},
		{"bad reason", context.Background(), actor, DecisionInput{RegistrationID: 1, Revision: 1, Decision: Approve, Reason: " bad", RequestID: valid.RequestID}, key},
		{"bad request ID", context.Background(), actor, DecisionInput{RegistrationID: 1, Revision: 1, Decision: Approve, Reason: valid.Reason}, key},
		{"zero key", context.Background(), actor, valid, [32]byte{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Decide(test.ctx, panicBeginner{}, panicGateway{}, clock, test.actor, test.input, test.key)
			if err == nil || result != (DecisionResult{}) {
				t.Fatalf("Decide() = (%+v, %v), want zero/error", result, err)
			}
		})
	}
}

func TestRegistrationReferenceIsDomainSeparatedAndStable(t *testing.T) {
	key := [32]byte{0x42}
	first := registrationReference(key, 17)
	if first == "" || first != registrationReference(key, 17) || first == registrationReference(key, 18) || first == registrationReference([32]byte{0x43}, 17) {
		t.Fatalf("invalid registration reference behavior: %q", first)
	}
}

func TestRemoteFailureClassIsClosed(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{authentikgateway.ErrRemoteUnavailable, "remote_unavailable"},
		{authentikgateway.ErrRemoteConflict, "remote_conflict"},
		{authentikgateway.ErrAbsentObject, "remote_absent"},
		{authentikgateway.ErrRemoteInvalid, "remote_invalid"},
		{errors.New("secret remote detail"), "remote_failure"},
	}
	for _, test := range tests {
		if got := remoteFailureClass(test.err); got != test.want {
			t.Fatalf("remoteFailureClass(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

var _ transactionBeginner = panicBeginner{}
var _ Gateway = panicGateway{}
