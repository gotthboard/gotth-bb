package httpui

import (
	"context"
	"fmt"
	"net/http"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

// AreaIndexLister returns only areas visible to one canonical request authority.
type AreaIndexLister func(context.Context, auth.AccessContext) ([]db.Area, error)

// newAreaIndexHandler loads the visible areas for the exact server-owned
// request authority, builds canonical area links from schema-valid slugs, and
// renders either the complete root page or its HTMX fragment. Store failures
// and malformed rows discard every returned row and emit one redacted
// unavailable page; render or committed-write failures remain observable at
// the outer recovery boundary.
//
// Complexity: construction is tight Theta(1) time and space. For g authority
// group IDs, delegated list cost L(g,r), r returned areas containing s slug
// bytes, and rendered output cost R(r,n), request time is O(L+s+R), Omega(1),
// with no tighter bound because PostgreSQL and writer costs vary. Auxiliary
// space is O(A(L)+r+s+A(R)), Omega(1), including canonical link strings, one
// typed view-model projection, and the buffered renderer. No operation is
// retried or detached.
func newAreaIndexHandler(builder URLBuilder, view pageView, list AreaIndexLister) (http.Handler, error) {
	if _, err := builder.Path("areas", "area"); err != nil {
		return nil, fmt.Errorf("area index URL builder is invalid: %w", err)
	}
	if list == nil {
		return nil, fmt.Errorf("area index lister is required")
	}
	serveUnavailable := func(response http.ResponseWriter, request *http.Request) {
		if renderErr := renderResponse(
			response,
			request,
			http.StatusServiceUnavailable,
			errorPage(view, http.StatusServiceUnavailable, "Areas unavailable", "Discussion areas are temporarily unavailable."),
			errorContent(view, http.StatusServiceUnavailable, "Areas unavailable", "Discussion areas are temporarily unavailable."),
		); renderErr != nil {
			panic(renderErr)
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authentication := sessionAuthenticationFromContext(request.Context())
		logoutVerificationFailed := authentication.Access.Authenticated && request.URL.RawQuery == logoutVerificationFailureQuery
		areas, err := list(request.Context(), authentication.Access)
		if err != nil {
			serveUnavailable(response, request)
			return
		}
		items := make([]areaIndexItem, len(areas))
		for index, area := range areas {
			if area.Name == "" || !policy.ValidAreaSlug(area.Slug) {
				serveUnavailable(response, request)
				return
			}
			areaURL, buildErr := builder.Path("areas", area.Slug)
			if buildErr != nil {
				serveUnavailable(response, request)
				return
			}
			items[index] = areaIndexItem{Name: area.Name, Description: area.Description, URL: areaURL}
		}
		if renderErr := renderResponse(
			response,
			request,
			http.StatusOK,
			areaIndexPageWithAreas(view, items, logoutVerificationFailed),
			areaIndexContentWithAreas(view, items, logoutVerificationFailed),
		); renderErr != nil {
			panic(renderErr)
		}
	}), nil
}
