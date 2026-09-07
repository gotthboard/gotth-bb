package httpui

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
)

const maximumDiscoveryResponseBytes = 256 << 10

const discoveryResponseHeader = "X-GOTTH-Discovery-Response"

var errDiscoveryResponseTooLarge = errors.New("discovery response exceeds bound")

type discoveryBuffer struct {
	buffer bytes.Buffer
}

// Write rejects a chunk that would make the complete discovery envelope
// exceed 256 KiB; the caller discards the entire uncommitted buffer.
//
// Complexity: for p input bytes, time is O(p), Omega(1), and tight Theta(p)
// when accepted because bytes.Buffer copies the chunk; auxiliary capacity is
// bounded by maximumDiscoveryResponseBytes.
func (buffer *discoveryBuffer) Write(value []byte) (int, error) {
	if len(value) > maximumDiscoveryResponseBytes-buffer.buffer.Len() {
		return 0, errDiscoveryResponseTooLarge
	}
	return buffer.buffer.Write(value)
}

// renderDiscoveryResponse renders one complete page or HTMX fragment into a
// hard-bounded private buffer, checks cancellation, and only then commits
// headers and writes the response.
//
// Complexity: for rendered output n <= 256 KiB and delegated component cost R,
// time is O(R+n), Omega(1), with no tighter Theta bound because R may fail or
// stop early; auxiliary space is O(n), Omega(1), and tight Theta(n) for a
// successful n-byte response. Transport I/O remains delegated.
func renderDiscoveryResponse(response http.ResponseWriter, request *http.Request, status int, page, fragment templ.Component) error {
	if request == nil || status < http.StatusOK || status > 599 || status == http.StatusNoContent || status == http.StatusResetContent || status == http.StatusNotModified {
		return fmt.Errorf("discovery HTML response input is invalid")
	}
	if page == nil || fragment == nil {
		return fmt.Errorf("discovery HTML components are required")
	}
	if err := request.Context().Err(); err != nil {
		return err
	}
	selected := page
	renderContext := request.Context()
	if selectResponseMode(response, request) == responseModeFragment {
		selected = fragment
	} else {
		renderContext = withFooterTemplateStart(renderContext)
	}
	buffer := &discoveryBuffer{}
	if err := selected.Render(renderContext, buffer); err != nil {
		return fmt.Errorf("render bounded discovery response: %w", err)
	}
	if err := request.Context().Err(); err != nil {
		return err
	}
	response.Header().Set(discoveryResponseHeader, "1")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "private, no-store")
	response.WriteHeader(status)
	if _, err := response.Write(buffer.buffer.Bytes()); err != nil {
		return fmt.Errorf("write bounded discovery response: %w", err)
	}
	return nil
}
