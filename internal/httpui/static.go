package httpui

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

const appStylesheetFilename = "app-bc1d159bbac02ede6796386bbbd35f48226f85b0b719e0734fb57946954bf046.css"

//go:embed static/app-bc1d159bbac02ede6796386bbbd35f48226f85b0b719e0734fb57946954bf046.css
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
