package httpui

import "net/http"

type responseMode uint8

const (
	responseModePage responseMode = iota + 1
	responseModeFragment
)

// selectResponseMode marks both request headers that affect representation and
// returns a fragment only for HTMX's exact request value outside history
// restoration. History cache misses must receive a complete document.
//
// Complexity: time and auxiliary space are tight Theta(1): two fixed Vary
// additions and two bounded header comparisons.
func selectResponseMode(response http.ResponseWriter, request *http.Request) responseMode {
	response.Header().Add("Vary", "HX-Request")
	response.Header().Add("Vary", "HX-History-Restore-Request")
	if request.Header.Get("HX-Request") == "true" && request.Header.Get("HX-History-Restore-Request") != "true" {
		return responseModeFragment
	}
	return responseModePage
}
