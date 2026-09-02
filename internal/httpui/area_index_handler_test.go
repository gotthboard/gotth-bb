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
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

func TestAreaIndexHandlerListsOnlyStoreReturnedAreasForPageAndFragment(t *testing.T) {
	t.Parallel()

	view := areaIndexTestView(t)
	wantAccess := auth.AccessContext{
		Authenticated: true, UserID: 42, Role: auth.RoleMember, GroupIDs: []int64{3, 11},
		ValidatedAt: time.Date(2026, time.September, 2, 1, 0, 0, 0, time.UTC),
	}
	wantAreas := []db.Area{
		{ID: 5, Slug: "announcements", Name: "Announcements & News", Description: "Durable <updates>", DisplayOrder: 1, Visibility: "public", PostingMode: "read_only"},
		{ID: 8, Slug: "members", Name: "Member discussion", Description: "Private conversation", DisplayOrder: 2, Visibility: "groups", PostingMode: "normal"},
	}
	for _, hxRequest := range []string{"", "true"} {
		hxRequest := hxRequest
		t.Run("hx="+hxRequest, func(t *testing.T) {
			t.Parallel()

			calls := 0
			handler, err := newAreaIndexHandler(view, func(ctx context.Context, access auth.AccessContext) ([]db.Area, error) {
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
				strings.Contains(body, "announcements") || strings.Contains(body, "read_only") || strings.Contains(body, "groups") {
				t.Fatalf("area index response = (status %d, calls %d, body %q)", response.Code, calls, body)
			}
			if gotPage := strings.HasPrefix(body, "<!doctype html>"); gotPage != (hxRequest == "") {
				t.Fatalf("complete page = %t, want %t", gotPage, hxRequest == "")
			}
		})
	}
}

func TestAreaIndexHandlerRendersEmptyAndRedactedUnavailableStates(t *testing.T) {
	t.Parallel()

	view := areaIndexTestView(t)
	secret := "do-not-leak-area-store-failure"
	for _, test := range []struct {
		name       string
		list       AreaIndexLister
		wantStatus int
		wantText   string
	}{
		{name: "empty", list: func(context.Context, auth.AccessContext) ([]db.Area, error) { return []db.Area{}, nil }, wantStatus: http.StatusOK, wantText: "ready for its first discussion area"},
		{name: "failure", list: func(context.Context, auth.AccessContext) ([]db.Area, error) {
			return []db.Area{{Name: secret}}, errors.New(secret)
		}, wantStatus: http.StatusServiceUnavailable, wantText: "Discussion areas are temporarily unavailable"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newAreaIndexHandler(view, test.list)
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

func TestNewAreaIndexHandlerRejectsMissingLister(t *testing.T) {
	t.Parallel()

	if got, err := newAreaIndexHandler(areaIndexTestView(t), nil); err == nil || got != nil {
		t.Fatalf("newAreaIndexHandler(nil) = (%v, %v), want nil/error", got, err)
	}
}

func TestAreaIndexHandlerPropagatesCommittedWriteFailure(t *testing.T) {
	t.Parallel()

	for _, list := range []AreaIndexLister{
		func(context.Context, auth.AccessContext) ([]db.Area, error) { return []db.Area{}, nil },
		func(context.Context, auth.AccessContext) ([]db.Area, error) { return nil, errors.New("store failed") },
	} {
		handler, err := newAreaIndexHandler(areaIndexTestView(t), list)
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

func areaIndexTestView(t *testing.T) pageView {
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
	return view
}
