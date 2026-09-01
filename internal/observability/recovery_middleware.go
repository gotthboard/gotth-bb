package observability

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// NewRecoveryMiddleware logs a sanitized panic event and emits a bounded 500
// only while the response is uncommitted. A committed response is re-panicked
// with http.ErrAbortHandler so net/http quietly closes the connection without
// leaking the original panic value or appending a second response.
//
// Complexity: construction is tight Theta(1) time and auxiliary space. For p
// pre-application header map entries plus value slots, each request adds
// O(p+1), Omega(1), and tight Theta(p+1) time and auxiliary space for
// Header.Clone; cloned strings retain their bytes by reference. On panic, for f
// captured stack bytes, c current header-map bucket population, and d request
// context-chain depth, ordinary-panic recovery adds O(f+p+c+d+1) time and
// O(f+p+1) auxiliary space, excluding configured logger cost. Quiet-abort
// propagation performs no context lookup or stack capture.
func NewRecoveryMiddleware(next http.Handler, logger *slog.Logger) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("recovery middleware requires a downstream handler")
	}
	if logger == nil {
		return nil, fmt.Errorf("recovery middleware requires a logger")
	}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		preservedHeaders := response.Header().Clone()
		observer := &responseObserver{ResponseWriter: response}
		defer func() {
			if recovered := recover(); recovered != nil {
				if recoveredError, ok := recovered.(error); ok && recoveredError == http.ErrAbortHandler {
					panic(http.ErrAbortHandler)
				}
				requestID, _ := RequestID(request.Context())
				logger.ErrorContext(
					request.Context(),
					"request panicked",
					"error_class", "panic",
					"request_id", requestID,
					"stack", string(debug.Stack()),
				)
				if observer.wroteHeader {
					panic(http.ErrAbortHandler)
				}
				headers := response.Header()
				clear(headers)
				for name, values := range preservedHeaders {
					headers[name] = values
				}
				message := "internal server error"
				if requestID != "" {
					message += "\nrequest_id=" + requestID
				}
				http.Error(observer, message, http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(observer, request)
	}), nil
}
