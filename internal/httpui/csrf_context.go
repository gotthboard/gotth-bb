package httpui

import "context"

type csrfTokenContextKey struct{}

// csrfTokenFromContext returns the request-scoped synchronizer token derived by
// the session boundary. Missing, nil, and malformed context state is empty so a
// mutation validator fails closed rather than inventing CSRF authority.
//
// Complexity: the context lookup and type assertion are tight Theta(1) time
// and auxiliary space; the returned string is an immutable header copy.
func csrfTokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(csrfTokenContextKey{}).(string)
	return token
}
