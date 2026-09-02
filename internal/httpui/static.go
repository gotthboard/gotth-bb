package httpui

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

const appStylesheetFilename = "app-e71086849e8a67de8ca330847ac0c75e5bc98c47149a05b7423c8824f9f9be14.css"

//go:embed static/app-e71086849e8a67de8ca330847ac0c75e5bc98c47149a05b7423c8824f9f9be14.css
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
