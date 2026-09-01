package observability

import "net/http"

type responseObserver struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

// WriteHeader forwards informational statuses and records and forwards only
// the first switching-protocols or final response status.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary space O(1),
// Omega(1), and tight Theta(1), excluding delegated ResponseWriter I/O.
func (observer *responseObserver) WriteHeader(status int) {
	if observer.wroteHeader {
		return
	}
	observer.ResponseWriter.WriteHeader(status)
	if status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols {
		return
	}
	observer.wroteHeader = true
	observer.status = status
}

// Write records the implicit success status and successfully delegated bytes.
//
// Complexity: for n payload bytes, wrapper work is tight Theta(1) and
// delegated write time is O(n), Omega(1), and tight Theta(n) for an in-memory
// writer; auxiliary space O(1), Omega(1), and tight Theta(1). The payload is
// passed through without copying.
func (observer *responseObserver) Write(payload []byte) (int, error) {
	if !observer.wroteHeader {
		observer.WriteHeader(http.StatusOK)
	}
	written, err := observer.ResponseWriter.Write(payload)
	observer.bytes += int64(written)
	return written, err
}

// Unwrap exposes the underlying writer to net/http.ResponseController.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary space O(1),
// Omega(1), and tight Theta(1).
func (observer *responseObserver) Unwrap() http.ResponseWriter {
	return observer.ResponseWriter
}
