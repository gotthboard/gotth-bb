package httpui

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

const (
	appStylesheetFilename               = "app-69b83784213ded6fa360c25d2a2929f8d5260a9c26104a889e4947baed620a65.css"
	immediatePriorAppStylesheetFilename = "app-3f1bf5d28948bd8391383ee7aba5e9cf14fc5b746449e93d996dff895406cacf.css"
	priorAppStylesheetFilename          = "app-4237ef90067eac5c030813c0722cfd222a7419a85169782c672d4c96f8727893.css"
	previousAppStylesheetFilename       = "app-d8e495881d927546f70f69915c1807efc8fb02c2bc22c9d2a98e87736a04e210.css"
	olderAppStylesheetFilename          = "app-3104ce3eede233f21f5a885d4fc547bcc33a24f8249e1d2757f0606a6d958251.css"
	legacyAppStylesheetFilename         = "app-3faf03facd9c7083d4d359467a15860e45effe7a5a6c94aeb7c98f756993a6fa.css"
	discoveryResponseFilename           = "discovery-response-83d6c618d951879489e90e83d947a31d4211b8f565604c773346fd1ef0d4152b.js"
	markdownToolbarFilename             = "markdown-toolbar-9b94e2d14953039596b28abd1bf40cda34ebc0fcd910204606ca0f3862b36848.js"
)

//go:embed static/app-69b83784213ded6fa360c25d2a2929f8d5260a9c26104a889e4947baed620a65.css
var appStylesheet []byte

//go:embed static/app-3f1bf5d28948bd8391383ee7aba5e9cf14fc5b746449e93d996dff895406cacf.css
var immediatePriorAppStylesheet []byte

//go:embed static/app-4237ef90067eac5c030813c0722cfd222a7419a85169782c672d4c96f8727893.css
var priorAppStylesheet []byte

//go:embed static/app-d8e495881d927546f70f69915c1807efc8fb02c2bc22c9d2a98e87736a04e210.css
var previousAppStylesheet []byte

//go:embed static/app-3104ce3eede233f21f5a885d4fc547bcc33a24f8249e1d2757f0606a6d958251.css
var olderAppStylesheet []byte

//go:embed static/app-3faf03facd9c7083d4d359467a15860e45effe7a5a6c94aeb7c98f756993a6fa.css
var legacyAppStylesheet []byte

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
