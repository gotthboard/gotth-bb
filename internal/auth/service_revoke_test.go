package auth

import (
	"context"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

func TestServiceRevokeSessionRejectsUninitializedService(t *testing.T) {
	t.Parallel()

	database := &constructorSessionDatabase{}
	queries := db.New(database)
	for _, test := range []struct {
		name    string
		service *Service
	}{
		{name: "nil"},
		{name: "zero", service: &Service{}},
		{name: "database", service: &Service{queries: queries, clock: time.Now}},
		{name: "queries", service: &Service{database: database, clock: time.Now}},
		{name: "clock", service: &Service{database: database, queries: queries}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if revoked, err := test.service.RevokeSession(context.Background(), "token"); err == nil || revoked {
				t.Fatalf("RevokeSession() = (%t, %v), want false/error", revoked, err)
			}
		})
	}
}

func TestServiceRevokeSessionTreatsInvalidCookieAsNoOpWithoutDatabase(t *testing.T) {
	t.Parallel()

	database := &constructorSessionDatabase{}
	service := &Service{database: database, queries: db.New(database), clock: time.Now}
	if revoked, err := service.RevokeSession(context.Background(), "invalid"); err != nil || revoked {
		t.Fatalf("RevokeSession() = (%t, %v), want false/nil", revoked, err)
	}
}
