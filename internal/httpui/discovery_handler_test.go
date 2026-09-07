package httpui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/discovery"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestDiscoverySearchRendersCanonicalFullAndHTMXPages(t *testing.T) {
	t.Parallel()

	now := pgtype.Timestamptz{Time: time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC), Valid: true}
	services := DiscoveryHTTPServices{
		Search: func(_ context.Context, request discovery.SearchRequest, access auth.AccessContext) (discovery.SearchPage, error) {
			if request.Query != "café" || request.AuthorID != 2 || request.Page != 1 || !access.Valid() {
				t.Fatalf("search input = %+v, access %+v", request, access)
			}
			return discovery.SearchPage{Results: []discovery.SearchResult{
				{Kind: "topic", ID: 11, AreaID: 3, AreaSlug: "public", AreaName: "Public", TopicID: 11, TopicTitle: "A topic", AuthorID: 2, AuthorName: "Alice", CreatedAt: now},
				{Kind: "post", ID: 12, PostID: 12, AreaID: 3, AreaSlug: "public", AreaName: "Public", TopicID: 11, TopicTitle: "A topic", AuthorID: 2, AuthorName: "Alice", CreatedAt: now, Excerpt: `<script>alert(1)</script> & safe`},
			}, HasNextPage: true, BeyondWindow: true}, nil
		},
		Activity:   unavailableActivityService,
		DirectPost: unavailableDirectPostService,
	}
	handler := mustDiscoveryHTTPHandler(t, "/bb", services)

	for _, htmx := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/search?q=cafe%CC%81&author=2", nil)
		if htmx {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, `value="café"`) || !strings.Contains(body, `value="2"`) ||
			strings.Contains(body, `<script>alert(1)</script>`) || !strings.Contains(body, `&lt;script&gt;alert(1)&lt;/script&gt; &amp; safe`) ||
			!strings.Contains(body, `/bb/search?author=2&amp;page=2&amp;q=caf%C3%A9`) || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("HTMX=%t response = (%d, %v, %q)", htmx, response.Code, response.Header(), body)
		}
		if !htmx && !strings.Contains(body, `https://forum.example.test/bb/search?author=2&amp;q=caf%C3%A9`) {
			t.Fatalf("full page omitted canonical URL: %q", body)
		}
		if strings.Contains(body, "50 best") || !strings.Contains(body, "newest 50 authorized matches") {
			t.Fatalf("search scope label is dishonest: %q", body)
		}
		if htmx == strings.Contains(body, "<!doctype html>") {
			t.Fatalf("HTMX=%t document envelope mismatch", htmx)
		}
	}
}

func TestDiscoverySearchMapsFixedStatuses(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		page   discovery.SearchPage
		err    error
		target string
		status int
	}{
		{name: "semantic query", err: discovery.ErrInvalidSearchQuery, target: "/search?q=-term", status: http.StatusBadRequest},
		{name: "empty second page", target: "/search?author=2&page=2", status: http.StatusNotFound},
		{name: "store failure", err: errors.New("contains secret-filter"), target: "/search?q=secret-filter", status: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			services := DiscoveryHTTPServices{
				Search: func(context.Context, discovery.SearchRequest, auth.AccessContext) (discovery.SearchPage, error) {
					return test.page, test.err
				},
				Activity: unavailableActivityService, DirectPost: unavailableDirectPostService,
			}
			response := httptest.NewRecorder()
			mustDiscoveryHTTPHandler(t, "", services).ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.target, nil))
			if response.Code != test.status || strings.Contains(response.Body.String(), "secret-filter") {
				t.Fatalf("response = (%d, %q), want %d and redaction", response.Code, response.Body.String(), test.status)
			}
		})
	}
}

