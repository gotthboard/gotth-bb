package httpui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/discovery"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/jackc/pgx/v5"
)

type DiscoverySearchService func(context.Context, discovery.SearchRequest, auth.AccessContext) (discovery.SearchPage, error)
type DiscoveryActivityService func(context.Context, *discovery.AuthenticatedCursor, auth.AccessContext) (discovery.ActivityPage, error)
type DiscoveryDirectPostService func(context.Context, int64, auth.AccessContext) (discovery.DirectPost, error)

type DiscoveryHTTPServices struct {
	Search     DiscoverySearchService
	Activity   DiscoveryActivityService
	DirectPost DiscoveryDirectPostService
}

// newDiscoveryHandler constructs the post-session presentation boundary for
// the three exact AN-02 read routes. Preflight-owned typed request state is
// mandatory; handlers never reparse a weaker request representation.
//
// Complexity: construction is tight Theta(1). Per request, local work is
// O(r+s+n), Omega(1), where r <= 25 results, s is bounded presentation text,
// and n <= 256 KiB rendered output; delegated service and transport costs are
// additional and variable. Auxiliary space is O(r+s+n), bounded by the page
// and response limits.
func newDiscoveryHandler(builder URLBuilder, services DiscoveryHTTPServices) (http.Handler, error) {
	if services.Search == nil || services.Activity == nil || services.DirectPost == nil {
		return nil, fmt.Errorf("discovery HTTP services are incomplete")
	}
	search, err := newDiscoverySearchHandler(builder, services.Search)
	if err != nil {
		return nil, err
	}
	activity, err := newDiscoveryActivityHandler(builder, services.Activity)
	if err != nil {
		return nil, err
	}
	directPost, err := newDiscoveryDirectPostHandler(builder, services.DirectPost)
	if err != nil {
		return nil, err
	}
	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/search", search.ServeHTTP)
	router.Get("/activity", activity.ServeHTTP)
	router.Get("/posts/{postID}", directPost.ServeHTTP)
	return recordRoutePattern(router), nil
}

func newDiscoverySearchHandler(builder URLBuilder, search DiscoverySearchService) (http.Handler, error) {
	baseView, err := newPageView(builder, "Search")
	if err != nil {
		return nil, fmt.Errorf("construct search view: %w", err)
	}
	if search == nil {
		return nil, fmt.Errorf("search service is required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		parsed, ok := searchRequestFromContext(request.Context())
		if !ok {
			serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Search unavailable", "Search is temporarily unavailable.")
			return
		}
		page, searchErr := search(request.Context(), parsed, sessionAuthenticationFromContext(request.Context()).Access)
		if searchErr != nil {
			if errors.Is(searchErr, discovery.ErrInvalidSearchQuery) {
				presentation, buildErr := buildSearchPresentation(builder, parsed, discovery.SearchPage{})
				if buildErr == nil {
					presentation.ValidationErr = "The search text does not contain a usable positive expression."
					view := baseView
					view.CanonicalURL, buildErr = builder.AbsoluteWithQuery([]string{"search"}, canonicalSearchQuery(parsed, false))
					if buildErr == nil {
						renderDiscoveryOrFailure(response, request, view, http.StatusBadRequest, discoverySearchPage(view, presentation), discoverySearchContent(view, presentation))
						return
					}
				}
			}
			serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Search unavailable", "Search is temporarily unavailable.")
			return
		}
		if parsed.Page == 2 && len(page.Results) == 0 {
			serveDiscoveryFailure(response, request, baseView, http.StatusNotFound, "Page not found", "The requested search page does not exist.")
			return
		}
		presentation, buildErr := buildSearchPresentation(builder, parsed, page)
		if buildErr != nil {
			serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Search unavailable", "Search is temporarily unavailable.")
			return
		}
		view := baseView
		view.CanonicalURL, buildErr = builder.AbsoluteWithQuery([]string{"search"}, canonicalSearchQuery(parsed, false))
		if buildErr != nil {
			serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Search unavailable", "Search is temporarily unavailable.")
			return
		}
		renderDiscoveryOrFailure(response, request, view, http.StatusOK, discoverySearchPage(view, presentation), discoverySearchContent(view, presentation))
	}), nil
}

