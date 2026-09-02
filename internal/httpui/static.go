package httpui

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

const appStylesheetFilename = "app-36aa5f1c6d71db407e7dfc84ecbb3d38253bf7686e0b8b96cf7e1c34dae047e3.css"

//go:embed static/app-36aa5f1c6d71db407e7dfc84ecbb3d38253bf7686e0b8b96cf7e1c34dae047e3.css
var appStylesheet []byte

//go:embed static/htmx-2.0.10.min.js
var htmxScript []byte

// staticAssetHandler serves one release-versioned immutable byte slice with a
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