func TestDiscoveryActivityAndDirectPostRenderBoundedCanonicalViews(t *testing.T) {
	t.Parallel()

	now := pgtype.Timestamptz{Time: time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC), Valid: true}
	next := repeatByte('A', discovery.EncodedCursorLength)
	services := DiscoveryHTTPServices{
		Search: unavailableSearchService,
		Activity: func(_ context.Context, cursor *discovery.AuthenticatedCursor, access auth.AccessContext) (discovery.ActivityPage, error) {
			if cursor != nil || !access.Valid() {
				t.Fatalf("activity input cursor=%v access=%+v", cursor, access)
			}
			return discovery.ActivityPage{Results: []discovery.ActivityResult{{PostID: 9, CreatedAt: now, AuthorID: 2, AuthorName: "Alice", TopicID: 3, TopicTitle: "Topic", AreaID: 4, AreaSlug: "area", AreaName: "Area"}}, NextBoundary: &discovery.ActivityBoundary{CreatedAt: now.Time, PostID: 9}, NextCursor: next}, nil
		},
		DirectPost: func(_ context.Context, postID int64, access auth.AccessContext) (discovery.DirectPost, error) {
			if postID != 9 || !access.Valid() {
				t.Fatalf("direct input post=%d access=%+v", postID, access)
			}
			return discovery.DirectPost{PostID: 9, CreatedAt: now, UpdatedAt: now, Revision: 1, Body: contentrender.SanitizeHTML(`<p>Safe <strong>post</strong></p><script>bad</script>`), RendererVersion: "p2", AuthorID: 2, AuthorName: "Alice", TopicID: 3, TopicTitle: "Topic", AreaID: 4, AreaSlug: "area", AreaName: "Area"}, nil
		},
	}
	handler := mustDiscoveryHTTPHandler(t, "/bb", services)

	activity := httptest.NewRecorder()
	handler.ServeHTTP(activity, httptest.NewRequest(http.MethodGet, "/activity", nil))
	if activity.Code != http.StatusOK || !strings.Contains(activity.Body.String(), `/bb/posts/9`) || !strings.Contains(activity.Body.String(), `/bb/activity?cursor=`+next) {
		t.Fatalf("activity response = (%d, %q)", activity.Code, activity.Body.String())
	}

	direct := httptest.NewRecorder()
	handler.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "/posts/9", nil))
	if direct.Code != http.StatusOK || !strings.Contains(direct.Body.String(), `<strong>post</strong>`) || strings.Contains(direct.Body.String(), "<script>") ||
		!strings.Contains(direct.Body.String(), `https://forum.example.test/bb/posts/9`) || !strings.Contains(direct.Body.String(), `/bb/topics/3`) {
		t.Fatalf("direct response = (%d, %q)", direct.Code, direct.Body.String())
	}
}

func TestDiscoveryActivityAndDirectPostMapCursorAndExistenceFailures(t *testing.T) {
	t.Parallel()

	services := DiscoveryHTTPServices{
		Search: unavailableSearchService,
		Activity: func(context.Context, *discovery.AuthenticatedCursor, auth.AccessContext) (discovery.ActivityPage, error) {
			return discovery.ActivityPage{}, discovery.ErrInvalidActivityCursor
		},
		DirectPost: func(context.Context, int64, auth.AccessContext) (discovery.DirectPost, error) {
			return discovery.DirectPost{}, pgx.ErrNoRows
		},
	}
	handler := mustDiscoveryHTTPHandler(t, "", services)
	for _, test := range []struct {
		target string
		status int
	}{{"/activity", http.StatusBadRequest}, {"/posts/9", http.StatusNotFound}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.target, nil))
		if response.Code != test.status {
			t.Fatalf("%s status = %d, want %d", test.target, response.Code, test.status)
		}
	}
}

