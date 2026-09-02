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

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

func TestAreaIndexHandlerListsOnlyStoreReturnedAreasForPageAndFragment(t *testing.T) {
	t.Parallel()

	builder, view := areaIndexTestBuilderAndView(t)
	wantAccess := auth.AccessContext{
		Authenticated: true, UserID: 42, Role: auth.RoleMember, GroupIDs: []int64{3, 11},
		ValidatedAt: time.Date(2026, time.September, 2, 1, 0, 0, 0, time.UTC),
	}
	wantAreas := []store.VisibleAreaSummary{
		{
			Area:       db.Area{ID: 5, Slug: "announcements", Name: "Announcements & News", Description: "Durable <updates>", DisplayOrder: 1, Visibility: "public", PostingMode: "read_only"},
			TopicCount: 3, PostCount: 27,
			LatestPost: &store.VisibleAreaLatestPost{
				TopicID: 91, TopicTitle: "Release <notes>", PostID: 117, PostNumber: 26, TreeOrdinal: 3,
				Author: "Ada & Co", CreatedAt: time.Date(2026, time.September, 2, 14, 5, 0, 0, time.FixedZone("CDT", -5*60*60)),
			},
		},
		{Area: db.Area{ID: 8, Slug: "members", Name: "Member discussion", Description: "Private conversation", DisplayOrder: 2, Visibility: "groups", PostingMode: "normal"}},
	}
	for _, hxRequest := range []string{"", "true"} {
		hxRequest := hxRequest
		t.Run("hx="+hxRequest, func(t *testing.T) {
			t.Parallel()

			calls := 0
			handler, err := newAreaIndexHandler(builder, view, func(ctx context.Context, access auth.AccessContext) ([]store.VisibleAreaSummary, error) {
				calls++
				if ctx == nil || !reflect.DeepEqual(access, wantAccess) {
					t.Fatalf("area list authority = %+v", access)
				}
				return wantAreas, nil
			})
			if err != nil {
				t.Fatalf("newAreaIndexHandler() returned error: %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("HX-Request", hxRequest)
			request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{Access: wantAccess}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusOK || calls != 1 ||
				!strings.Contains(body, "Announcements &amp; News") || !strings.Contains(body, "Durable &lt;updates&gt;") ||
				!strings.Contains(body, "Member discussion") || strings.Contains(body, "ready for its first discussion area") ||
				!strings.Contains(body, `href="/bb/areas/announcements"`) || !strings.Contains(body, `hx-get="/bb/areas/announcements"`) ||
				!strings.Contains(body, `href="/bb/areas/members"`) || !strings.Contains(body, `hx-target="#main-content"`) ||
				!strings.Contains(body, `hx-swap="outerHTML"`) || !strings.Contains(body, `hx-push-url="true"`) ||
				!strings.Contains(body, ">3<") || !strings.Contains(body, ">27<") ||
				!strings.Contains(body, "Release &lt;notes&gt;") || !strings.Contains(body, "Ada &amp; Co") ||
				!strings.Contains(body, `href="/bb/topics/91#post-117"`) ||
				!strings.Contains(body, "Sep 2, 2026 14:05 CDT") ||
				strings.Contains(body, "read_only") || strings.Contains(body, "groups") {
				t.Fatalf("area index response = (status %d, calls %d, body %q)", response.Code, calls, body)
			}
			if gotPage := strings.HasPrefix(body, "<!doctype html>"); gotPage != (hxRequest == "") {
				t.Fatalf("complete page = %t, want %t", gotPage, hxRequest == "")
			}
			for _, darkSurface := range []string{
				`class="h-full bg-slate-950"`,
				`bg-slate-900`,
				`text-slate-200`,
				`text-blue-300`,
			} {
				if hxRequest == "" && !strings.Contains(body, darkSurface) {
					t.Fatalf("complete page lacks dark-theme surface %q: %s", darkSurface, body)
				}
			}
			for _, lightSurface := range []string{"bg-white", "bg-slate-50", "bg-slate-100", "bg-blue-50"} {
				if strings.Contains(body, lightSurface) {
					t.Fatalf("area index contains obsolete light surface %q: %s", lightSurface, body)
				}
			}
		})
	}
}

func TestAreaIndexHandlerRendersEmptyAndRedactedUnavailableStates(t *testing.T) {
	t.Parallel()

	builder, view := areaIndexTestBuilderAndView(t)
	secret := "do-not-leak-area-store-failure"
	for _, test := range []struct {
		name       string
		list       AreaIndexLister
		wantStatus int
		wantText   string
	}{
		{name: "empty", list: func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return []store.VisibleAreaSummary{}, nil
		}, wantStatus: http.StatusOK, wantText: "ready for its first discussion area"},
		{name: "failure", list: func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return []store.VisibleAreaSummary{{Area: db.Area{Name: secret}}}, errors.New(secret)
		}, wantStatus: http.StatusServiceUnavailable, wantText: "Discussion areas are temporarily unavailable"},
		{name: "malformed row", list: func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return []store.VisibleAreaSummary{{Area: db.Area{ID: 1, Slug: "with/slash", Name: secret}}}, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "Discussion areas are temporarily unavailable"},
		{name: "missing name", list: func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return []store.VisibleAreaSummary{{Area: db.Area{ID: 1, Slug: "public"}}}, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "Discussion areas are temporarily unavailable"},
		{name: "malformed latest post", list: func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return []store.VisibleAreaSummary{{Area: db.Area{ID: 1, Slug: "public", Name: "Public"}, PostCount: 1, LatestPost: &store.VisibleAreaLatestPost{TopicID: 4, TopicTitle: "Topic", PostID: 0, PostNumber: 1, Author: secret, CreatedAt: time.Now()}}}, nil
		}, wantStatus: http.StatusServiceUnavailable, wantText: "Discussion areas are temporarily unavailable"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newAreaIndexHandler(builder, view, test.list)
			if err != nil {
				t.Fatalf("newAreaIndexHandler() returned error: %v", err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantText) || strings.Contains(response.Body.String(), secret) {
				t.Fatalf("area index response = (%d, %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestAreaIndexHandlerShowsLogoutVerificationFailureOnlyToAuthenticatedSession(t *testing.T) {
	t.Parallel()

	builder, view := areaIndexTestBuilderAndView(t)
	handler, err := newAreaIndexHandler(builder, view, func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
		return []store.VisibleAreaSummary{}, nil
	})
	if err != nil {
		t.Fatalf("newAreaIndexHandler() returned error: %v", err)
	}
	for _, test := range []struct {
		name          string
		target        string
		authenticated bool
		wantNotice    bool
	}{
		{name: "authenticated failure", target: "/?" + logoutVerificationFailureQuery, authenticated: true, wantNotice: true},
		{name: "anonymous marker", target: "/?" + logoutVerificationFailureQuery},
		{name: "unrelated query", target: "/?logout=other", authenticated: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			if test.authenticated {
				request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{
					Access: auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator},
				}))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			gotNotice := strings.Contains(response.Body.String(), "Logout failed because the page was stale. You are still logged in. Please try again.")
			if response.Code != http.StatusOK || gotNotice != test.wantNotice {
				t.Fatalf("response = (status %d, notice %t, body %q)", response.Code, gotNotice, response.Body.String())
			}
		})
	}
}

