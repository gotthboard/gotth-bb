package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	contentrender "git.dannyhunn.com/agents/gotth-bb/internal/render"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TopicPostPageLoader returns one atomically access-filtered topic/post page
// for the canonical request authority, topic identifier, and page number.
type TopicPostPageLoader func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error)

// newTopicPostListHandler parses canonical topic/page input, invokes one
// access-aware loader, projects only validated rows, re-sanitizes persisted
// renderer output into TrustedHTML, and renders full or HTMX topic pages.
// Missing and inaccessible input shares one 404; storage or malformed-result
// failures discard all partial presentation and return one redacted 503.
//
// Complexity: construction is tight Theta(1) time and space. For q bounded
// query/path bytes, r <= 25 post rows containing h rendered bytes, delegated
// loader cost L, and n response bytes, request time is O(q+r+h+L+n), Omega(1),
// without a tight Theta bound because PostgreSQL and writer work vary.
// Auxiliary space is O(r+h+n+A(L)), Omega(1), including trusted projections
// and the buffered render. No operation is retried or detached.
func newTopicPostListHandler(builder URLBuilder, maximumPage int32, load TopicPostPageLoader) (http.Handler, error) {
	baseView, err := newPageView(builder, "Topic")
	if err != nil {
		return nil, fmt.Errorf("construct topic post base view: %w", err)
	}
	if _, err := parseTopicPageQuery("", maximumPage); err != nil {
		return nil, fmt.Errorf("validate topic post page maximum: %w", err)
	}
	if load == nil {
		return nil, fmt.Errorf("topic post page loader is required")
	}
	notFoundView := baseView
	notFoundView.Title = "Page not found"
	notFoundView.CanonicalURL = ""
	unavailableView := baseView
	unavailableView.Title = "Topic unavailable"
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
		topicID, identifierErr := parseTopicID(chi.URLParam(request, "topicID"))
		pageNumber, pageErr := parseTopicPageQuery(request.URL.RawQuery, maximumPage)
		if request.URL.RawPath != "" || identifierErr != nil || pageErr != nil {
			serveError(response, request, http.StatusNotFound, notFoundView, "Page not found", "The requested page does not exist or is not visible to you.")
			return
		}
		authentication := sessionAuthenticationFromContext(request.Context())
		loaded, loadErr := load(request.Context(), authentication.Access, topicID, pageNumber)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveError(response, request, http.StatusNotFound, notFoundView, "Page not found", "The requested page does not exist or is not visible to you.")
				return
			}
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Topic unavailable", "This topic is temporarily unavailable.")
			return
		}
		if len(loaded.Rows) == 0 {
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Topic unavailable", "This topic is temporarily unavailable.")
			return
		}
		first := loaded.Rows[0]
		expectedPages := int64(0)
		if loaded.TotalPosts > 0 {
			expectedPages = 1 + (loaded.TotalPosts-1)/int64(store.PostPageSize)
		}
		invalid := loaded.Number != pageNumber || loaded.TotalPosts < 0 || loaded.TotalPages != expectedPages ||
			first.AreaID <= 0 || !policy.ValidAreaSlug(first.AreaSlug) || first.AreaName == "" || first.TopicID != topicID ||
			first.TopicTitle == "" || first.TopicAuthorDisplayName == "" || !first.TopicCreatedAt.Valid ||
			len(loaded.Rows) > int(store.PostPageSize) ||
			(loaded.TotalPosts == 0 && (pageNumber != 1 || len(loaded.Rows) != 1 || first.PostID.Valid || first.PostNumber.Valid ||
				first.RenderedHtml.Valid || first.RendererVersion.Valid || first.Revision.Valid || first.PostCreatedAt.Valid ||
				first.PostUpdatedAt.Valid || first.PostEditedAt.Valid || first.PostAuthorDisplayName.Valid)) ||
			(loaded.TotalPosts > 0 && (pageNumber > int32(loaded.TotalPages) || !first.PostID.Valid))
		stateLabel := ""
		switch first.TopicState {
		case "open":
			stateLabel = "Open"
		case "locked":
			stateLabel = "Locked"
		case "archived":
			stateLabel = "Archived"
		case "hidden":
			stateLabel = "Hidden"
			invalid = invalid || authentication.Access.Role != auth.RoleModerator && authentication.Access.Role != auth.RoleAdministrator
		default:
			invalid = true
		}
		identifier := strconv.FormatInt(topicID, 10)
		segments := []string{"topics", identifier}
		view, viewErr := newPageView(builder, first.TopicTitle, segments...)
		areaURL := ""
		previousURL := ""
		nextURL := ""
		currentQuery := url.Values(nil)
		if pageNumber > 1 {
			currentQuery = url.Values{"page": {strconv.FormatInt(int64(pageNumber), 10)}}
		}
		if viewErr == nil && pageNumber > 1 {
			view.CanonicalURL, viewErr = builder.AbsoluteWithQuery(segments, currentQuery)
		}
		if viewErr == nil {
			areaURL, viewErr = builder.Path("areas", first.AreaSlug)
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
		posts := make([]topicPostItem, 0, len(loaded.Rows))
		if loaded.TotalPosts > 0 {
			for _, row := range loaded.Rows {
				validRow := sameTopicPostPresentationMetadata(first, row) && row.TotalVisiblePosts == loaded.TotalPosts &&
					row.PostID.Valid && row.PostID.Int64 > 0 && row.PostNumber.Valid && row.PostNumber.Int32 > 0 &&
					row.RenderedHtml.Valid && row.RendererVersion.Valid && row.RendererVersion.String != "" &&
					row.Revision.Valid && row.Revision.Int32 > 0 && row.PostCreatedAt.Valid && row.PostUpdatedAt.Valid &&
					row.PostAuthorDisplayName.Valid && row.PostAuthorDisplayName.String != ""
				if !validRow {
					invalid = true
					break
				}
				anchor := "post-" + strconv.FormatInt(row.PostID.Int64, 10)
				permalink, permalinkErr := builder.PathWithQueryAndFragment(segments, currentQuery, anchor)
				if permalinkErr != nil {
					viewErr = permalinkErr
					break
				}
				edited := ""
				if row.PostEditedAt.Valid {
					edited = "Edited " + row.PostEditedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST")
				}
				posts = append(posts, topicPostItem{
					Anchor: anchor, Permalink: permalink, Number: row.PostNumber.Int32,
					Author:  row.PostAuthorDisplayName.String,
					Created: row.PostCreatedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST"), Edited: edited,
					Body: contentrender.SanitizeHTML(row.RenderedHtml.String),
				})
			}
		}
		if invalid || viewErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Topic unavailable", "This topic is temporarily unavailable.")
			return
		}
		presentation := topicPostPageView{
			AreaName: first.AreaName, AreaURL: areaURL, Title: first.TopicTitle,
			StateLabel: stateLabel, Pinned: first.TopicPinnedAt.Valid,
			Author: first.TopicAuthorDisplayName, Started: first.TopicCreatedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST"),
			Posts: posts, Number: pageNumber, TotalPosts: loaded.TotalPosts,
			PreviousURL: previousURL, NextURL: nextURL,
		}
		if renderErr := renderResponse(
			response, request, http.StatusOK,
			topicPostListPage(view, presentation),
			topicPostListContent(view, presentation),
		); renderErr != nil {
			panic(renderErr)
		}
	}), nil
}

// sameTopicPostPresentationMetadata proves every projected row belongs to the
// same access-filtered topic and breadcrumb authority as the first row.
//
// Complexity: time and auxiliary space are tight Theta(1).
func sameTopicPostPresentationMetadata(first, row db.GetVisibleTopicPostPageRow) bool {
	return row.AreaID == first.AreaID && row.AreaSlug == first.AreaSlug && row.AreaName == first.AreaName &&
		row.AreaDescription == first.AreaDescription && row.TopicID == first.TopicID && row.TopicTitle == first.TopicTitle &&
		row.TopicState == first.TopicState && row.TopicPinnedAt == first.TopicPinnedAt && row.TopicCreatedAt == first.TopicCreatedAt &&
		row.TopicAuthorDisplayName == first.TopicAuthorDisplayName
}
