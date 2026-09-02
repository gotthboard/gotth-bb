package httpui

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestTopicPostListHandlerRendersSanitizedPageAndFragment(t *testing.T) {
	t.Parallel()

	wantAccess := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember, GroupIDs: []int64{3, 11}}
	wantPage := topicPostTestPage(2)
	for _, hxRequest := range []string{"", "true"} {
		hxRequest := hxRequest
		t.Run("hx="+hxRequest, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(ctx context.Context, access auth.AccessContext, topicID int64, page int32) (store.VisibleTopicPostPage, error) {
				calls++
				if ctx == nil || !reflect.DeepEqual(access, wantAccess) || topicID != 42 || page != 2 {
					t.Fatalf("loader call = (access %+v, topic %d, page %d)", access, topicID, page)
				}
				return wantPage, nil
			})
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			request := topicPostTestRequest("/topics/42?page=2", "42", wantAccess)
			request.Header.Set("HX-Request", hxRequest)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			for _, required := range []string{
				"Members &amp; Friends", "Welcome &lt;everyone&gt;", "Locked", "Pinned", "Started by Alice &amp; Bob",
				`id="post-126"`, `href="/bb/topics/42?page=2#post-126"`, `href="/bb/topics/42"`,
				`href="/bb/areas/members"`, `<strong>safe</strong>`, "Edited Sep 2, 2026 02:30 UTC",
			} {
				if !strings.Contains(body, required) {
					t.Fatalf("topic response lacks %q: %s", required, body)
				}
			}
			for _, forbidden := range []string{"alert(1)", `onclick=`, "groups", "renderer-v1"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("topic response contains %q: %s", forbidden, body)
				}
			}
			if response.Code != http.StatusOK || calls != 1 {
				t.Fatalf("topic response = (status %d, calls %d, body %q)", response.Code, calls, body)
			}
			if gotPage := strings.HasPrefix(body, "<!doctype html>"); gotPage != (hxRequest == "") {
				t.Fatalf("complete page = %t, want %t", gotPage, hxRequest == "")
			}
			if hxRequest == "" && !strings.Contains(body, `href="https://forum.example.test/bb/topics/42?page=2"`) {
				t.Fatalf("complete page lacks canonical page URL: %s", body)
			}
		})
	}
}

