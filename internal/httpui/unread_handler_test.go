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

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/jackc/pgx/v5"
)

func TestUnreadPreflightRejectsMalformedRequestsBeforeServiceWork(t *testing.T) {
	t.Parallel()

	calls := 0
	handler := unreadTestHandler(t, UnreadHTTPServices{
		FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
			calls++
			return forum.FirstUnreadTarget{}, nil
		},
		MarkRead: func(context.Context, auth.AccessContext, int64) error {
			calls++
			return nil
		},
	})
	for _, test := range []struct {
		method string
		target string
		status int
	}{
		{method: http.MethodGet, target: "/topics/041/unread", status: http.StatusNotFound},
		{method: http.MethodGet, target: "/topics/41/unread?x=1", status: http.StatusBadRequest},
		{method: http.MethodPost, target: "/topics/41/read?", status: http.StatusBadRequest},
		{method: http.MethodPost, target: "/topics/0/read", status: http.StatusNotFound},
		{method: http.MethodPut, target: "/topics/41/read", status: http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
		if response.Code != test.status || calls != 0 || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s %s = (status %d calls %d cache %q)", test.method, test.target, response.Code, calls, response.Header().Get("Cache-Control"))
		}
	}
}

func TestAuthenticatedUnreadDispatchKeepsPreflightOutsideSessionLookup(t *testing.T) {
	t.Parallel()

	service := &authenticatedHandlerTestService{}
	service.authenticate = func(context.Context, string) (auth.SessionAuthentication, error) {
		panic("malformed unread request must not authenticate")
	}
	unread := UnreadHTTPServices{
		FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
			panic("malformed request must not query")
		},
		MarkRead: func(context.Context, auth.AccessContext, int64) error { panic("malformed request must not write") },
	}
	handler, err := newAuthenticatedHandler(
		callbackTestURLBuilder(t), service, emptyAreaIndexLister, panicAreaTopicPageLoader, 10_000,
		panicTopicPostPageLoader, 10_000, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, &unread, url.URL{}, false, nil, nil, "gotth_bb_session", true, unavailableReadiness,
	)
	if err != nil {
		t.Fatalf("newAuthenticatedHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/topics/41/unread?probe=1", nil)
	request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: validCSRFTokenForTest(0x61)})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("preflight status = %d, want 400", response.Code)
	}
}

func TestFirstUnreadRequiresCurrentSessionWithoutTopicProbe(t *testing.T) {
	t.Parallel()

	calls := 0
	handler := unreadTestHandler(t, UnreadHTTPServices{
		FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
			calls++
			return forum.FirstUnreadTarget{}, nil
		},
		MarkRead: func(context.Context, auth.AccessContext, int64) error { panic("mark-read must not run") },
	})
	for _, test := range []struct {
		name           string
		authentication auth.SessionAuthentication
		wantLocation   string
	}{
		{name: "missing", wantLocation: "/bb/login?return=%2Fbb%2Ftopics%2F41%2Funread"},
		{name: "stale", authentication: auth.SessionAuthentication{
			SessionID: 7, Access: unreadTestAccess(), RequiresRevalidation: true,
		}, wantLocation: "/bb/auth/revalidate?return=%2Fbb%2Ftopics%2F41%2Funread"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := unreadTestRequest(http.MethodGet, "/topics/41/unread", test.authentication, "")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.wantLocation || calls != 0 {
				t.Fatalf("session response = (status %d location %q calls %d)", response.Code, response.Header().Get("Location"), calls)
			}
		})
	}
}

