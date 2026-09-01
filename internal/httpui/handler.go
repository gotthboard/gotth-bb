package httpui

import (
	"io"
	"net/http"
)

// NewHandler constructs the internal HTTP routing shell. Caddy removes the
// external base prefix before requests reach these routes.
//
// Complexity: the fixed route table is Θ(1) time and Θ(1) auxiliary space.
// net/http owns request matching and per-request transport costs after return.
func NewHandler() http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("GET /health/live", serveLiveness)
	router.HandleFunc("GET /health/ready", serveNotReady)
	return router
}

// serveLiveness reports only that the process can execute its HTTP loop.
//
// Complexity: this writes a fixed three-byte body in Θ(1) time and Θ(1)
// auxiliary space; ResponseWriter I/O may block according to the transport.
func serveLiveness(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, "ok\n")
}

// serveNotReady fails closed until the database-backed readiness contract is
// implemented; an incomplete alpha foundation must not advertise readiness.
//
// Complexity: this writes a fixed ten-byte body in Θ(1) time and Θ(1)
// auxiliary space; http.Error delegates fixed-size ResponseWriter I/O.
func serveNotReady(response http.ResponseWriter, _ *http.Request) {
	http.Error(response, "not ready", http.StatusServiceUnavailable)
}
