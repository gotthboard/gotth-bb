package httpui

import (
	"context"
	"testing"
)

func TestCSRFTokenFromContextReturnsStoredToken(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), csrfTokenContextKey{}, "csrf-token")
	if got := csrfTokenFromContext(ctx); got != "csrf-token" {
		t.Fatalf("csrfTokenFromContext() = %q", got)
	}
}

func TestCSRFTokenFromContextDefaultsToEmpty(t *testing.T) {
	t.Parallel()

	for _, ctx := range []context.Context{
		nil,
		context.Background(),
		context.WithValue(context.Background(), csrfTokenContextKey{}, 42),
	} {
		if got := csrfTokenFromContext(ctx); got != "" {
			t.Fatalf("csrfTokenFromContext() = %q, want empty", got)
		}
	}
}
