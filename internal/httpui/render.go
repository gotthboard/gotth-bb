package httpui

import (
	"fmt"
	"net/http"

	"github.com/a-h/templ"
)

// renderResponse buffers exactly one complete page or HTMX fragment before
// committing the requested non-empty HTML status. Callers must propagate a
// returned error to the outer recovery boundary so render/write failures are
// observable without exposing template details to the browser.
//
// Complexity: for rendered output of n bytes and delegated component work R,
// time is O(R+n), Omega(1), and auxiliary space is O(n), Omega(1). The pooled
// templ buffer avoids retaining one unbounded buffer per request after return;
// response transport cost remains external.
func renderResponse(response http.ResponseWriter, request *http.Request, status int, page, fragment templ.Component) error {
	if status < http.StatusOK || status > 599 || status == http.StatusNoContent || status == http.StatusResetContent || status == http.StatusNotModified {
		return fmt.Errorf("HTML response status is invalid: %d", status)
	}
	if page == nil {
		return fmt.Errorf("complete page component is required")
	}
	if fragment == nil {
		return fmt.Errorf("HTMX fragment component is required")
	}
	selected := page
	renderContext := request.Context()
	if selectResponseMode(response, request) == responseModeFragment {
		selected = fragment
	} else {
		renderContext = withFooterTemplateStart(renderContext)
	}
	buffer := templ.GetBuffer()
	defer templ.ReleaseBuffer(buffer)
	if err := selected.Render(renderContext, buffer); err != nil {
		return fmt.Errorf("render HTML response: %w", err)
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "private, no-store")
	response.WriteHeader(status)
	if _, err := response.Write(buffer.Bytes()); err != nil {
		return fmt.Errorf("write HTML response: %w", err)
	}
	return nil
}
