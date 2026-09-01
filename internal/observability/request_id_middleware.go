package observability

import (
	"context"
	"fmt"
	"net/http"
)

// RequestIDGenerator returns one lowercase 128-bit hexadecimal identifier.
type RequestIDGenerator func() (string, error)

// NewRequestIDMiddleware constructs a boundary that ignores caller-provided
// identifiers and fails closed unless a valid server identifier is available.
//
// Complexity: construction is tight Theta(1) time and auxiliary space. Per
// request, excluding generator and downstream costs, validation and context
// attachment are tight Theta(1) time and auxiliary space because the accepted
// identifier is fixed at 32 bytes; context.WithValue allocates one fixed node.
func NewRequestIDMiddleware(next http.Handler, generate RequestIDGenerator) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("request ID middleware requires a downstream handler")
	}
	if generate == nil {
		return nil, fmt.Errorf("request ID middleware requires a generator")
	}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestID, err := generate()
		if err != nil || len(requestID) != 32 {
			http.Error(response, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		for index := 0; index < len(requestID); index++ {
			character := requestID[index]
			if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
				http.Error(response, "service unavailable", http.StatusServiceUnavailable)
				return
			}
		}

		response.Header().Set("X-Request-ID", requestID)
		contextWithID := context.WithValue(request.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(response, request.WithContext(contextWithID))
	}), nil
}