func buildSearchPresentation(builder URLBuilder, request discovery.SearchRequest, page discovery.SearchPage) (discoverySearchPageView, error) {
	if len(page.Results) > discovery.SearchPageSize {
		return discoverySearchPageView{}, fmt.Errorf("search result bound exceeded")
	}
	action, err := builder.Path("search")
	if err != nil {
		return discoverySearchPageView{}, err
	}
	presentation := discoverySearchPageView{
		ActionURL: action, Query: request.Query, Area: request.AreaSlug,
		ScopeLabel: "Text rank applies only within the newest 50 authorized matches.",
	}
	if request.AuthorID > 0 {
		presentation.Author = strconv.FormatInt(request.AuthorID, 10)
	}
	if !request.From.IsZero() {
		presentation.From = request.From.Format("2006-01-02")
	}
	if !request.ToExclusive.IsZero() {
		to := request.ToExclusive.AddDate(0, 0, -1)
		if request.ToInclusiveMaximum {
			to = request.ToExclusive
		}
		presentation.To = to.Format("2006-01-02")
	}
	if page.BeyondWindow {
		presentation.ScopeLabel += " At least 51 authorized matches exist; no exact total is claimed."
	}
	presentation.Results = make([]discoverySearchResultView, len(page.Results))
	for index, result := range page.Results {
		if result.ID <= 0 || result.AreaID <= 0 || !policy.ValidAreaSlug(result.AreaSlug) || result.AreaName == "" || result.TopicID <= 0 || result.TopicTitle == "" || result.AuthorID <= 0 || result.AuthorName == "" || !result.CreatedAt.Valid || result.CreatedAt.Time.Year() < 1 || result.CreatedAt.Time.Year() > 9999 || math.IsNaN(float64(result.Rank)) || math.IsInf(float64(result.Rank), 0) || utf8.RuneCountInString(result.Excerpt) > discovery.MaximumExcerptRunes {
			return discoverySearchPageView{}, fmt.Errorf("search result is malformed")
		}
		segments := []string{"topics", strconv.FormatInt(result.TopicID, 10)}
		kind := "Topic"
		if result.Kind == "post" {
			if result.PostID <= 0 || result.PostID != result.ID {
				return discoverySearchPageView{}, fmt.Errorf("search post result is malformed")
			}
			segments, kind = []string{"posts", strconv.FormatInt(result.PostID, 10)}, "Post"
		} else if result.Kind != "topic" || result.ID != result.TopicID || result.PostID != 0 || result.Excerpt != "" {
			return discoverySearchPageView{}, fmt.Errorf("search topic result is malformed")
		}
		resultURL, buildErr := builder.Path(segments...)
		if buildErr != nil {
			return discoverySearchPageView{}, buildErr
		}
		topicURL := ""
		if result.Kind == "post" {
			topicURL, buildErr = builder.Path("topics", strconv.FormatInt(result.TopicID, 10))
			if buildErr != nil {
				return discoverySearchPageView{}, buildErr
			}
		}
		presentation.Results[index] = discoverySearchResultView{KindLabel: kind, URL: resultURL, TopicURL: topicURL, AreaName: result.AreaName, TopicTitle: result.TopicTitle, AuthorName: result.AuthorName, Created: result.CreatedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST"), Excerpt: result.Excerpt}
	}
	if request.Page == 2 {
		presentation.PreviousURL, err = builder.PathWithQuery([]string{"search"}, canonicalSearchQuery(request, true))
	} else if page.HasNextPage {
		next := canonicalSearchQuery(request, false)
		next.Set("page", "2")
		presentation.NextURL, err = builder.PathWithQuery([]string{"search"}, next)
	}
	return presentation, err
}

func canonicalSearchQuery(request discovery.SearchRequest, firstPage bool) url.Values {
	values := make(url.Values)
	if request.Query != "" {
		values.Set("q", request.Query)
	}
	if request.AuthorID > 0 {
		values.Set("author", strconv.FormatInt(request.AuthorID, 10))
	}
	if request.AreaSlug != "" {
		values.Set("area", request.AreaSlug)
	}
	if !request.From.IsZero() {
		values.Set("from", request.From.Format("2006-01-02"))
	}
	if !request.ToExclusive.IsZero() {
		to := request.ToExclusive.AddDate(0, 0, -1)
		if request.ToInclusiveMaximum {
			to = request.ToExclusive
		}
		values.Set("to", to.Format("2006-01-02"))
	}
	if !firstPage && request.Page > 1 {
		values.Set("page", strconv.Itoa(int(request.Page)))
	}
	return values
}

func newDiscoveryActivityHandler(builder URLBuilder, activity DiscoveryActivityService) (http.Handler, error) {
	baseView, err := newPageView(builder, "Recent activity", "activity")
	if err != nil {
		return nil, fmt.Errorf("construct activity view: %w", err)
	}
	if activity == nil {
		return nil, fmt.Errorf("activity service is required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var cursor *discovery.AuthenticatedCursor
		if verified, ok := discoveryCursorFromContext(request.Context()); ok {
			cursor = &verified
		}
		page, activityErr := activity(request.Context(), cursor, sessionAuthenticationFromContext(request.Context()).Access)
		if activityErr != nil {
			if errors.Is(activityErr, discovery.ErrInvalidActivityCursor) {
				serveDiscoveryFailure(response, request, baseView, http.StatusBadRequest, "Invalid activity request", "The activity request is invalid.")
			} else {
				serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Activity unavailable", "Recent activity is temporarily unavailable.")
			}
			return
		}
		presentation, buildErr := buildActivityPresentation(builder, page)
		if buildErr != nil {
			serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Activity unavailable", "Recent activity is temporarily unavailable.")
			return
		}
		renderDiscoveryOrFailure(response, request, baseView, http.StatusOK, discoveryActivityPage(baseView, presentation), discoveryActivityContent(baseView, presentation))
	}), nil
}

func buildActivityPresentation(builder URLBuilder, page discovery.ActivityPage) (discoveryActivityPageView, error) {
	if len(page.Results) > discovery.ActivityPageSize || (page.NextBoundary == nil) != (page.NextCursor == "") || page.NextCursor != "" && len(page.NextCursor) != discovery.EncodedCursorLength {
		return discoveryActivityPageView{}, fmt.Errorf("activity page is malformed")
	}
	presentation := discoveryActivityPageView{Results: make([]discoveryActivityResultView, len(page.Results))}
	for index, result := range page.Results {
		if result.PostID <= 0 || !result.CreatedAt.Valid || result.AuthorID <= 0 || result.AuthorName == "" || result.TopicID <= 0 || result.TopicTitle == "" || result.AreaID <= 0 || !policy.ValidAreaSlug(result.AreaSlug) || result.AreaName == "" {
			return discoveryActivityPageView{}, fmt.Errorf("activity result is malformed")
		}
		postURL, err := builder.Path("posts", strconv.FormatInt(result.PostID, 10))
		if err != nil {
			return discoveryActivityPageView{}, err
		}
		presentation.Results[index] = discoveryActivityResultView{URL: postURL, AreaName: result.AreaName, TopicTitle: result.TopicTitle, AuthorName: result.AuthorName, Created: result.CreatedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST")}
	}
	if page.NextCursor != "" {
		var err error
		presentation.NextURL, err = builder.PathWithQuery([]string{"activity"}, url.Values{"cursor": {page.NextCursor}})
		if err != nil {
			return discoveryActivityPageView{}, err
		}
	}
	return presentation, nil
}

func newDiscoveryDirectPostHandler(builder URLBuilder, direct DiscoveryDirectPostService) (http.Handler, error) {
	baseView, err := newPageView(builder, "Post")
	if err != nil {
		return nil, fmt.Errorf("construct direct-post view: %w", err)
	}
	if direct == nil {
		return nil, fmt.Errorf("direct-post service is required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		postID, ok := directPostIDFromContext(request.Context())
		if !ok {
			serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Post unavailable", "This post is temporarily unavailable.")
			return
		}
		post, loadErr := direct(request.Context(), postID, sessionAuthenticationFromContext(request.Context()).Access)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveDiscoveryFailure(response, request, baseView, http.StatusNotFound, "Page not found", "The requested page does not exist or is not visible to you.")
			} else {
				serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Post unavailable", "This post is temporarily unavailable.")
			}
			return
		}
		presentation, view, buildErr := buildDirectPostPresentation(builder, postID, post)
		if buildErr != nil {
			serveDiscoveryFailure(response, request, baseView, http.StatusServiceUnavailable, "Post unavailable", "This post is temporarily unavailable.")
			return
		}
		renderDiscoveryOrFailure(response, request, view, http.StatusOK, discoveryDirectPostPage(view, presentation), discoveryDirectPostContent(view, presentation))
	}), nil
}