func TestTopicPostListHandlerRendersEmptyTopic(t *testing.T) {
	t.Parallel()

	page := topicPostTestPage(1)
	page.Rows = page.Rows[:1]
	clearTopicPostTestRow(&page.Rows[0])
	page.TotalPosts = 0
	page.TotalPages = 0
	handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		return page, nil
	})
	if err != nil {
		t.Fatalf("newTopicPostListHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, topicPostTestRequest("/topics/42", "42", auth.AccessContext{}))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "No visible posts are available in this topic yet") ||
		strings.Contains(body, "Previous") || strings.Contains(body, "Next") ||
		!strings.Contains(body, `href="https://forum.example.test/bb/topics/42"`) {
		t.Fatalf("empty topic response = (%d, %q)", response.Code, body)
	}
}

func TestTopicPostListHandlerBuildsFirstPagePostAndNextURLs(t *testing.T) {
	t.Parallel()

	page := topicPostTestPage(1)
	page.Rows = page.Rows[:1]
	page.Rows[0].PostID = pgtype.Int8{Int64: 101, Valid: true}
	page.Rows[0].PostNumber = pgtype.Int4{Int32: 1, Valid: true}
	page.Rows[0].TotalVisiblePosts = 26
	page.TotalPosts = 26
	page.TotalPages = 2
	handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		return page, nil
	})
	if err != nil {
		t.Fatalf("newTopicPostListHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, topicPostTestRequest("/topics/42", "42", auth.AccessContext{}))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `href="/bb/topics/42#post-101"`) ||
		!strings.Contains(body, `href="/bb/topics/42?page=2"`) || strings.Contains(body, ">Previous<") {
		t.Fatalf("first topic page response = (%d, %q)", response.Code, body)
	}
}

func TestTopicPostListHandlerRendersStatesAndLaterPagination(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		state      string
		access     auth.AccessContext
		page       int32
		totalPosts int64
		totalPages int64
		wantLabel  string
		wantURLs   []string
	}{
		{name: "open first", state: "open", page: 1, totalPosts: 26, totalPages: 2, wantLabel: "Open", wantURLs: []string{`href="/bb/topics/42?page=2"`}},
		{name: "archived later", state: "archived", page: 3, totalPosts: 100, totalPages: 4, wantLabel: "Archived", wantURLs: []string{`href="/bb/topics/42?page=2"`, `href="/bb/topics/42?page=4"`}},
		{name: "hidden moderator", state: "hidden", access: auth.AccessContext{Authenticated: true, Role: auth.RoleModerator}, page: 1, totalPosts: 1, totalPages: 1, wantLabel: "Hidden"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			page := topicPostTestPage(test.page)
			page.Rows = page.Rows[:1]
			page.Rows[0].TopicState = test.state
			page.Rows[0].TotalVisiblePosts = test.totalPosts
			page.TotalPosts = test.totalPosts
			page.TotalPages = test.totalPages
			handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
				return page, nil
			})
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			target := "/topics/42"
			if test.page > 1 {
				target += "?page=" + string(rune('0'+test.page))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, topicPostTestRequest(target, "42", test.access))
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, test.wantLabel) {
				t.Fatalf("state response = (%d, %q)", response.Code, body)
			}
			for _, wantURL := range test.wantURLs {
				if !strings.Contains(body, wantURL) {
					t.Fatalf("state response lacks %q: %s", wantURL, body)
				}
			}
		})
	}
}

func TestTopicPostListHandlerDoesNotNarrowTotalPageCount(t *testing.T) {
	t.Parallel()

	page := topicPostTestPage(1)
	page.Rows = page.Rows[:1]
	page.TotalPosts = math.MaxInt64
	page.TotalPages = 1 + (math.MaxInt64-1)/int64(store.PostPageSize)
	page.Rows[0].TotalVisiblePosts = math.MaxInt64
	handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		return page, nil
	})
	if err != nil {
		t.Fatalf("newTopicPostListHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, topicPostTestRequest("/topics/42", "42", auth.AccessContext{}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Welcome") {
		t.Fatalf("large total response = (%d, %q)", response.Code, response.Body.String())
	}
}

