package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// AreaTopicPageLoader returns one access-filtered area topic page for the
// canonical request authority, slug, and bounded page number.
type AreaTopicPageLoader func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error)

// newAreaTopicListHandler parses the canonical page query, passes only the
// server-owned request authority to the loader, projects persistence rows into
// narrow presentation values, and renders complete or HTMX area-topic pages.
// Missing/invisible input shares one 404; store or malformed-result failures
// discard all partial data and render one redacted 503.
//
// Complexity: construction is tight Theta(1) time and space. For q bounded
// query bytes, r <= 25 topic rows, delegated loader cost L, and rendered bytes
// n, request time is O(q+r+L+n), Omega(1), without a tight Theta bound because
// PostgreSQL and writer work varies. Auxiliary space is O(r+n+A(L)), Omega(1),
// including one typed projection and buffered render. No operation is retried.
func newAreaTopicListHandler(builder URLBuilder, maximumPage int32, load AreaTopicPageLoader) (http.Handler, error) {
	baseView, err := newPageView(builder, "Discussion area")
	if err != nil {
		return nil, fmt.Errorf("construct area topic base view: %w", err)
	}
	if _, err := parseTopicPageQuery("", maximumPage); err != nil {
		return nil, fmt.Errorf("validate area topic page maximum: %w", err)
	}
	if load == nil {
		return nil, fmt.Errorf("area topic page loader is required")
	}
	notFoundView := baseView
	notFoundView.Title = "Page not found"
	notFoundView.CanonicalURL = ""
	unavailableView := baseView
	unavailableView.Title = "Area unavailable"
	unavailableView.CanonicalURL = ""
	serveError := func(response http.ResponseWriter, request *http.Request, status int, view pageView, heading, message string) {
		if renderErr := renderResponse(
			response, request, status,
			errorPage(view, status, heading, message),
			errorContent(view, status, heading, message),
		); renderErr != nil {
			panic(renderErr)
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		slug := chi.URLParam(request, "slug")
		pageNumber, parseErr := parseTopicPageQuery(request.URL.RawQuery, maximumPage)
		if slug == "" || parseErr != nil {
			serveError(response, request, http.StatusNotFound, notFoundView, "Page not found", "The requested page does not exist or is not visible to you.")
			return
		}
		authentication := sessionAuthenticationFromContext(request.Context())
		loaded, loadErr := load(request.Context(), authentication.Access, slug, pageNumber)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveError(response, request, http.StatusNotFound, notFoundView, "Page not found", "The requested page does not exist or is not visible to you.")
				return
			}
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Area unavailable", "This discussion area is temporarily unavailable.")
			return
		}
		invalid := loaded.Area.ID <= 0 || loaded.Area.Slug != slug || loaded.Area.Name == "" || loaded.Number != pageNumber ||
			loaded.TotalTopics < 0 || loaded.TotalPages < 0 ||
			(len(loaded.Topics) == 0 && (loaded.TotalTopics != 0 || loaded.TotalPages != 0)) ||
			(len(loaded.Topics) > 0 && (loaded.TotalTopics <= 0 || loaded.TotalPages < int64(pageNumber)))
		items := make([]areaTopicListItem, len(loaded.Topics))
		for index, topic := range loaded.Topics {
			if invalid || topic.TopicID <= 0 || topic.Title == "" || topic.AuthorDisplayName == "" ||
				topic.ReplyCount < 0 || !topic.LastActivityAt.Valid || topic.TotalVisibleTopics != loaded.TotalTopics {
				invalid = true
				break
			}
			stateLabel := ""
			switch topic.State {
			case "open":
				stateLabel = "Open"
			case "locked":
				stateLabel = "Locked"
			case "archived":
				stateLabel = "Archived"
			case "hidden":
				stateLabel = "Hidden"
			default:
				invalid = true
			}
			if invalid {
				break
			}
			topicURL, buildErr := builder.Path("topics", strconv.FormatInt(topic.TopicID, 10))
			if buildErr != nil {
				invalid = true
				break
			}
			replyLabel := fmt.Sprintf("%d replies", topic.ReplyCount)
			if topic.ReplyCount == 1 {
				replyLabel = "1 reply"
			}
			items[index] = areaTopicListItem{
				Title: topic.Title, URL: topicURL, StateLabel: stateLabel, Pinned: topic.PinnedAt.Valid,
				ReplyLabel: replyLabel, Author: topic.AuthorDisplayName,
				LastActivity: topic.LastActivityAt.Time.UTC().Format("Jan 2, 2006 15:04 MST"),
			}
		}
		segments := []string{"areas", slug}
		view, viewErr := newPageView(builder, loaded.Area.Name, segments...)
		previousURL := ""
		nextURL := ""
		if viewErr == nil && pageNumber > 1 {
			view.CanonicalURL, viewErr = builder.AbsoluteWithQuery(segments, url.Values{"page": {strconv.FormatInt(int64(pageNumber), 10)}})
		}
		if viewErr == nil && pageNumber > 1 {
			previousPage := pageNumber - 1
			if previousPage == 1 {
				previousURL, viewErr = builder.Path(segments...)
			} else {
				previousURL, viewErr = builder.PathWithQuery(segments, url.Values{"page": {strconv.FormatInt(int64(previousPage), 10)}})
			}
		}
		if viewErr == nil && int64(pageNumber) < loaded.TotalPages && pageNumber < maximumPage {
			nextURL, viewErr = builder.PathWithQuery(segments, url.Values{"page": {strconv.FormatInt(int64(pageNumber+1), 10)}})
		}
		if invalid || viewErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Area unavailable", "This discussion area is temporarily unavailable.")
			return
		}
		presentation := areaTopicListView{
			Name: loaded.Area.Name, Description: loaded.Area.Description, Topics: items,
			Number: pageNumber, TotalTopics: loaded.TotalTopics, PreviousURL: previousURL, NextURL: nextURL,
		}
		if renderErr := renderResponse(
			response, request, http.StatusOK,
			areaTopicListPage(view, presentation),
			areaTopicListContent(view, presentation),
		); renderErr != nil {
			panic(renderErr)
		}
	}), nil
}
