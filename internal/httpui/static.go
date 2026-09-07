package httpui

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

const appStylesheetFilename = "app-884dd01428d7d3d5fc8389301fe159dd5b4475f466007c268363c69dfd03e821.css"

//go:embed static/app-884dd01428d7d3d5fc8389301fe159dd5b4475f466007c268363c69dfd03e821.css
var appStylesheet []byte

//go:embed static/htmx-2.0.10.min.js
var htmxScript []byte

//go:embed static/markdown-toolbar-v1.js
var markdownToolbarScript []byte

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
