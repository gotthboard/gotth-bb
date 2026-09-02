package httpui

import (
	"net/http"
	"strconv"
)

const mainContentSelector = "#main-content"

// serveSessionRedirect performs a browser-level redirect for authentication,
// revalidation, and other session-boundary transitions. HTMX's HX-Redirect is
// intentional here because the complete document and browser security state
// must be reloaded across that boundary.
//
// Complexity: for a bounded location of n bytes, time and auxiliary space are
// O(n), Omega(1). No external authority is accepted here.
func serveSessionRedirect(response http.ResponseWriter, request *http.Request, location string) {
	if selectResponseMode(response, request) == responseModeFragment {
		response.Header().Set("HX-Redirect", location)
		response.WriteHeader(http.StatusNoContent)
		return
	}
	response.Header().Set("Location", location)
	response.WriteHeader(http.StatusSeeOther)
}

// serveMutationNavigation preserves ordinary POST/Redirect/GET fallback while
// directing HTMX to fetch and swap the canonical server-rendered main fragment
// without reloading the document. The follow-up GET remains authoritative for
// current access, pagination, moderation controls, and post counts.
//
// Complexity: for a bounded location of n bytes, JSON construction is O(n)
// time and space, Omega(1). No request is retried by the server.
func serveMutationNavigation(response http.ResponseWriter, request *http.Request, location string) {
	if selectResponseMode(response, request) == responseModeFragment {
		value := `{"path":` + strconv.Quote(location) + `,"target":"` + mainContentSelector + `","swap":"outerHTML"}`
		response.Header().Set("HX-Location", value)
		response.WriteHeader(http.StatusNoContent)
		return
	}
	response.Header().Set("Location", location)
	response.WriteHeader(http.StatusSeeOther)
}
