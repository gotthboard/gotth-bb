package httpui

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/auth"
)

func TestSessionAuthenticationFromContextReturnsStoredSnapshot(t *testing.T) {
	t.Parallel()

	mutedUntil := time.Date(2026, time.September, 1, 15, 0, 0, 0, time.UTC)
	want := auth.SessionAuthentication{
		Access: auth.AccessContext{
			Authenticated: true,
			UserID:        42,
			Role:          auth.RoleModerator,
			GroupIDs:      []int64{3, 7},
			MutedUntil:    &mutedUntil,
			ValidatedAt:   mutedUntil.Add(-time.Hour),
		},
		RequiresRevalidation: true,
	}
	ctx := context.WithValue(context.Background(), sessionAuthenticationContextKey{}, want)
	if got := sessionAuthenticationFromContext(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionAuthenticationFromContext() = %+v, want %+v", got, want)
	}
}

func TestSessionAuthenticationFromContextDefaultsToAnonymous(t *testing.T) {
	t.Parallel()

	for _, ctx := range []context.Context{
		nil,
		context.Background(),
		context.WithValue(context.Background(), sessionAuthenticationContextKey{}, "wrong type"),
	} {
		if got := sessionAuthenticationFromContext(ctx); !reflect.DeepEqual(got, auth.SessionAuthentication{}) {
			t.Fatalf("sessionAuthenticationFromContext() = %+v, want anonymous", got)
		}
	}
}
