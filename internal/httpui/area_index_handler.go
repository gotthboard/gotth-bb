package httpui

import (
	"context"
	"fmt"
	"net/http"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

type areaIndexLister func(context.Context, auth.AccessContext) ([]db.Area, error)

// newAreaIndexHandler loads the visible areas for the exact server-owned
// request authority and renders either the complete root page or its HTMX
// fragment. Store failures discard every returned row and emit one redacted
// unavailable page; render or committed-write failures remain observable at
// the outer recovery boundary.
//
// Complexity: construction is tight Theta(1) time and space. For g authority
// group IDs, delegated list cost L(g,r), r returned areas, and rendered output
// cost R(r,n), request
// time is O(L+R), Omega(1), with no tighter bound because PostgreSQL and writer
// costs vary. Auxiliary space is O(A(L)+r+A(R)), Omega(1), including one typed
// view-model projection and the buffered renderer. No operation is retried or
// detached.
func newAreaIndexHandler(view pageView, list areaIndexLister) (http.Handler, error) {
	if list == nil {
		return nil, fmt.Errorf("area index lister is required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authentication := sessionAuthenticationFromContext(request.Context())
		areas, err := list(request.Context(), authentication.Access)
		if err != nil {
			if renderErr := renderResponse(
				response,
				request,
				http.StatusServiceUnavailable,
				errorPage(view, http.StatusServiceUnavailable, "Areas unavailable", "Discussion areas are temporarily unavailable."),
				errorContent(view, http.StatusServiceUnavailable, "Areas unavailable", "Discussion areas are temporarily unavailable."),
			); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		items := make([]areaIndexItem, len(areas))
		for index, area := range areas {
			items[index] = areaIndexItem{Name: area.Name, Description: area.Description}
		}
		if renderErr := renderResponse(
			response,
			request,
			http.StatusOK,
			areaIndexPageWithAreas(view, items),
			areaIndexContentWithAreas(view, items),
		); renderErr != nil {
			panic(renderErr)
		}
	}), nil
}
