package registration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type pendingQueryStub struct {
	rows   []db.ListPendingRegistrationsForAdministrationRow
	err    error
	params db.ListPendingRegistrationsForAdministrationParams
}

func (stub *pendingQueryStub) ListPendingRegistrationsForAdministration(_ context.Context, params db.ListPendingRegistrationsForAdministrationParams) ([]db.ListPendingRegistrationsForAdministrationRow, error) {
	stub.params = params
	return stub.rows, stub.err
}

func TestListPendingReturnsBoundedValidatedPage(t *testing.T) {
	observed := time.Date(2026, 9, 9, 19, 15, 0, 987654321, time.UTC)
	intake := time.Date(2026, 9, 9, 19, 0, 0, 123456789, time.UTC)
	rows := make([]db.ListPendingRegistrationsForAdministrationRow, 51)
	for index := range rows {
		rows[index] = db.ListPendingRegistrationsForAdministrationRow{
			RegistrationPresent: true, ID: int64(index + 11), DisplayName: "Pending Member",
			VerifiedEmail: "pending@example.test", Status: "pending", AdministrationRevision: 1,
			IntakeAt: pgtype.Timestamptz{Time: intake, Valid: true},
		}
	}
	stub := &pendingQueryStub{rows: rows}
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	page, err := ListPending(context.Background(), stub, actor, observed, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Registrations) != 50 || page.NextAfter != 60 || page.Registrations[0].ID != 11 || !page.Registrations[0].IntakeAt.Equal(intake.Truncate(time.Microsecond)) {
		t.Fatalf("pending page = %+v", page)
	}
	if stub.params.ActorUserID != 7 || stub.params.AfterRegistrationID != 10 || stub.params.PageLimit != 51 || !stub.params.ObservedAt.Time.Equal(observed.Truncate(time.Microsecond)) {
		t.Fatalf("query params = %+v", stub.params)
	}
}

func TestListPendingEmptyDeniedAndMalformedResults(t *testing.T) {
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	observed := time.Now()
	empty, err := ListPending(context.Background(), &pendingQueryStub{rows: []db.ListPendingRegistrationsForAdministrationRow{{}}}, actor, observed, 0)
	if err != nil || len(empty.Registrations) != 0 {
		t.Fatalf("empty page = (%+v, %v)", empty, err)
	}
	if _, err := ListPending(context.Background(), &pendingQueryStub{}, actor, observed, 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing actor error = %v", err)
	}
	bad := db.ListPendingRegistrationsForAdministrationRow{
		RegistrationPresent: true, ID: 1, DisplayName: "Pending", VerifiedEmail: "Name <pending@example.test>",
		Status: "pending", AdministrationRevision: 1, IntakeAt: pgtype.Timestamptz{Time: observed, Valid: true},
	}
	if _, err := ListPending(context.Background(), &pendingQueryStub{rows: []db.ListPendingRegistrationsForAdministrationRow{bad}}, actor, observed, 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("malformed row error = %v", err)
	}
	if _, err := ListPending(context.Background(), &pendingQueryStub{err: errors.New("database")}, actor, observed, 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("query error = %v", err)
	}
}

func TestListPendingRejectsBoundaryBeforeQuery(t *testing.T) {
	stub := &pendingQueryStub{}
	member := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleMember}
	for _, test := range []struct {
		ctx      context.Context
		actor    policy.AccessContext
		observed time.Time
		after    int64
	}{
		{context.Background(), member, time.Now(), 0},
		{context.Background(), policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}, time.Time{}, 0},
		{context.Background(), policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}, time.Now(), -1},
	} {
		if _, err := ListPending(test.ctx, stub, test.actor, test.observed, test.after); err == nil {
			t.Fatal("invalid list boundary accepted")
		}
	}
	if stub.params != (db.ListPendingRegistrationsForAdministrationParams{}) {
		t.Fatalf("query called for invalid boundary: %+v", stub.params)
	}
}

var _ pendingQuerier = (*pendingQueryStub)(nil)