func TestTopicPostListHandlerCollapsesMissingAndRedactsFailures(t *testing.T) {
	t.Parallel()

	secret := "do-not-leak-topic-post-failure"
	for _, test := range []struct {
		name       string
		target     string
		identifier string
		rawPath    string
		loader     TopicPostPageLoader
		wantStatus int
		wantText   string
	}{
		{name: "leading zero ID", target: "/topics/01", identifier: "01", loader: panicTopicPostPageLoader, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "invalid query", target: "/topics/42?page=01", identifier: "42", loader: panicTopicPostPageLoader, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "escaped path", target: "/topics/%34%32", identifier: "42", rawPath: "/topics/%34%32", loader: panicTopicPostPageLoader, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "missing", target: "/topics/42", identifier: "42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			return store.VisibleTopicPostPage{}, pgx.ErrNoRows
		}, wantStatus: http.StatusNotFound, wantText: "does not exist or is not visible"},
		{name: "failure", target: "/topics/42", identifier: "42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			page := topicPostTestPage(1)
			page.Rows[0].TopicTitle = secret
			return page, errors.New(secret)
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This topic is temporarily unavailable"},
		{name: "malformed", target: "/topics/42", identifier: "42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			return store.VisibleTopicPostPage{Number: 1}, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This topic is temporarily unavailable"},
		{name: "unknown state", target: "/topics/42", identifier: "42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			page := topicPostTestPage(1)
			page.Rows[0].TopicState = "invented"
			return page, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This topic is temporarily unavailable"},
		{name: "hidden visitor", target: "/topics/42", identifier: "42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			page := topicPostTestPage(1)
			for index := range page.Rows {
				page.Rows[index].TopicState = "hidden"
			}
			return page, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This topic is temporarily unavailable"},
		{name: "invalid area slug", target: "/topics/42", identifier: "42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			page := topicPostTestPage(1)
			for index := range page.Rows {
				page.Rows[index].AreaSlug = "."
			}
			return page, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This topic is temporarily unavailable"},
		{name: "inconsistent row", target: "/topics/42", identifier: "42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			page := topicPostTestPage(1)
			page.Rows[1].TopicID = 43
			return page, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "This topic is temporarily unavailable"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, test.loader)
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			request := topicPostTestRequest(test.target, test.identifier, auth.AccessContext{})
			request.URL.RawPath = test.rawPath
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantText) || strings.Contains(response.Body.String(), secret) {
				t.Fatalf("error response = (%d, %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestTopicPostListHandlerRejectsMalformedEmptySentinel(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*db.GetVisibleTopicPostPageRow){
		func(row *db.GetVisibleTopicPostPageRow) { row.TotalVisiblePosts = 1 },
		func(row *db.GetVisibleTopicPostPageRow) { row.PostNumber = pgtype.Int4{Int32: 1, Valid: true} },
		func(row *db.GetVisibleTopicPostPageRow) {
			row.RenderedHtml = pgtype.Text{String: "secret", Valid: true}
		},
		func(row *db.GetVisibleTopicPostPageRow) { row.RendererVersion = pgtype.Text{String: "v1", Valid: true} },
		func(row *db.GetVisibleTopicPostPageRow) { row.Revision = pgtype.Int4{Int32: 1, Valid: true} },
		func(row *db.GetVisibleTopicPostPageRow) {
			row.PostCreatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		},
		func(row *db.GetVisibleTopicPostPageRow) {
			row.PostUpdatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		},
		func(row *db.GetVisibleTopicPostPageRow) {
			row.PostEditedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		},
		func(row *db.GetVisibleTopicPostPageRow) {
			row.PostAuthorDisplayName = pgtype.Text{String: "secret", Valid: true}
		},
	} {
		mutate := mutate
		t.Run("malformed", func(t *testing.T) {
			t.Parallel()
			page := topicPostTestPage(1)
			page.Rows = page.Rows[:1]
			clearTopicPostTestRow(&page.Rows[0])
			mutate(&page.Rows[0])
			page.TotalPosts = 0
			page.TotalPages = 0
			handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
				return page, nil
			})
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, topicPostTestRequest("/topics/42", "42", auth.AccessContext{}))
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("malformed sentinel response = (%d, %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestTopicPostListHandlerPropagatesCommittedWriteFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		target string
		loader TopicPostPageLoader
	}{
		{name: "success", target: "/topics/42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			return topicPostTestPage(1), nil
		}},
		{name: "missing", target: "/topics/42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			return store.VisibleTopicPostPage{}, pgx.ErrNoRows
		}},
		{name: "failure", target: "/topics/42", loader: func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
			return store.VisibleTopicPostPage{}, errors.New("store failed")
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, test.loader)
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			writer := &failingRenderResponseWriter{header: make(http.Header), cause: errTestResponseWrite}
			recovered := captureHandlerPanic(func() {
				handler.ServeHTTP(writer, topicPostTestRequest(test.target, "42", auth.AccessContext{}))
			})
			if !errors.Is(asError(recovered), errTestResponseWrite) {
				t.Fatalf("topic post panic = %v, want write cause", recovered)
			}
		})
	}
}

