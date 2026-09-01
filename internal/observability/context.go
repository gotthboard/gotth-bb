package observability

import "context"

type requestIDContextKey struct{}

// RequestID returns the server-generated request identifier from context.
//
// Complexity: for context-chain depth d, time O(d), Omega(1), and tight
// Theta(d) when the key is absent or deepest; auxiliary space O(1), Omega(1),
// and tight Theta(1). context.Context owns the delegated chain traversal.
func RequestID(contextWithID context.Context) (string, bool) {
	requestID, ok := contextWithID.Value(requestIDContextKey{}).(string)
	return requestID, ok
}
