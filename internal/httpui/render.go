package httpui

import (
	"fmt"
	"io"
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
		var shellErr error
		renderContext, shellErr = loadSiteShellForDocument(renderContext)
		if shellErr != nil {
			return renderUnbrandedShellFailure(response, status)
		}
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

func renderUnbrandedShellFailure(response http.ResponseWriter, requestedStatus int) error {
	status := requestedStatus
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		status = http.StatusServiceUnavailable
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "private, no-store")
	response.WriteHeader(status)
	_, err := io.WriteString(response, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>Service unavailable</title></head><body><main><h1>Service unavailable</h1><p>This page is temporarily unavailable.</p></main></body></html>")
	if err != nil {
		return fmt.Errorf("write unbranded shell failure: %w", err)
	}
	return nil
}

func renderUnbrandedRouteError(response http.ResponseWriter, status int) error {
	title := "Page not found"
	message := "The requested page does not exist."
	if status == http.StatusMethodNotAllowed {
		title = "Method not allowed"
		message = "The requested method is not allowed for this page."
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "private, no-store")
	response.WriteHeader(status)
	if _, err := fmt.Fprintf(response, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>%s</title></head><body><main><h1>%s</h1><p>%s</p></main></body></html>", title, title, message); err != nil {
		return fmt.Errorf("write unbranded route error: %w", err)
	}
	return nil
}