func TestNewTopicPostListHandlerRejectsDependencies(t *testing.T) {
	t.Parallel()

	builder := areaTopicTestBuilder(t)
	for _, test := range []struct {
		builder URLBuilder
		maximum int32
		loader  TopicPostPageLoader
	}{
		{maximum: store.MaximumPostPage, loader: panicTopicPostPageLoader},
		{builder: builder, loader: panicTopicPostPageLoader},
		{builder: builder, maximum: store.MaximumPostPage},
	} {
		if got, err := newTopicPostListHandler(test.builder, test.maximum, test.loader); err == nil || got != nil {
			t.Fatalf("newTopicPostListHandler() = (%v, %v), want nil/error", got, err)
		}
	}
}

func topicPostTestPage(number int32) store.VisibleTopicPostPage {
	topicCreated := time.Date(2026, time.September, 1, 20, 0, 0, 0, time.UTC)
	postCreated := time.Date(2026, time.September, 2, 2, 0, 0, 0, time.UTC)
	base := db.GetVisibleTopicPostPageRow{
		AreaID: 8, AreaSlug: "members", AreaName: "Members & Friends", AreaDescription: "Private <discussion>",
		TopicID: 42, TopicTitle: "Welcome <everyone>", TopicState: "locked",
		TopicPinnedAt:  pgtype.Timestamptz{Time: topicCreated, Valid: true},
		TopicCreatedAt: pgtype.Timestamptz{Time: topicCreated, Valid: true}, TopicAuthorDisplayName: "Alice & Bob",
		PostID: pgtype.Int8{Int64: 126, Valid: true}, PostNumber: pgtype.Int4{Int32: 26, Valid: true},
		RenderedHtml:    pgtype.Text{String: `<p onclick="alert(1)">Hello <strong>safe</strong><script>alert(1)</script></p>`, Valid: true},
		RendererVersion: pgtype.Text{String: "renderer-v1", Valid: true}, Revision: pgtype.Int4{Int32: 2, Valid: true},
		PostCreatedAt:         pgtype.Timestamptz{Time: postCreated, Valid: true},
		PostUpdatedAt:         pgtype.Timestamptz{Time: postCreated.Add(30 * time.Minute), Valid: true},
		PostEditedAt:          pgtype.Timestamptz{Time: postCreated.Add(30 * time.Minute), Valid: true},
		PostAuthorDisplayName: pgtype.Text{String: "Carol <Admin>", Valid: true}, TotalVisiblePosts: 27,
	}
	second := base
	second.PostID = pgtype.Int8{Int64: 127, Valid: true}
	second.PostNumber = pgtype.Int4{Int32: 27, Valid: true}
	second.Revision = pgtype.Int4{Int32: 1, Valid: true}
	second.PostEditedAt = pgtype.Timestamptz{}
	second.RenderedHtml = pgtype.Text{String: `<p><a href="javascript:alert(1)">unsafe link</a></p>`, Valid: true}
	return store.VisibleTopicPostPage{Rows: []db.GetVisibleTopicPostPageRow{base, second}, Number: number, TotalPosts: 27, TotalPages: 2}
}

func clearTopicPostTestRow(row *db.GetVisibleTopicPostPageRow) {
	row.PostID = pgtype.Int8{}
	row.PostNumber = pgtype.Int4{}
	row.RenderedHtml = pgtype.Text{}
	row.RendererVersion = pgtype.Text{}
	row.Revision = pgtype.Int4{}
	row.PostCreatedAt = pgtype.Timestamptz{}
	row.PostUpdatedAt = pgtype.Timestamptz{}
	row.PostEditedAt = pgtype.Timestamptz{}
	row.PostAuthorDisplayName = pgtype.Text{}
	row.TotalVisiblePosts = 0
}

func topicPostTestRequest(target, identifier string, access auth.AccessContext) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("topicID", identifier)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, sessionAuthenticationContextKey{}, auth.SessionAuthentication{Access: access})
	return request.WithContext(ctx)
}

var panicTopicPostPageLoader TopicPostPageLoader = func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
	panic("topic post page loader must not run")
}
