package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/discovery"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestAuthenticatedDiscoveryPreflightRunsBeforeSessionLookup(t *testing.T) {
	t.Parallel()

	service := &authenticatedHandlerTestService{}
	service.authenticate = func(context.Context, string) (auth.SessionAuthentication, error) {
		service.authenticateCalls++
		return auth.SessionAuthentication{SessionID: 7, Access: auth.AccessContext{Authenticated: true, UserID: 2, Role: auth.RoleMember}}, nil
	}
	var searches, directPosts, verifications atomic.Int32
	discoveryServices := DiscoveryHTTPServices{
		Search: func(context.Context, discovery.SearchRequest, auth.AccessContext) (discovery.SearchPage, error) {
			searches.Add(1)
			return discovery.SearchPage{}, nil
		},
		Activity: unavailableActivityService,
		DirectPost: func(context.Context, int64, auth.AccessContext) (discovery.DirectPost, error) {
			directPosts.Add(1)
			return discovery.DirectPost{}, pgx.ErrNoRows
		},
	}
	verifyActivityCursor := func(string) (discovery.AuthenticatedCursor, error) {
		verifications.Add(1)
		return discovery.AuthenticatedCursor{}, discovery.ErrInvalidActivityCursor
	}
	handler, err := newAuthenticatedHandler(
		callbackTestURLBuilder(t), service, emptyAreaIndexLister, panicAreaTopicPageLoader, store.MaximumTopicPage,
		panicTopicPostPageLoader, store.MaximumPostPage, abuse.DestinationPolicy{}, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &discoveryServices, verifyActivityCursor, nil, nil,
		url.URL{}, false, nil, nil, "gotth_bb_session", true, unavailableReadiness,
	)
	if err != nil {
		t.Fatalf("newAuthenticatedHandler() returned error: %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))

	for _, target := range []string{
		"/search", "/search?area=private", "/activity?cursor=short", "/posts/7?unexpected=1", "/posts/07",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, body %q", target, response.Code, response.Body.String())
		}
	}
	if service.authenticateCalls != 0 || searches.Load() != 0 || directPosts.Load() != 0 || verifications.Load() != 0 {
		t.Fatalf("rejected input crossed boundary: auth=%d search=%d direct=%d verify=%d", service.authenticateCalls, searches.Load(), directPosts.Load(), verifications.Load())
	}

	validSearch := httptest.NewRequest(http.MethodGet, "/search?author=2", nil)
	validSearch.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: token})
	searchResponse := httptest.NewRecorder()
	handler.ServeHTTP(searchResponse, validSearch)
	if searchResponse.Code != http.StatusOK || service.authenticateCalls != 1 || searches.Load() != 1 {
		t.Fatalf("valid search = (status %d, auth %d, search %d)", searchResponse.Code, service.authenticateCalls, searches.Load())
	}

	validPost := httptest.NewRequest(http.MethodGet, "/posts/7", nil)
	validPost.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: token})
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, validPost)
	if postResponse.Code != http.StatusNotFound || service.authenticateCalls != 2 || directPosts.Load() != 1 {
		t.Fatalf("valid direct post = (status %d, auth %d, direct %d)", postResponse.Code, service.authenticateCalls, directPosts.Load())
	}
}
