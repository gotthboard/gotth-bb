package httpui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
)

// AreaIndexLister returns only area summaries visible to one canonical request authority.
type AreaIndexLister func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error)

type areaIndexNotice string

const (
	areaIndexNoNotice                 areaIndexNotice = ""
	areaIndexLogoutVerificationFailed areaIndexNotice = "logout-verification-failed"
	areaIndexReportSubmitted          areaIndexNotice = "report-submitted"
)

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
		unreadEnabled := unreadControlsEnabled(request.Context())
		if authentication.Access.Authenticated {
			response.Header().Set("Cache-Control", "private, no-store")
		}
		notice := areaIndexNoNotice
		if authentication.Access.Authenticated {
			switch request.URL.RawQuery {
			case logoutVerificationFailureQuery:
				notice = areaIndexLogoutVerificationFailed
			case reportSubmittedQuery:
				notice = areaIndexReportSubmitted
			}
		}
		summaries, err := list(request.Context(), authentication.Access)
		if err != nil {
			serveUnavailable(response, request)
			return
		}
		items := make([]areaIndexItem, len(summaries))
		for index, summary := range summaries {
			area := summary.Area
			if area.Name == "" || !policy.ValidAreaSlug(area.Slug) {
				serveUnavailable(response, request)
				return
			}
			areaURL, buildErr := builder.Path("areas", area.Slug)
			if buildErr != nil {
				serveUnavailable(response, request)
				return
			}
			item := areaIndexItem{
				Name: area.Name, Description: area.Description, URL: areaURL,
				TopicCount: summary.TopicCount, PostCount: summary.PostCount,
			}
			if unreadEnabled && authentication.Access.Authenticated && summary.UnreadTopicCount == nil ||
				unreadEnabled && !authentication.Access.Authenticated && summary.UnreadTopicCount != nil ||
				summary.UnreadTopicCount != nil && *summary.UnreadTopicCount < 0 {
				serveUnavailable(response, request)
				return
			}
			if unreadEnabled && summary.UnreadTopicCount != nil {
				if *summary.UnreadTopicCount == 1 {
					item.UnreadLabel = "1 unread topic"
				} else {
					item.UnreadLabel = fmt.Sprintf("%d unread topics", *summary.UnreadTopicCount)
				}
			}
			if latest := summary.LatestPost; latest != nil {
				if latest.TopicID <= 0 || latest.TopicTitle == "" || latest.PostID <= 0 || latest.PostNumber <= 0 || latest.TreeOrdinal <= 0 || latest.Author == "" || latest.CreatedAt.IsZero() {
					serveUnavailable(response, request)
					return
				}
				query := make(url.Values)
				if page := 1 + (latest.TreeOrdinal-1)/int64(store.PostPageSize); page > 1 {
					query.Set("page", strconv.FormatInt(int64(page), 10))
				}
				latestURL, buildErr := builder.PathWithQueryAndFragment(
					[]string{"topics", strconv.FormatInt(latest.TopicID, 10)},
					query,
					"post-"+strconv.FormatInt(latest.PostID, 10),
				)
				if buildErr != nil {
					serveUnavailable(response, request)
					return
				}
				item.LatestTitle = latest.TopicTitle
				item.LatestURL = latestURL
				item.LatestAuthor = latest.Author
				item.LatestAt = latest.CreatedAt.Format("Jan 2, 2006 15:04 MST")
			}
			items[index] = item
		}
		if renderErr := renderResponse(
			response,
			request,
			http.StatusOK,
			areaIndexPageWithAreas(view, items, notice),
			areaIndexContentWithAreas(view, items, notice),
		); renderErr != nil {
			panic(renderErr)
		}
	}), nil
}
