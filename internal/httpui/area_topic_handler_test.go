package httpui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAreaTopicListHandlerRendersTypedPageAndFragment(t *testing.T) {
	t.Parallel()

	builder := areaTopicTestBuilder(t)
	wantAccess := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember, GroupIDs: []int64{3, 11}}
	activity := pgtype.Timestamptz{Time: time.Date(2026, time.September, 2, 1, 30, 0, 0, time.UTC), Valid: true}
	wantPage := store.VisibleAreaTopicPage{
		Area: db.Area{ID: 8, Slug: "members", Name: "Members & Friends", Description: "Private <discussion>", Visibility: "groups", PostingMode: "normal"},
		Topics: []db.ListVisibleTopicsByAreaSlugRow{
			{TopicID: 41, Title: "Pinned <welcome>", State: "locked", PinnedAt: pgtype.Timestamptz{Valid: true}, ReplyCount: 4, AuthorDisplayName: "Alice & Bob", LastActivityAt: activity, TotalVisibleTopics: 27},
			{TopicID: 40, Title: "Recent", State: "archived", ReplyCount: 2, AuthorDisplayName: "Carol", LastActivityAt: activity, TotalVisibleTopics: 27},
		},
		Number: 2, TotalTopics: 27, TotalPages: 3,
	}
	for _, hxRequest := range []string{"", "true"} {
		hxRequest := hxRequest
		t.Run("hx="+hxRequest, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler, err := newAreaTopicListHandler(builder, 10000, func(ctx context.Context, access auth.AccessContext, slug string, page int32) (store.VisibleAreaTopicPage, error) {
				calls++
				if ctx == nil || !reflect.DeepEqual(access, wantAccess) || slug != "members" || page != 2 {
					t.Fatalf("loader call = (access %+v, slug %q, page %d)", access, slug, page)
				}
				return wantPage, nil
			})
			if err != nil {
				t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
			}
			request := areaTopicTestRequest("/areas/members?page=2", "members", wantAccess)
			request.Header.Set("HX-Request", hxRequest)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			for _, required := range []string{
				"Members &amp; Friends", "Private &lt;discussion&gt;", "Pinned &lt;welcome&gt;", "Alice &amp; Bob",
				"Locked", "Archived", "Pinned", "4 replies", "Sep 2, 2026 01:30 UTC",
				`href="/bb/topics/41"`, `href="/bb/areas/members"`, `href="/bb/areas/members?page=3"`,
			} {
				if !strings.Contains(body, required) {
					t.Fatalf("area topic response lacks %q: %s", required, body)
				}
			}
			if response.Code != http.StatusOK || calls != 1 || strings.Contains(body, "groups") || strings.Contains(body, "normal") {
				t.Fatalf("area topic response = (status %d, calls %d, body %q)", response.Code, calls, body)
			}
			if gotPage := strings.HasPrefix(body, "<!doctype html>"); gotPage != (hxRequest == "") {
				t.Fatalf("complete page = %t, want %t", gotPage, hxRequest == "")
			}
			if hxRequest == "" && !strings.Contains(body, `href="https://forum.example.test/bb/areas/members?page=2"`) {
				t.Fatalf("complete page lacks canonical page URL: %s", body)
			}
		})
	}
}

func TestAreaTopicListHandlerRendersEmptyFirstPageWithoutPagination(t *testing.T) {
	t.Parallel()

	handler, err := newAreaTopicListHandler(areaTopicTestBuilder(t), 10000, func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
		return store.VisibleAreaTopicPage{Area: db.Area{ID: 3, Slug: "public", Name: "Public"}, Number: 1}, nil
	})
	if err != nil {
		t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, areaTopicTestRequest("/areas/public", "public", auth.AccessContext{}))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "No topics have been published in this area yet") ||
		strings.Contains(body, "Previous") || strings.Contains(body, "Next") ||
		!strings.Contains(body, `href="https://forum.example.test/bb/areas/public"`) {
		t.Fatalf("empty area response = (%d, %q)", response.Code, body)
	}
}

func TestAreaTopicListHandlerRendersOpenHiddenAndSingularReply(t *testing.T) {
	t.Parallel()

	activity := pgtype.Timestamptz{Time: time.Date(2026, time.September, 2, 2, 0, 0, 0, time.UTC), Valid: true}
	handler, err := newAreaTopicListHandler(areaTopicTestBuilder(t), 10000, func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
		return store.VisibleAreaTopicPage{
			Area: db.Area{ID: 3, Slug: "staff", Name: "Staff"},
			Topics: []db.ListVisibleTopicsByAreaSlugRow{
				{TopicID: 1, Title: "Open topic", State: "open", ReplyCount: 1, AuthorDisplayName: "Author", LastActivityAt: activity, TotalVisibleTopics: 2},
				{TopicID: 2, Title: "Hidden topic", State: "hidden", AuthorDisplayName: "Moderator", LastActivityAt: activity, TotalVisibleTopics: 2},
			},
			Number: 1, TotalTopics: 2, TotalPages: 1,
		}, nil
	})
	if err != nil {
		t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, areaTopicTestRequest("/areas/staff", "staff", auth.AccessContext{}))
	body := response.Body.String()
	for _, required := range []string{"Open", "Hidden", "1 reply", "0 replies"} {
		if !strings.Contains(body, required) {
			t.Fatalf("state/reply response lacks %q: %s", required, body)
		}
	}
}

