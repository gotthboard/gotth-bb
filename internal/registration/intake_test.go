package registration

import (
	"context"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
)

type captureIntakeStore struct {
	params db.UpsertPendingRegistrationIntakeParams
	called bool
}

func (store *captureIntakeStore) UpsertPendingRegistrationIntake(_ context.Context, params db.UpsertPendingRegistrationIntakeParams) (bool, error) {
	store.params = params
	store.called = true
	return true, nil
}

func TestAcceptIntakePersistsClosedProjection(t *testing.T) {
	store := &captureIntakeStore{}
	now := time.Date(2026, 9, 9, 19, 0, 0, 123456789, time.UTC)
	intake := Intake{
		AuthentikUserID: 17, Subject: "77777777-7777-4777-8777-777777777777",
		Username: "member", DisplayName: "Member", VerifiedEmail: "member@example.test",
	}
	if err := AcceptIntake(context.Background(), store, func() time.Time { return now }, intake); err != nil {
		t.Fatal(err)
	}
	if store.params.AuthentikUserID != 17 || store.params.AuthentikSubject.Bytes[0] != 0x77 ||
		store.params.DisplayName != "Member" || store.params.VerifiedEmail != "member@example.test" ||
		!store.params.IntakeAt.Time.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("stored intake = %+v", store.params)
	}
}

func TestAcceptIntakeRejectsInvalidProjectionBeforeStore(t *testing.T) {
	valid := Intake{AuthentikUserID: 17, Subject: "77777777-7777-4777-8777-777777777777", Username: "member", DisplayName: "Member", VerifiedEmail: "member@example.test"}
	tests := []Intake{
		{},
		{AuthentikUserID: 17, Subject: "NOT-A-UUID", Username: "member", DisplayName: "Member", VerifiedEmail: "member@example.test"},
		{AuthentikUserID: 17, Subject: valid.Subject, Username: " member", DisplayName: "Member", VerifiedEmail: "member@example.test"},
		{AuthentikUserID: 17, Subject: valid.Subject, Username: "member", DisplayName: "Member\nName", VerifiedEmail: "member@example.test"},
		{AuthentikUserID: 17, Subject: valid.Subject, Username: "member", DisplayName: "Member", VerifiedEmail: "Member <member@example.test>"},
	}
	for _, intake := range tests {
		store := &captureIntakeStore{}
		if err := AcceptIntake(context.Background(), store, time.Now, intake); err == nil || store.called {
			t.Fatalf("invalid intake accepted: %+v", intake)
		}
	}
}

var _ IntakeStore = (*captureIntakeStore)(nil)
