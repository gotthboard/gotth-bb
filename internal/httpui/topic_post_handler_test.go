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
				`href="/bb/areas/members"`, `href="/bb/posts/126/edit"`, `<strong>safe</strong>`, "Edited Sep 2, 2026 02:30 UTC",
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
	for index := range page.Rows {
		page.Rows[index].TopicState = "open"
	}
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

func TestTopicPostListHandlerOffersDeleteButNotEditAtExhaustedRevision(t *testing.T) {
	t.Parallel()

	page := topicPostTestPage(1)
	page.Rows = page.Rows[:1]
	page.Rows[0].Revision.Int32 = int32(1<<31 - 1)
	handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		return page, nil
	})
	if err != nil {
		t.Fatalf("newTopicPostListHandler() returned error: %v", err)
	}
	request := topicPostTestRequest("/topics/42", "42", auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember})
	request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, validCSRFTokenForTest(0x51)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, `href="/bb/posts/101/edit"`) ||
		!strings.Contains(body, `action="/bb/posts/101/delete"`) || !strings.Contains(body, `name="revision" value="2147483647"`) {
		t.Fatalf("exhausted revision actions = (%d, %q)", response.Code, body)
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

func TestTopicPostListHandlerShowsStateValidModerationControlsOnlyToActiveStaff(t *testing.T) {
	t.Parallel()

	mute := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, state string
		access      auth.AccessContext
		wantActions []string
		wantLabels  []string
	}{
		{name: "moderator locks or hides open", state: "open", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}, wantActions: []string{`/bb/topics/42/lock`, `/bb/topics/42/hide`}, wantLabels: []string{"Lock topic", "Hide topic"}},
		{name: "administrator unlocks locked", state: "locked", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}, wantActions: []string{`/bb/topics/42/unlock`}, wantLabels: []string{"Unlock topic"}},
		{name: "moderator restores hidden", state: "hidden", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}, wantActions: []string{`/bb/topics/42/restore`}, wantLabels: []string{"Restore topic"}},
		{name: "archived", state: "archived", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}},
		{name: "member", state: "open", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}},
		{name: "muted moderator", state: "open", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator, MutedUntil: &mute}},
		{name: "suspended administrator", state: "open", access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator, Suspended: true}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			page := topicPostTestPage(1)
			page.Rows = page.Rows[:1]
			page.Rows[0].TopicState = test.state
			page.Rows[0].TotalVisiblePosts = 1
			page.TotalPosts, page.TotalPages = 1, 1
			handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
				return page, nil
			})
			if err != nil {
				t.Fatalf("newTopicPostListHandler() returned error: %v", err)
			}
			request := topicPostTestRequest("/topics/42", "42", test.access)
			request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, validCSRFTokenForTest(0x51)))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusOK {
				t.Fatalf("topic response = (%d, %q)", response.Code, body)
			}
			if len(test.wantActions) == 0 {
				if strings.Contains(body, "Moderation reason") || strings.Contains(body, `/topics/42/lock`) || strings.Contains(body, `/topics/42/unlock`) || strings.Contains(body, `/topics/42/hide`) || strings.Contains(body, `/topics/42/restore`) {
					t.Fatalf("unauthorized moderation control: %s", body)
				}
				return
			}
			if strings.Count(body, "Moderation reason") != len(test.wantActions) || strings.Count(body, `name="reason"`) != len(test.wantActions) {
				t.Fatalf("moderation form count = %d, want %d: %s", strings.Count(body, "Moderation reason"), len(test.wantActions), body)
			}
			for _, action := range test.wantActions {
				for _, required := range []string{`action="` + action + `"`, `hx-post="` + action + `"`} {
					if !strings.Contains(body, required) {
						t.Fatalf("moderation control lacks %q: %s", required, body)
					}
				}
			}
			for _, required := range append([]string{`name="_csrf" value="` + validCSRFTokenForTest(0x51) + `"`}, test.wantLabels...) {
				if !strings.Contains(body, required) {
					t.Fatalf("moderation control lacks %q: %s", required, body)
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

func TestTopicPostListHandlerRendersThreadContextAndContentFreeTombstone(t *testing.T) {
	t.Parallel()

	page := topicPostTestPage(1)
	for index := range page.Rows {
		page.Rows[index].TopicState = "open"
	}
	page.Rows[0].IsTombstone.Bool = true
	page.Rows[0].RenderedHtml = pgtype.Text{}
	page.Rows[0].RendererVersion = pgtype.Text{}
	page.Rows[0].Revision = pgtype.Int4{}
	page.Rows[0].PostCreatedAt = pgtype.Timestamptz{}
	page.Rows[0].PostUpdatedAt = pgtype.Timestamptz{}
	page.Rows[0].PostEditedAt = pgtype.Timestamptz{}
	page.Rows[0].PostAuthorID = pgtype.Int8{}
	page.Rows[0].PostAuthorDisplayName = pgtype.Text{}
	page.Rows[1].ParentAuthorDisplayName = pgtype.Text{}
	handler, err := newTopicPostListHandler(areaTopicTestBuilder(t), store.MaximumPostPage, func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error) {
		return page, nil
	})
	if err != nil {
		t.Fatalf("newTopicPostListHandler() returned error: %v", err)
	}
	request := topicPostTestRequest("/topics/42", "42", auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember})
	request = request.WithContext(context.WithValue(request.Context(), csrfTokenContextKey{}, validCSRFTokenForTest(0x51)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "This post was deleted") ||
		!strings.Contains(body, "In reply to deleted post #1") || !strings.Contains(body, `name="parent_post_id" value="127"`) ||
		strings.Contains(body, `name="parent_post_id" value="101"`) || strings.Contains(body, "Hello <strong>safe</strong>") {
		t.Fatalf("thread tombstone response = (%d, %q)", response.Code, body)
	}
}

func TestThreadIndentClassCapsVisualDepth(t *testing.T) {
	t.Parallel()

	if threadIndentClass(1) != "" || threadIndentClass(2) == "" || threadIndentClass(7) != threadIndentClass(32) {
		t.Fatalf("thread indentation mapping is not bounded")
	}
}

func topicPostTestPage(number int32) store.VisibleTopicPostPage {
	topicCreated := time.Date(2026, time.September, 1, 20, 0, 0, 0, time.UTC)
	postCreated := time.Date(2026, time.September, 2, 2, 0, 0, 0, time.UTC)
	base := db.GetVisibleTopicPostPageRow{
		AreaID: 8, AreaSlug: "members", AreaName: "Members & Friends", AreaDescription: "Private <discussion>", AreaPostingMode: "normal",
		TopicID: 42, TopicFirstPostID: 101, TopicTitle: "Welcome <everyone>", TopicState: "locked",
		TopicPinnedAt:  pgtype.Timestamptz{Time: topicCreated, Valid: true},
		TopicCreatedAt: pgtype.Timestamptz{Time: topicCreated, Valid: true}, TopicAuthorDisplayName: "Alice & Bob",
		PostID: pgtype.Int8{Int64: 126, Valid: true}, PostNumber: pgtype.Int4{Int32: 26, Valid: true},
		ParentPostID: pgtype.Int8{Int64: 101, Valid: true}, ThreadDepth: 2, IsTombstone: pgtype.Bool{Bool: false, Valid: true},
		RenderedHtml:    pgtype.Text{String: `<p onclick="alert(1)">Hello <strong>safe</strong><script>alert(1)</script></p>`, Valid: true},
		RendererVersion: pgtype.Text{String: "renderer-v1", Valid: true}, Revision: pgtype.Int4{Int32: 2, Valid: true},
		PostCreatedAt: pgtype.Timestamptz{Time: postCreated, Valid: true},
		PostUpdatedAt: pgtype.Timestamptz{Time: postCreated.Add(30 * time.Minute), Valid: true},
		PostEditedAt:  pgtype.Timestamptz{Time: postCreated.Add(30 * time.Minute), Valid: true},
		PostAuthorID:  pgtype.Int8{Int64: 42, Valid: true}, PostAuthorDisplayName: pgtype.Text{String: "Carol <Admin>", Valid: true},
		ParentPostNumber: pgtype.Int4{Int32: 1, Valid: true}, ParentAuthorDisplayName: pgtype.Text{String: "Alice & Bob", Valid: true}, ParentNodeOrdinal: pgtype.Int8{Int64: 1, Valid: true},
		NodeOrdinal: pgtype.Int8{Int64: 26, Valid: true}, TotalVisiblePosts: 27,
	}
	second := base
	second.PostID = pgtype.Int8{Int64: 127, Valid: true}
	second.PostNumber = pgtype.Int4{Int32: 27, Valid: true}
	second.NodeOrdinal = pgtype.Int8{Int64: 27, Valid: true}
	second.Revision = pgtype.Int4{Int32: 1, Valid: true}
	second.PostEditedAt = pgtype.Timestamptz{}
	second.RenderedHtml = pgtype.Text{String: `<p><a href="javascript:alert(1)">unsafe link</a></p>`, Valid: true}
	if number == 1 {
		base.PostID = pgtype.Int8{Int64: 101, Valid: true}
		base.PostNumber = pgtype.Int4{Int32: 1, Valid: true}
		base.ParentPostID = pgtype.Int8{}
		base.ThreadDepth = 1
		base.ParentPostNumber = pgtype.Int4{}
		base.ParentAuthorDisplayName = pgtype.Text{}
		base.ParentNodeOrdinal = pgtype.Int8{}
		base.NodeOrdinal = pgtype.Int8{Int64: 1, Valid: true}
		second.ParentPostID = pgtype.Int8{Int64: 101, Valid: true}
		second.ParentPostNumber = pgtype.Int4{Int32: 1, Valid: true}
		second.ParentAuthorDisplayName = pgtype.Text{String: "Carol <Admin>", Valid: true}
		second.ParentNodeOrdinal = pgtype.Int8{Int64: 1, Valid: true}
		second.NodeOrdinal = pgtype.Int8{Int64: 2, Valid: true}
	}
	return store.VisibleTopicPostPage{Rows: []db.GetVisibleTopicPostPageRow{base, second}, Number: number, TotalPosts: 27, TotalPages: 2}
}

func clearTopicPostTestRow(row *db.GetVisibleTopicPostPageRow) {
	row.PostID = pgtype.Int8{}
	row.PostNumber = pgtype.Int4{}
	row.ParentPostID = pgtype.Int8{}
	row.ThreadDepth = 0
	row.IsTombstone = pgtype.Bool{}
	row.RenderedHtml = pgtype.Text{}
	row.RendererVersion = pgtype.Text{}
	row.Revision = pgtype.Int4{}
	row.PostCreatedAt = pgtype.Timestamptz{}
	row.PostUpdatedAt = pgtype.Timestamptz{}
	row.PostEditedAt = pgtype.Timestamptz{}
	row.PostAuthorID = pgtype.Int8{}
	row.PostAuthorDisplayName = pgtype.Text{}
	row.ParentPostNumber = pgtype.Int4{}
	row.ParentAuthorDisplayName = pgtype.Text{}
	row.ParentNodeOrdinal = pgtype.Int8{}
	row.NodeOrdinal = pgtype.Int8{}
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