func TestAreaTopicListHandlerBuildsLaterPreviousPage(t *testing.T) {
	t.Parallel()

	activity := pgtype.Timestamptz{Time: time.Date(2026, time.September, 2, 2, 0, 0, 0, time.UTC), Valid: true}
	handler, err := newAreaTopicListHandler(areaTopicTestBuilder(t), 10000, func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
		return store.VisibleAreaTopicPage{
			Area:   db.Area{ID: 3, Slug: "public", Name: "Public"},
			Topics: []db.ListVisibleTopicsByAreaSlugRow{{TopicID: 1, Title: "Topic", State: "open", AuthorDisplayName: "Author", LastActivityAt: activity, TotalVisibleTopics: 100}},
			Number: 3, TotalTopics: 100, TotalPages: 4,
		}, nil
	})
	if err != nil {
		t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, areaTopicTestRequest("/areas/public?page=3", "public", auth.AccessContext{}))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `href="/bb/areas/public?page=2"`) || !strings.Contains(body, `href="/bb/areas/public?page=4"`) {
		t.Fatalf("later page response = (%d, %q)", response.Code, body)
	}
}

func TestAreaTopicListHandlerCollapsesMissingAndRedactsFailures(t *testing.T) {
	t.Parallel()

	secret := "do-not-leak-topic-page-failure"
	for _, test := range []struct {
		name       string
		target     string
		loader     AreaTopicPageLoader
		wantStatus int
		wantText   string
	}{
		{name: "invalid query", target: "/areas/public?page=01", loader: panicAreaTopicPageLoader, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "missing slug", target: "/areas/public", loader: panicAreaTopicPageLoader, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "escaped path", target: "/areas/public%2Fnested", loader: panicAreaTopicPageLoader, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "missing", target: "/areas/public", loader: func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			return store.VisibleAreaTopicPage{}, pgx.ErrNoRows
		}, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "failure", target: "/areas/public", loader: func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			return store.VisibleAreaTopicPage{Area: db.Area{Name: secret}, Topics: []db.ListVisibleTopicsByAreaSlugRow{{Title: secret}}}, errors.New(secret)
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This discussion area is temporarily unavailable"},
		{name: "invalid result", target: "/areas/public", loader: func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			return store.VisibleAreaTopicPage{Area: db.Area{ID: 1, Slug: "public", Name: "Public"}, Topics: []db.ListVisibleTopicsByAreaSlugRow{{TopicID: 1, Title: secret, State: "invented", AuthorDisplayName: "Author", LastActivityAt: pgtype.Timestamptz{Valid: true}, TotalVisibleTopics: 1}}, Number: 1, TotalTopics: 1, TotalPages: 1}, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This discussion area is temporarily unavailable"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newAreaTopicListHandler(areaTopicTestBuilder(t), 10000, test.loader)
			if err != nil {
				t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
			}
			response := httptest.NewRecorder()
			slug := "public"
			if test.name == "missing slug" {
				slug = ""
			} else if test.name == "escaped path" {
				slug = "public%2Fnested"
			}
			handler.ServeHTTP(response, areaTopicTestRequest(test.target, slug, auth.AccessContext{}))
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantText) || strings.Contains(response.Body.String(), secret) {
				t.Fatalf("error response = (%d, %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestAreaTopicListHandlerRejectsMalformedLoadedRows(t *testing.T) {
	t.Parallel()

	validActivity := pgtype.Timestamptz{Time: time.Date(2026, time.September, 2, 2, 0, 0, 0, time.UTC), Valid: true}
	validPage := func() store.VisibleAreaTopicPage {
		return store.VisibleAreaTopicPage{
			Area:   db.Area{ID: 1, Slug: "public", Name: "Public"},
			Topics: []db.ListVisibleTopicsByAreaSlugRow{{TopicID: 1, Title: "Topic", State: "open", AuthorDisplayName: "Author", LastActivityAt: validActivity, TotalVisibleTopics: 1}},
			Number: 1, TotalTopics: 1, TotalPages: 1,
		}
	}
	for _, mutate := range []func(*store.VisibleAreaTopicPage){
		func(page *store.VisibleAreaTopicPage) { page.Area.ID = 0 },
		func(page *store.VisibleAreaTopicPage) { page.Area.Slug = "other" },
		func(page *store.VisibleAreaTopicPage) { page.Area.Name = "" },
		func(page *store.VisibleAreaTopicPage) { page.Number = 2 },
		func(page *store.VisibleAreaTopicPage) { page.TotalTopics = -1 },
		func(page *store.VisibleAreaTopicPage) { page.TotalPages = -1 },
		func(page *store.VisibleAreaTopicPage) { page.Topics = nil },
		func(page *store.VisibleAreaTopicPage) { page.TotalTopics = 0 },
		func(page *store.VisibleAreaTopicPage) { page.TotalPages = 0 },
		func(page *store.VisibleAreaTopicPage) { page.Topics[0].TopicID = 0 },
		func(page *store.VisibleAreaTopicPage) { page.Topics[0].Title = "" },
		func(page *store.VisibleAreaTopicPage) { page.Topics[0].AuthorDisplayName = "" },
		func(page *store.VisibleAreaTopicPage) { page.Topics[0].ReplyCount = -1 },
		func(page *store.VisibleAreaTopicPage) { page.Topics[0].LastActivityAt = pgtype.Timestamptz{} },
		func(page *store.VisibleAreaTopicPage) { page.Topics[0].TotalVisibleTopics = 2 },
	} {
		mutate := mutate
		t.Run("malformed", func(t *testing.T) {
			t.Parallel()
			page := validPage()
			mutate(&page)
			handler, err := newAreaTopicListHandler(areaTopicTestBuilder(t), 10000, func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
				return page, nil
			})
			if err != nil {
				t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, areaTopicTestRequest("/areas/public", "public", auth.AccessContext{}))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "temporarily unavailable") {
				t.Fatalf("malformed response = (%d, %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestAreaTopicListHandlerPropagatesCommittedWriteFailure(t *testing.T) {
	t.Parallel()

	activity := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	for _, loader := range []AreaTopicPageLoader{
		func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			return store.VisibleAreaTopicPage{Area: db.Area{ID: 1, Slug: "public", Name: "Public"}, Topics: []db.ListVisibleTopicsByAreaSlugRow{{TopicID: 1, Title: "Topic", State: "open", AuthorDisplayName: "Author", LastActivityAt: activity, TotalVisibleTopics: 1}}, Number: 1, TotalTopics: 1, TotalPages: 1}, nil
		},
		func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			return store.VisibleAreaTopicPage{}, pgx.ErrNoRows
		},
		func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
			return store.VisibleAreaTopicPage{}, errors.New("store failed")
		},
	} {
		handler, err := newAreaTopicListHandler(areaTopicTestBuilder(t), 10000, loader)
		if err != nil {
			t.Fatalf("newAreaTopicListHandler() returned error: %v", err)
		}
		writer := &failingRenderResponseWriter{header: make(http.Header), cause: errTestResponseWrite}
		recovered := captureHandlerPanic(func() {
			handler.ServeHTTP(writer, areaTopicTestRequest("/areas/public", "public", auth.AccessContext{}))
		})
		if !errors.Is(asError(recovered), errTestResponseWrite) {
			t.Fatalf("area topic panic = %v, want write cause", recovered)
		}
	}
}

func TestNewAreaTopicListHandlerRejectsDependencies(t *testing.T) {
	t.Parallel()

	builder := areaTopicTestBuilder(t)
	for _, test := range []struct {
		builder URLBuilder
		maximum int32
		loader  AreaTopicPageLoader
	}{
		{maximum: 10000, loader: panicAreaTopicPageLoader},
		{builder: builder, loader: panicAreaTopicPageLoader},
		{builder: builder, maximum: 10000},
	} {
		if got, err := newAreaTopicListHandler(test.builder, test.maximum, test.loader); err == nil || got != nil {
			t.Fatalf("newAreaTopicListHandler() = (%v, %v), want nil/error", got, err)
		}
	}
}

func areaTopicTestBuilder(t *testing.T) URLBuilder {
	t.Helper()
	publicBase, err := url.Parse("https://forum.example.test/bb")
	if err != nil {
		t.Fatalf("url.Parse() returned error: %v", err)
	}
	builder, err := NewURLBuilder(*publicBase, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	return builder
}

func areaTopicTestRequest(target, slug string, access auth.AccessContext) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("slug", slug)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, sessionAuthenticationContextKey{}, auth.SessionAuthentication{Access: access})
	return request.WithContext(ctx)
}

var panicAreaTopicPageLoader AreaTopicPageLoader = func(context.Context, auth.AccessContext, string, int32) (store.VisibleAreaTopicPage, error) {
	panic("area topic page loader must not run")
}