func TestDiscoveryDeadlineReturnsFixedUncommittedFailure(t *testing.T) {
	t.Parallel()

	builder := mustURLBuilder(t, "")
	inner, err := newDiscoveryHandler(builder, DiscoveryHTTPServices{
		Search: unavailableSearchService,
		Activity: func(ctx context.Context, _ *discovery.AuthenticatedCursor, _ auth.AccessContext) (discovery.ActivityPage, error) {
			<-ctx.Done()
			return discovery.ActivityPage{}, ctx.Err()
		},
		DirectPost: unavailableDirectPostService,
	})
	if err != nil {
		t.Fatalf("newDiscoveryHandler() returned error: %v", err)
	}
	handler, err := newDiscoveryPreflightHandlerWithTimeouts(
		builder, inner,
		func(string) (discovery.AuthenticatedCursor, error) { return discovery.AuthenticatedCursor{}, nil },
		discoveryTimeouts{search: 30 * time.Millisecond, activity: 20 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandlerWithTimeouts() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/activity", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "Recent activity is temporarily unavailable.") || strings.Contains(response.Body.String(), "deadline") {
		t.Fatalf("deadline response = (%d, %q)", response.Code, response.Body.String())
	}
}

func TestDiscoveryHandlersFailClosedOnMalformedServiceOutput(t *testing.T) {
	t.Parallel()

	now := pgtype.Timestamptz{Time: time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC), Valid: true}
	searchResult := discovery.SearchResult{Kind: "post", ID: 9, PostID: 9, AreaID: 4, AreaSlug: "area", AreaName: "Area", TopicID: 3, TopicTitle: "Topic", AuthorID: 2, AuthorName: "Alice", CreatedAt: now}
	activityResult := discovery.ActivityResult{PostID: 9, CreatedAt: now, AuthorID: 2, AuthorName: "Alice", TopicID: 3, TopicTitle: "Topic", AreaID: 4, AreaSlug: "area", AreaName: "Area"}
	directPost := discovery.DirectPost{PostID: 9, CreatedAt: now, UpdatedAt: now, Revision: 1, Body: contentrender.SanitizeHTML("<p>body</p>"), RendererVersion: "p2", AuthorID: 2, AuthorName: "Alice", TopicID: 3, TopicTitle: "Topic", AreaID: 4, AreaSlug: "area", AreaName: "Area"}
	for _, test := range []struct {
		name     string
		target   string
		services DiscoveryHTTPServices
	}{
		{name: "too many search rows", target: "/search?q=term", services: DiscoveryHTTPServices{
			Search: func(context.Context, discovery.SearchRequest, auth.AccessContext) (discovery.SearchPage, error) {
				return discovery.SearchPage{Results: make([]discovery.SearchResult, discovery.SearchPageSize+1)}, nil
			}, Activity: unavailableActivityService, DirectPost: unavailableDirectPostService,
		}},
		{name: "malformed search row", target: "/search?q=term", services: DiscoveryHTTPServices{
			Search: func(context.Context, discovery.SearchRequest, auth.AccessContext) (discovery.SearchPage, error) {
				row := searchResult
				row.AreaSlug = "INVALID"
				return discovery.SearchPage{Results: []discovery.SearchResult{row}}, nil
			}, Activity: unavailableActivityService, DirectPost: unavailableDirectPostService,
		}},
		{name: "oversize rendered search", target: "/search?q=term", services: DiscoveryHTTPServices{
			Search: func(context.Context, discovery.SearchRequest, auth.AccessContext) (discovery.SearchPage, error) {
				row := searchResult
				row.TopicTitle = strings.Repeat("x", maximumDiscoveryResponseBytes)
				return discovery.SearchPage{Results: []discovery.SearchResult{row}}, nil
			}, Activity: unavailableActivityService, DirectPost: unavailableDirectPostService,
		}},
		{name: "malformed activity page", target: "/activity", services: DiscoveryHTTPServices{
			Search: unavailableSearchService,
			Activity: func(context.Context, *discovery.AuthenticatedCursor, auth.AccessContext) (discovery.ActivityPage, error) {
				return discovery.ActivityPage{Results: []discovery.ActivityResult{activityResult}, NextCursor: repeatByte('A', discovery.EncodedCursorLength)}, nil
			}, DirectPost: unavailableDirectPostService,
		}},
		{name: "malformed activity row", target: "/activity", services: DiscoveryHTTPServices{
			Search: unavailableSearchService,
			Activity: func(context.Context, *discovery.AuthenticatedCursor, auth.AccessContext) (discovery.ActivityPage, error) {
				row := activityResult
				row.PostID = 0
				return discovery.ActivityPage{Results: []discovery.ActivityResult{row}}, nil
			}, DirectPost: unavailableDirectPostService,
		}},
		{name: "direct service failure", target: "/posts/9", services: DiscoveryHTTPServices{
			Search: unavailableSearchService, Activity: unavailableActivityService,
			DirectPost: func(context.Context, int64, auth.AccessContext) (discovery.DirectPost, error) {
				return discovery.DirectPost{}, errors.New("private direct failure")
			},
		}},
		{name: "malformed direct row", target: "/posts/9", services: DiscoveryHTTPServices{
			Search: unavailableSearchService, Activity: unavailableActivityService,
			DirectPost: func(context.Context, int64, auth.AccessContext) (discovery.DirectPost, error) {
				row := directPost
				row.PostID = 10
				return row, nil
			},
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			mustDiscoveryHTTPHandler(t, "", test.services).ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.target, nil))
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private direct failure") || response.Body.Len() > maximumDiscoveryResponseBytes {
				t.Fatalf("response = (%d, %d bytes, %q)", response.Code, response.Body.Len(), response.Body.String())
			}
		})
	}
}