func buildDirectPostPresentation(builder URLBuilder, requestedID int64, post discovery.DirectPost) (discoveryDirectPostView, pageView, error) {
	if post.PostID != requestedID || post.PostID <= 0 || post.AuthorID <= 0 || post.AuthorName == "" || post.TopicID <= 0 || post.TopicTitle == "" || post.AreaID <= 0 || !policy.ValidAreaSlug(post.AreaSlug) || post.AreaName == "" || !post.CreatedAt.Valid || !post.UpdatedAt.Valid || post.Revision <= 0 || post.RendererVersion == "" {
		return discoveryDirectPostView{}, pageView{}, fmt.Errorf("direct post is malformed")
	}
	areaURL, err := builder.Path("areas", post.AreaSlug)
	if err != nil {
		return discoveryDirectPostView{}, pageView{}, err
	}
	topicURL, err := builder.Path("topics", strconv.FormatInt(post.TopicID, 10))
	if err != nil {
		return discoveryDirectPostView{}, pageView{}, err
	}
	permalink, err := builder.Path("posts", strconv.FormatInt(post.PostID, 10))
	if err != nil {
		return discoveryDirectPostView{}, pageView{}, err
	}
	view, err := newPageView(builder, "Post by "+post.AuthorName, "posts", strconv.FormatInt(post.PostID, 10))
	if err != nil {
		return discoveryDirectPostView{}, pageView{}, err
	}
	edited := ""
	if post.EditedAt.Valid {
		edited = "Edited " + post.EditedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST")
	}
	return discoveryDirectPostView{AreaName: post.AreaName, AreaURL: areaURL, TopicTitle: post.TopicTitle, TopicURL: topicURL, Permalink: permalink, AuthorName: post.AuthorName, Created: post.CreatedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST"), Edited: edited, Body: post.Body}, view, nil
}

func renderDiscoveryOrFailure(response http.ResponseWriter, request *http.Request, view pageView, status int, page, fragment templ.Component) {
	err := renderDiscoveryResponse(response, request, status, page, fragment)
	if err == nil {
		return
	}
	if errors.Is(err, errDiscoveryResponseTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		serveDiscoveryFailure(response, request, view, http.StatusServiceUnavailable, "Discovery unavailable", "Discovery is temporarily unavailable.")
		return
	}
	panic(err)
}

// serveDiscoveryFailure emits one fixed, input-free HTML error in both full
// and HTMX modes.
func serveDiscoveryFailure(response http.ResponseWriter, request *http.Request, view pageView, status int, heading, message string) {
	view.CanonicalURL = ""
	response.Header().Set(discoveryResponseHeader, "1")
	failureRequest := request.Clone(context.WithoutCancel(request.Context()))
	if err := renderResponse(response, failureRequest, status, errorPage(view, status, heading, message), errorContent(view, status, heading, message)); err != nil {
		panic(err)
	}
}
