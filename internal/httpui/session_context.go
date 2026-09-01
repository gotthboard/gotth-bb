package httpui

import (
	"context"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
)

type sessionAuthenticationContextKey struct{}

// sessionAuthenticationFromContext returns the authentication snapshot placed
// on one request by the session boundary. Missing and malformed context state
// is the exact anonymous zero value; callers never invent a synthetic user.
//
// Complexity: the context lookup and type assertion are tight Theta(1) time
// and auxiliary space. The returned snapshot is a shallow value copy whose
// GroupIDs slice, when populated, remains immutable request-scoped data.
func sessionAuthenticationFromContext(ctx context.Context) auth.SessionAuthentication {
	if ctx == nil {
		return auth.SessionAuthentication{}
	}
	authentication, _ := ctx.Value(sessionAuthenticationContextKey{}).(auth.SessionAuthentication)
	return authentication
}