func TestNewAreaIndexHandlerRejectsMissingLister(t *testing.T) {
	t.Parallel()

	builder, view := areaIndexTestBuilderAndView(t)
	if got, err := newAreaIndexHandler(builder, view, nil); err == nil || got != nil {
		t.Fatalf("newAreaIndexHandler(nil) = (%v, %v), want nil/error", got, err)
	}
	if got, err := newAreaIndexHandler(URLBuilder{}, view, func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) { return nil, nil }); err == nil || got != nil {
		t.Fatalf("newAreaIndexHandler(zero builder) = (%v, %v), want nil/error", got, err)
	}
}

func TestAreaIndexHandlerPropagatesCommittedWriteFailure(t *testing.T) {
	t.Parallel()

	builder, view := areaIndexTestBuilderAndView(t)
	for _, list := range []AreaIndexLister{
		func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return []store.VisibleAreaSummary{}, nil
		},
		func(context.Context, auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return nil, errors.New("store failed")
		},
	} {
		handler, err := newAreaIndexHandler(builder, view, list)
		if err != nil {
			t.Fatalf("newAreaIndexHandler() returned error: %v", err)
		}
		writer := &failingRenderResponseWriter{header: make(http.Header), cause: errTestResponseWrite}
		recovered := captureHandlerPanic(func() {
			handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))
		})
		if !errors.Is(asError(recovered), errTestResponseWrite) {
			t.Fatalf("area index panic = %v, want write cause", recovered)
		}
	}
}

func areaIndexTestBuilderAndView(t *testing.T) (URLBuilder, pageView) {
	t.Helper()
	publicBase, err := url.Parse("https://forum.example.test/bb")
	if err != nil {
		t.Fatalf("url.Parse() returned error: %v", err)
	}
	builder, err := NewURLBuilder(*publicBase, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	view, err := newPageView(builder, "Discussion areas")
	if err != nil {
		t.Fatalf("newPageView() returned error: %v", err)
	}
	return builder, view
}