func TestDiscoveryHandlerRejectsIncompleteConstruction(t *testing.T) {
	t.Parallel()

	valid := DiscoveryHTTPServices{Search: unavailableSearchService, Activity: unavailableActivityService, DirectPost: unavailableDirectPostService}
	for _, services := range []DiscoveryHTTPServices{
		{Activity: valid.Activity, DirectPost: valid.DirectPost},
		{Search: valid.Search, DirectPost: valid.DirectPost},
		{Search: valid.Search, Activity: valid.Activity},
	} {
		if handler, err := newDiscoveryHandler(mustURLBuilder(t, ""), services); err == nil || handler != nil {
			t.Fatalf("newDiscoveryHandler() = (%v, %v), want rejected", handler, err)
		}
	}
	if handler, err := newDiscoveryHandler(URLBuilder{}, valid); err == nil || handler != nil {
		t.Fatalf("newDiscoveryHandler(invalid builder) = (%v, %v)", handler, err)
	}
}

func TestDiscoveryCanonicalPresentationPreservesAllFiltersAndEditedPost(t *testing.T) {
	t.Parallel()

	builder := mustURLBuilder(t, "/bb")
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	toExclusive := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	request := discovery.SearchRequest{Query: "term", AuthorID: 2, AreaSlug: "general", From: from, ToExclusive: toExclusive, Page: 2}
	presentation, err := buildSearchPresentation(builder, request, discovery.SearchPage{})
	if err != nil || presentation.PreviousURL != "/bb/search?area=general&author=2&from=2026-08-01&q=term&to=2026-08-31" || presentation.To != "2026-08-31" {
		t.Fatalf("ordinary filters = (%+v, %v)", presentation, err)
	}
	maximum := discovery.SearchRequest{AuthorID: 2, ToExclusive: time.Date(9999, 12, 31, 23, 59, 59, 999_999_000, time.UTC), ToInclusiveMaximum: true, Page: 1}
	values := canonicalSearchQuery(maximum, false)
	if values.Get("to") != "9999-12-31" || values.Get("page") != "" {
		t.Fatalf("maximum-date canonical query = %q", values.Encode())
	}

	now := pgtype.Timestamptz{Time: time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC), Valid: true}
	edited := pgtype.Timestamptz{Time: now.Time.Add(time.Minute), Valid: true}
	post := discovery.DirectPost{PostID: 9, CreatedAt: now, UpdatedAt: edited, EditedAt: edited, Revision: 2, Body: contentrender.SanitizeHTML("<p>body</p>"), RendererVersion: "p2", AuthorID: 2, AuthorName: "Alice", TopicID: 3, TopicTitle: "Topic", AreaID: 4, AreaSlug: "general", AreaName: "General"}
	direct, view, err := buildDirectPostPresentation(builder, 9, post)
	if err != nil || direct.Edited == "" || direct.Permalink != "/bb/posts/9" || view.CanonicalURL != "https://forum.example.test/bb/posts/9" {
		t.Fatalf("edited direct post = (%+v, %+v, %v)", direct, view, err)
	}
}

func mustDiscoveryHTTPHandler(t *testing.T, basePath string, services DiscoveryHTTPServices) http.Handler {
	t.Helper()
	builder := mustURLBuilder(t, basePath)
	inner, err := newDiscoveryHandler(builder, services)
	if err != nil {
		t.Fatalf("newDiscoveryHandler() returned error: %v", err)
	}
	outer, err := newDiscoveryPreflightHandler(builder, inner, func(string) (discovery.AuthenticatedCursor, error) {
		return discovery.AuthenticatedCursor{}, nil
	})
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandler() returned error: %v", err)
	}
	return outer
}

func unavailableSearchService(context.Context, discovery.SearchRequest, auth.AccessContext) (discovery.SearchPage, error) {
	return discovery.SearchPage{}, errors.New("unavailable")
}

func unavailableActivityService(context.Context, *discovery.AuthenticatedCursor, auth.AccessContext) (discovery.ActivityPage, error) {
	return discovery.ActivityPage{}, errors.New("unavailable")
}

func unavailableDirectPostService(context.Context, int64, auth.AccessContext) (discovery.DirectPost, error) {
	return discovery.DirectPost{}, errors.New("unavailable")
}
