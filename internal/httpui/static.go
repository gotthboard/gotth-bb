package httpui

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

const (
	appStylesheetFilename   = "app-1b528939bc5a90b46925897b73a2e268ea711eaed8177b516d3b9cf5a767bc9b.css"
	markdownToolbarFilename = "markdown-toolbar-9b94e2d14953039596b28abd1bf40cda34ebc0fcd910204606ca0f3862b36848.js"
)

//go:embed static/app-1b528939bc5a90b46925897b73a2e268ea711eaed8177b516d3b9cf5a767bc9b.css
var appStylesheet []byte

//go:embed static/htmx-2.0.10.min.js
var htmxScript []byte

//go:embed static/markdown-toolbar-9b94e2d14953039596b28abd1bf40cda34ebc0fcd910204606ca0f3862b36848.js
var markdownToolbarScript []byte

// staticAssetHandler serves one content- or version-addressed immutable byte slice with a
// fixed media type. net/http owns HEAD, range, and conditional semantics.
//
// Complexity: construction is tight Theta(1) time and space. For an ordinary
// response of n bytes, delegated serving is O(n) time and O(1) local auxiliary
// space; bytes.NewReader retains the embedded slice without copying it.
func staticAssetHandler(contentType string, content []byte) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", contentType)
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(response, request, "", time.Time{}, bytes.NewReader(content))
	})
}
