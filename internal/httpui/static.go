package httpui

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

const (
	appStylesheetFilename         = "app-3104ce3eede233f21f5a885d4fc547bcc33a24f8249e1d2757f0606a6d958251.css"
	previousAppStylesheetFilename = "app-3faf03facd9c7083d4d359467a15860e45effe7a5a6c94aeb7c98f756993a6fa.css"
	discoveryResponseFilename     = "discovery-response-83d6c618d951879489e90e83d947a31d4211b8f565604c773346fd1ef0d4152b.js"
	markdownToolbarFilename       = "markdown-toolbar-9b94e2d14953039596b28abd1bf40cda34ebc0fcd910204606ca0f3862b36848.js"
)

//go:embed static/app-3104ce3eede233f21f5a885d4fc547bcc33a24f8249e1d2757f0606a6d958251.css
var appStylesheet []byte

//go:embed static/app-3faf03facd9c7083d4d359467a15860e45effe7a5a6c94aeb7c98f756993a6fa.css
var previousAppStylesheet []byte

//go:embed static/htmx-2.0.10.min.js
var htmxScript []byte

//go:embed static/discovery-response-83d6c618d951879489e90e83d947a31d4211b8f565604c773346fd1ef0d4152b.js
var discoveryResponseScript []byte

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