func TestMarkReadRequiresCurrentSessionWithoutBodyOrTopicProbe(t *testing.T) {
	t.Parallel()

	calls := 0
	handler := unreadTestHandler(t, UnreadHTTPServices{
		FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
			panic("first-unread must not run")
		},
		MarkRead: func(context.Context, auth.AccessContext, int64) error {
			calls++
			return nil
		},
	})
	for _, test := range []struct {
		name           string
		authentication auth.SessionAuthentication
		wantLocation   string
	}{
		{name: "missing", wantLocation: "/bb/login?return=%2Fbb%2Ftopics%2F41"},
		{name: "stale", authentication: auth.SessionAuthentication{
			SessionID: 7, Access: unreadTestAccess(), RequiresRevalidation: true,
		}, wantLocation: "/bb/auth/revalidate?return=%2Fbb%2Ftopics%2F41"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := unreadTestRequest(http.MethodPost, "/topics/41/read", test.authentication, "")
			request.Body = panicCSRFBody{}
			request.ContentLength = -1
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.wantLocation || calls != 0 {
				t.Fatalf("session response = (status %d location %q calls %d)", response.Code, response.Header().Get("Location"), calls)
			}
		})
	}
}

func TestFirstUnreadNavigatesToExactTarget(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		target     forum.FirstUnreadTarget
		htmx       bool
		wantStatus int
		wantHeader string
		wantValue  string
	}{
		{name: "none", wantStatus: http.StatusSeeOther, wantHeader: "Location", wantValue: "/bb/topics/41"},
		{name: "first page", target: forum.FirstUnreadTarget{PostID: 91, Page: 1}, wantStatus: http.StatusSeeOther, wantHeader: "Location", wantValue: "/bb/topics/41#post-91"},
		{name: "later page", target: forum.FirstUnreadTarget{PostID: 92, Page: 3}, wantStatus: http.StatusSeeOther, wantHeader: "Location", wantValue: "/bb/topics/41?page=3#post-92"},
		{name: "direct htmx", target: forum.FirstUnreadTarget{PostID: 93, Direct: true}, htmx: true, wantStatus: http.StatusNoContent, wantHeader: "HX-Location", wantValue: `{"path":"/bb/posts/93","target":"#main-content","swap":"outerHTML"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := unreadTestHandler(t, UnreadHTTPServices{
				FirstUnread: func(ctx context.Context, actor auth.AccessContext, topicID int64) (forum.FirstUnreadTarget, error) {
					calls++
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > firstUnreadTimeout || !reflect.DeepEqual(actor, unreadTestAccess()) || topicID != 41 {
						t.Fatalf("first-unread authority/deadline = (%+v, %d, %v, %v)", actor, topicID, deadline, ok)
					}
					return test.target, nil
				},
				MarkRead: func(context.Context, auth.AccessContext, int64) error { panic("mark-read must not run") },
			})
			request := unreadTestRequest(http.MethodGet, "/topics/41/unread", unreadTestAuthentication(), "")
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get(test.wantHeader) != test.wantValue || response.Body.Len() != 0 || calls != 1 {
				t.Fatalf("navigation = (status %d %s %q body %q calls %d)", response.Code, test.wantHeader, response.Header().Get(test.wantHeader), response.Body.String(), calls)
			}
		})
	}
}

func TestFirstUnreadMapsStoreAndMalformedTargetFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		target forum.FirstUnreadTarget
		err    error
		status int
	}{
		{name: "missing", err: pgx.ErrNoRows, status: http.StatusNotFound},
		{name: "database", err: errors.New("forced database failure"), status: http.StatusServiceUnavailable},
		{name: "malformed", target: forum.FirstUnreadTarget{PostID: 91, Page: 1, Direct: true}, status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := unreadTestHandler(t, UnreadHTTPServices{
				FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
					return test.target, test.err
				},
				MarkRead: func(context.Context, auth.AccessContext, int64) error { panic("mark-read must not run") },
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, unreadTestRequest(http.MethodGet, "/topics/41/unread", unreadTestAuthentication(), ""))
			if response.Code != test.status || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("failure = (status %d cache %q)", response.Code, response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestMarkReadAcceptsOnlyExactHeaderOrFormCSRF(t *testing.T) {
	t.Parallel()

	token := validCSRFTokenForTest(0x71)
	for _, test := range []struct {
		name       string
		body       string
		content    string
		header     []string
		htmx       bool
		wantStatus int
		wantCalls  int
	}{
		{name: "header", header: []string{token}, wantStatus: http.StatusSeeOther, wantCalls: 1},
		{name: "form htmx", body: "_csrf=" + token, content: "application/x-www-form-urlencoded", htmx: true, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "header body", body: "x", header: []string{token}, wantStatus: http.StatusBadRequest},
		{name: "extra form field", body: "_csrf=" + token + "&topic=41", content: "application/x-www-form-urlencoded", wantStatus: http.StatusBadRequest},
		{name: "bad token", header: []string{validCSRFTokenForTest(0x72)}, wantStatus: http.StatusForbidden},
		{name: "duplicate header", header: []string{token, token}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := unreadTestHandler(t, UnreadHTTPServices{
				FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
					panic("first-unread must not run")
				},
				MarkRead: func(ctx context.Context, actor auth.AccessContext, topicID int64) error {
					calls++
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > markReadTimeout || !reflect.DeepEqual(actor, unreadTestAccess()) || topicID != 41 {
						t.Fatalf("mark-read authority/deadline = (%+v, %d, %v, %v)", actor, topicID, deadline, ok)
					}
					return nil
				},
			})
			request := unreadTestRequest(http.MethodPost, "/topics/41/read", unreadTestAuthentication(), test.body)
			request.Header.Set("Content-Type", test.content)
			for _, value := range test.header {
				request.Header.Add(csrfHeaderName, value)
			}
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || calls != test.wantCalls || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("mark-read = (status %d calls %d cache %q body %q)", response.Code, calls, response.Header().Get("Cache-Control"), response.Body.String())
			}
			if test.wantCalls == 1 {
				if response.Body.Len() != 0 {
					t.Fatalf("success body = %q", response.Body.String())
				}
				if test.htmx && response.Header().Get("HX-Location") != `{"path":"/bb/topics/41","target":"#main-content","swap":"outerHTML"}` {
					t.Fatalf("HX-Location = %q", response.Header().Get("HX-Location"))
				}
				if !test.htmx && response.Header().Get("Location") != "/bb/topics/41" {
					t.Fatalf("Location = %q", response.Header().Get("Location"))
				}
			}
		})
	}
}

func TestMarkReadMapsTopicAndDatabaseFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "missing", err: pgx.ErrNoRows, status: http.StatusNotFound},
		{name: "database", err: errors.New("forced database failure"), status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := unreadTestHandler(t, UnreadHTTPServices{
				FirstUnread: func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error) {
					panic("first-unread must not run")
				},
				MarkRead: func(context.Context, auth.AccessContext, int64) error { return test.err },
			})
			request := unreadTestRequest(http.MethodPost, "/topics/41/read", unreadTestAuthentication(), "")
			request.Header.Set(csrfHeaderName, validCSRFTokenForTest(0x71))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("failure status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

func unreadTestHandler(t *testing.T, services UnreadHTTPServices) http.Handler {
	t.Helper()
	inner, err := newUnreadHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatalf("newUnreadHandler() returned error: %v", err)
	}
	handler, err := newUnreadPreflightHandler(callbackTestURLBuilder(t), inner)
	if err != nil {
		t.Fatalf("newUnreadPreflightHandler() returned error: %v", err)
	}
	return handler
}

func unreadTestRequest(method, target string, authentication auth.SessionAuthentication, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
	ctx = context.WithValue(ctx, csrfTokenContextKey{}, validCSRFTokenForTest(0x71))
	return request.WithContext(ctx)
}

func unreadTestAuthentication() auth.SessionAuthentication {
	return auth.SessionAuthentication{SessionID: 7, Access: unreadTestAccess()}
}

func unreadTestAccess() auth.AccessContext {
	return auth.AccessContext{Authenticated: true, UserID: 11, Role: auth.RoleMember}
}
