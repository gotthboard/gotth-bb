package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
)

func TestServiceAuthenticateSessionRejectsUninitializedService(t *testing.T) {
	t.Parallel()

	database := &constructorSessionDatabase{}
	queries := db.New(database)
	for _, test := range []struct {
		name    string
		service *Service
	}{
		{name: "nil"},
		{name: "zero", service: &Service{}},
		{name: "missing database", service: &Service{queries: queries, clock: time.Now, sessionIdleTimeout: time.Hour, revalidationInterval: time.Hour}},
		{name: "missing queries", service: &Service{database: database, clock: time.Now, sessionIdleTimeout: time.Hour, revalidationInterval: time.Hour}},
		{name: "missing clock", service: &Service{database: database, queries: queries, sessionIdleTimeout: time.Hour, revalidationInterval: time.Hour}},
		{name: "short idle timeout", service: &Service{database: database, queries: queries, clock: time.Now, sessionIdleTimeout: time.Second - time.Nanosecond, revalidationInterval: time.Hour}},
		{name: "short revalidation interval", service: &Service{database: database, queries: queries, clock: time.Now, sessionIdleTimeout: time.Hour, revalidationInterval: time.Second - time.Nanosecond}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := test.service.AuthenticateSession(context.Background(), "token")
			if err == nil || !reflect.DeepEqual(got, SessionAuthentication{}) {
				t.Fatalf("AuthenticateSession() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
}

func TestServiceAuthenticateSessionTreatsInvalidCookieAsAnonymousWithoutDatabase(t *testing.T) {
	t.Parallel()

	database := &constructorSessionDatabase{}
	service := &Service{
		database: database, queries: db.New(database), clock: time.Now,
		sessionIdleTimeout: time.Hour, revalidationInterval: time.Hour,
	}
	got, err := service.AuthenticateSession(context.Background(), "invalid")
	if err != nil || !reflect.DeepEqual(got, SessionAuthentication{}) {
		t.Fatalf("AuthenticateSession() = (%+v, %v), want anonymous", got, err)
	}
}
