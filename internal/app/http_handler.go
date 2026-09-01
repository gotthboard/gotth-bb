package app

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"git.dannyhunn.com/agents/gotth-bb/internal/observability"
)

// NewHTTPHandler assembles the request-ID, access-log, and recovery boundaries
// in their required outer-to-inner order around the application handler.
//
// Complexity: construction is tight Theta(1) time and auxiliary space. Request
// cost is the sum of the documented middleware and downstream-handler costs;
// request-ID generation always reads 16 entropy bytes.
func NewHTTPHandler(application http.Handler, logger *slog.Logger, entropy io.Reader, clock observability.Clock) (http.Handler, error) {
	if entropy == nil {
		return nil, fmt.Errorf("HTTP request ID entropy source is required")
	}

	recovery, err := observability.NewRecoveryMiddleware(application, logger)
	if err != nil {
		return nil, fmt.Errorf("construct recovery middleware: %w", err)
	}
	accessLog, err := observability.NewAccessLogMiddleware(recovery, logger, clock)
	if err != nil {
		return nil, fmt.Errorf("construct access log middleware: %w", err)
	}
	return observability.NewRequestIDMiddleware(accessLog, func() (string, error) {
		return observability.GenerateRequestID(entropy)
	})
}
