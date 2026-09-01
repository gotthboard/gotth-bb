package observability

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Clock supplies monotonic-capable timestamps for request duration accounting.
type Clock func() time.Time

// NewAccessLogMiddleware records one bounded structured completion event
// without logging raw URLs, query values, headers, cookies, or bodies.
//
// Complexity: construction is tight Theta(1) time and auxiliary space. For
// request-context chain depth d, per-request wrapper accounting is O(d+1),
// Omega(1), and tight Theta(d+1) time with tight Theta(1) auxiliary space,
// excluding downstream, clock, and configured slog.Handler costs.
func NewAccessLogMiddleware(next http.Handler, logger *slog.Logger, clock Clock) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("access log middleware requires a downstream handler")
	}
	if logger == nil {
		return nil, fmt.Errorf("access log middleware requires a logger")
	}
	if clock == nil {
		return nil, fmt.Errorf("access log middleware requires a clock")
	}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := clock()
		observer := &responseObserver{ResponseWriter: response}
		defer func() {
			recovered := recover()
			duration := clock().Sub(started)
			if duration < 0 {
				duration = 0
			}
			requestID, _ := RequestID(request.Context())
			if recoveredError, ok := recovered.(error); ok && recoveredError == http.ErrAbortHandler {
				logger.InfoContext(
					request.Context(),
					"request aborted",
					"error_class", "abort",
					"request_id", requestID,
					"route", request.Pattern,
					"method", request.Method,
					"bytes", observer.bytes,
					"duration_ms", duration.Milliseconds(),
				)
				panic(http.ErrAbortHandler)
			}
			if recovered != nil {
				panic(recovered)
			}
			status := observer.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.InfoContext(
				request.Context(),
				"request completed",
				"request_id", requestID,
				"route", request.Pattern,
				"method", request.Method,
				"status", status,
				"bytes", observer.bytes,
				"duration_ms", duration.Milliseconds(),
			)
		}()

		next.ServeHTTP(observer, request)
	}), nil
}
