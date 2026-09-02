package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

func TestNewAuthenticatedHandlerActivatesAuthenticationWithoutProtectingInfrastructure(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32))
	revalidatedToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, 32))
	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32))
	revalidationState := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x63}, 32))
	service := &authenticatedHandlerTestService{}
	service.begin = func(_ context.Context, returnPath string) (string, string, error) {
		service.beginCalls++
		if returnPath != "/bb/" {
			t.Fatalf("login return path = %q", returnPath)
		}
		return "https://auth.example/authorize?state=" + state, state, nil
	}
	service.complete = func(_ context.Context, gotState, code string) (string, string, time.Time, error) {
		service.completeCalls++
		if gotState != state || code != "code" {
			t.Fatalf("callback input = (%q, %q)", gotState, code)
		}
		return sessionToken, "/bb/", time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC), nil
	}
	service.beginRevalidation = func(_ context.Context, sessionID int64, returnPath string) (string, string, error) {
		service.beginRevalidationCalls++
		if sessionID != 11 || returnPath != "/bb/topics/7" {
			t.Fatalf("revalidation start = (%d, %q)", sessionID, returnPath)
		}
		return "https://auth.example/authorize?state=" + revalidationState, revalidationState, nil
	}
	service.completeRevalidation = func(_ context.Context, gotState, code, oldToken string) (string, string, time.Time, error) {
		service.completeRevalidationCalls++
		if gotState != revalidationState || code != "revalidation-code" || oldToken != sessionToken {
			t.Fatalf("revalidation callback input = (%q, %q, %q)", gotState, code, oldToken)
		}
		return revalidatedToken, "/bb/", time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC), nil
	}
	service.authenticate = func(_ context.Context, token string) (auth.SessionAuthentication, error) {
		service.authenticateCalls++
		if token != sessionToken {
			t.Fatalf("session token = %q", token)
		}
		return auth.SessionAuthentication{SessionID: 11, Access: auth.AccessContext{
			Authenticated: true,
			UserID:        17,
			Role:          auth.RoleMember,
			GroupIDs:      []int64{23, 29},
			ValidatedAt:   time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
		}}, nil
	}
	service.revoke = func(_ context.Context, token string) (bool, error) {
		service.revokeCalls++
		if token != sessionToken {
			t.Fatalf("revoked token = %q", token)
		}
		return true, nil
	}
	areaCalls := 0
	listAreas := func(_ context.Context, access auth.AccessContext) ([]db.Area, error) {
		areaCalls++
		if !access.Authenticated || access.UserID != 17 || access.Role != auth.RoleMember || !reflect.DeepEqual(access.GroupIDs, []int64{23, 29}) {
			t.Fatalf("area-list access = %+v", access)
		}
		return []db.Area{{ID: 9, Slug: "members", Name: "Member area", Visibility: "authenticated", PostingMode: "normal"}}, nil
	}
	topicCalls := 0
	loadAreaTopics := func(_ context.Context, access auth.AccessContext, slug string, page int32) (store.VisibleAreaTopicPage, error) {
		topicCalls++
		if !access.Authenticated || access.UserID != 17 || access.Role != auth.RoleMember ||
			!reflect.DeepEqual(access.GroupIDs, []int64{23, 29}) || slug != "members" || page != 1 {
			t.Fatalf("area-topic access = (%+v, %q, %d)", access, slug, page)
		}
		return store.VisibleAreaTopicPage{Area: db.Area{ID: 9, Slug: "members", Name: "Member area"}, Number: 1}, nil
	}
	postCalls := 0
	loadTopicPosts := func(_ context.Context, access auth.AccessContext, topicID int64, page int32) (store.VisibleTopicPostPage, error) {
		postCalls++
		if !access.Authenticated || access.UserID != 17 || access.Role != auth.RoleMember ||
			!reflect.DeepEqual(access.GroupIDs, []int64{23, 29}) || topicID != 42 || page != 1 {
			t.Fatalf("topic-post access = (%+v, %d, %d)", access, topicID, page)
		}
		return topicPostTestPage(1), nil
	}
	handler, err := NewAuthenticatedHandler(
		builder, service, listAreas, loadAreaTopics, store.MaximumTopicPage,
		loadTopicPosts, store.MaximumPostPage, "gotth_bb_session", true,
	)
	if err != nil {
		t.Fatalf("NewAuthenticatedHandler() returned error: %v", err)
	}

	for _, test := range []struct {
		target     string
		wantStatus int
	}{
		{target: "/health/live", wantStatus: http.StatusOK},
		{target: "/static/" + appStylesheetFilename, wantStatus: http.StatusOK},
		{target: "/static/app-1.0.0-alpha.1.css", wantStatus: http.StatusNotFound},
		{target: "/health/missing", wantStatus: http.StatusNotFound},
		{target: "/static/missing", wantStatus: http.StatusNotFound},
		{target: "/missing", wantStatus: http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodGet, test.target, nil)
		request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.wantStatus || service.authenticateCalls != 0 || areaCalls != 0 || topicCalls != 0 || postCalls != 0 {
			t.Fatalf("infrastructure request %q = (status %d, authentication/area/topic/post calls %d/%d/%d/%d)",
				test.target, response.Code, service.authenticateCalls, areaCalls, topicCalls, postCalls)
		}
	}
	for _, target := range []string{
		"/areas", "/areas/", "/areas/public/nested", "/areas/public%2Fnested", "/areas/%70ublic",
		"/topics", "/topics/", "/topics/01", "/topics/42/nested", "/topics/42%2Fnested", "/topics/%34%32",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || service.authenticateCalls != 0 || topicCalls != 0 {
			t.Fatalf("nonroute %q = (status %d, authentication/topic calls %d/%d)", target, response.Code, service.authenticateCalls, topicCalls)
		}
	}
	wrongMethodRequest := httptest.NewRequest(http.MethodPost, "/areas/members", nil)
	wrongMethodRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	wrongMethodResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethodResponse, wrongMethodRequest)
	if wrongMethodResponse.Code != http.StatusMethodNotAllowed || service.authenticateCalls != 0 || topicCalls != 0 {
		t.Fatalf("wrong area method = (status %d, authentication/topic calls %d/%d)", wrongMethodResponse.Code, service.authenticateCalls, topicCalls)
	}
	wrongTopicMethodRequest := httptest.NewRequest(http.MethodPost, "/topics/42", nil)
	wrongTopicMethodRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	wrongTopicMethodResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongTopicMethodResponse, wrongTopicMethodRequest)
	if wrongTopicMethodResponse.Code != http.StatusMethodNotAllowed || service.authenticateCalls != 0 || postCalls != 0 {
		t.Fatalf("wrong topic method = (status %d, authentication/post calls %d/%d)", wrongTopicMethodResponse.Code, service.authenticateCalls, postCalls)
	}

	loginRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusSeeOther || service.beginCalls != 1 || loginRequest.Pattern != "GET /login" {
		t.Fatalf("login response = (status %d, calls %d, pattern %q)", loginResponse.Code, service.beginCalls, loginRequest.Pattern)
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code&state="+state, nil)
	callbackRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session_oidc_state", Value: state})
	callbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(callbackResponse, callbackRequest)
	if callbackResponse.Code != http.StatusSeeOther || service.completeCalls != 1 || callbackRequest.Pattern != "GET /auth/callback" {
		t.Fatalf("callback response = (status %d, calls %d, pattern %q)", callbackResponse.Code, service.completeCalls, callbackRequest.Pattern)
	}

	revalidationRequest := httptest.NewRequest(http.MethodGet, "/auth/revalidate?return=%2Fbb%2Ftopics%2F7", nil)
	revalidationRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	revalidationResponse := httptest.NewRecorder()
	handler.ServeHTTP(revalidationResponse, revalidationRequest)
	revalidationCookies := revalidationResponse.Result().Cookies()
	if revalidationResponse.Code != http.StatusSeeOther || service.authenticateCalls != 1 ||
		service.beginRevalidationCalls != 1 || revalidationRequest.Pattern != "GET /auth/revalidate" ||
		len(revalidationCookies) != 1 || revalidationCookies[0].Name != "gotth_bb_session_oidc_revalidate_state" ||
		revalidationCookies[0].Value != revalidationState {
		t.Fatalf("revalidation response = (status %d, auth/begin calls %d/%d, pattern %q, cookies %+v)",
			revalidationResponse.Code, service.authenticateCalls, service.beginRevalidationCalls,
			revalidationRequest.Pattern, revalidationCookies)
	}
	revalidationCallbackRequest := httptest.NewRequest(
		http.MethodGet, "/auth/callback?code=revalidation-code&state="+revalidationState, nil,
	)
	revalidationCallbackRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session_oidc_revalidate_state", Value: revalidationState})
	revalidationCallbackRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	revalidationCallbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(revalidationCallbackResponse, revalidationCallbackRequest)
	revalidationCallbackCookies := revalidationCallbackResponse.Result().Cookies()
	if revalidationCallbackResponse.Code != http.StatusSeeOther || service.completeRevalidationCalls != 1 ||
		revalidationCallbackRequest.Pattern != "GET /auth/callback" || len(revalidationCallbackCookies) != 2 ||
		revalidationCallbackCookies[0].Name != "gotth_bb_session_oidc_revalidate_state" ||
		revalidationCallbackCookies[1].Name != "gotth_bb_session" || revalidationCallbackCookies[1].Value != revalidatedToken {
		t.Fatalf("revalidation callback = (status %d, calls %d, pattern %q, cookies %+v)",
			revalidationCallbackResponse.Code, service.completeRevalidationCalls,
			revalidationCallbackRequest.Pattern, revalidationCallbackCookies)
	}

	rootRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	rootResponse := httptest.NewRecorder()
	handler.ServeHTTP(rootResponse, rootRequest)
	if rootResponse.Code != http.StatusOK || service.authenticateCalls != 2 || areaCalls != 1 || rootRequest.Pattern != "GET /" ||
		!strings.Contains(rootResponse.Body.String(), "Member area") {
		t.Fatalf("root response = (status %d, auth/area calls %d/%d, pattern %q, body %q)",
			rootResponse.Code, service.authenticateCalls, areaCalls, rootRequest.Pattern, rootResponse.Body.String())
	}

	areaRequest := httptest.NewRequest(http.MethodGet, "/areas/members", nil)
	areaRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	areaResponse := httptest.NewRecorder()
	handler.ServeHTTP(areaResponse, areaRequest)
	if areaResponse.Code != http.StatusOK || service.authenticateCalls != 3 || topicCalls != 1 || areaRequest.Pattern != "GET /areas/{slug}" ||
		!strings.Contains(areaResponse.Body.String(), "Member area") {
		t.Fatalf("area response = (status %d, auth/topic calls %d/%d, pattern %q, body %q)",
			areaResponse.Code, service.authenticateCalls, topicCalls, areaRequest.Pattern, areaResponse.Body.String())
	}

	topicRequest := httptest.NewRequest(http.MethodGet, "/topics/42", nil)
	topicRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	topicResponse := httptest.NewRecorder()
	handler.ServeHTTP(topicResponse, topicRequest)
	if topicResponse.Code != http.StatusOK || service.authenticateCalls != 4 || postCalls != 1 || topicRequest.Pattern != "GET /topics/{topicID}" ||
		!strings.Contains(topicResponse.Body.String(), "Welcome") {
		t.Fatalf("topic response = (status %d, auth/post calls %d/%d, pattern %q, body %q)",
			topicResponse.Code, service.authenticateCalls, postCalls, topicRequest.Pattern, topicResponse.Body.String())
	}

	csrfToken, err := deriveCSRFToken(sessionToken)
	if err != nil {
		t.Fatalf("deriveCSRFToken() returned error: %v", err)
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	logoutRequest.Header.Set(csrfHeaderName, csrfToken)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusSeeOther || service.authenticateCalls != 5 || service.revokeCalls != 1 || logoutRequest.Pattern != "POST /logout" {
		t.Fatalf("logout response = (status %d, auth/revoke calls %d/%d, pattern %q)", logoutResponse.Code, service.authenticateCalls, service.revokeCalls, logoutRequest.Pattern)
	}
}

func TestNewAuthenticatedHandlerRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	service := &authenticatedHandlerTestService{
		begin: func(context.Context, string) (string, string, error) { return "", "", nil },
		beginRevalidation: func(context.Context, int64, string) (string, string, error) {
			return "", "", nil
		},
		complete: func(context.Context, string, string) (string, string, time.Time, error) {
			return "", "", time.Time{}, nil
		},
		completeRevalidation: func(context.Context, string, string, string) (string, string, time.Time, error) {
			return "", "", time.Time{}, nil
		},
		authenticate: func(context.Context, string) (auth.SessionAuthentication, error) {
			return auth.SessionAuthentication{}, nil
		},
		revoke: func(context.Context, string) (bool, error) { return false, nil },
	}
	for _, test := range []struct {
		name    string
		builder URLBuilder
		service AuthenticationService
		list    AreaIndexLister
		topics  AreaTopicPageLoader
		maximum int32
		cookie  string
	}{
		{name: "builder", service: service, list: emptyAreaIndexLister, topics: panicAreaTopicPageLoader, maximum: store.MaximumTopicPage, cookie: "session"},
		{name: "service", builder: builder, list: emptyAreaIndexLister, topics: panicAreaTopicPageLoader, maximum: store.MaximumTopicPage, cookie: "session"},
		{name: "area lister", builder: builder, service: service, topics: panicAreaTopicPageLoader, maximum: store.MaximumTopicPage, cookie: "session"},
		{name: "topic loader", builder: builder, service: service, list: emptyAreaIndexLister, maximum: store.MaximumTopicPage, cookie: "session"},
		{name: "topic maximum", builder: builder, service: service, list: emptyAreaIndexLister, topics: panicAreaTopicPageLoader, cookie: "session"},
		{name: "cookie", builder: builder, service: service, list: emptyAreaIndexLister, topics: panicAreaTopicPageLoader, maximum: store.MaximumTopicPage},
		{name: "invalid cookie", builder: builder, service: service, list: emptyAreaIndexLister, topics: panicAreaTopicPageLoader, maximum: store.MaximumTopicPage, cookie: "bad name"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := NewAuthenticatedHandler(
				test.builder, test.service, test.list, test.topics, test.maximum,
				panicTopicPostPageLoader, store.MaximumPostPage, test.cookie, true,
			); err == nil || got != nil {
				t.Fatalf("NewAuthenticatedHandler() = (%v, %v), want nil/error", got, err)
			}
		})
	}
}

func TestNewAuthenticatedHandlerFailsRevalidationWithoutServerSessionAuthority(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-revalidation-start-cause"
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, 32))
	for _, test := range []struct {
		name           string
		withCookie     bool
		authentication auth.SessionAuthentication
		beginFailure   bool
		wantStatus     int
		wantAuthCalls  int
		wantBeginCalls int
	}{
		{name: "anonymous", wantStatus: http.StatusUnauthorized, wantAuthCalls: 0},
		{name: "missing session ID", withCookie: true, authentication: auth.SessionAuthentication{Access: auth.AccessContext{Authenticated: true}}, wantStatus: http.StatusUnauthorized, wantAuthCalls: 1},
		{name: "begin failure", withCookie: true, authentication: auth.SessionAuthentication{SessionID: 9, Access: auth.AccessContext{Authenticated: true}}, beginFailure: true, wantStatus: http.StatusServiceUnavailable, wantAuthCalls: 1, wantBeginCalls: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := &authenticatedHandlerTestService{
				begin: func(context.Context, string) (string, string, error) { return "", "", nil },
				beginRevalidation: func(context.Context, int64, string) (string, string, error) {
					if !test.beginFailure {
						panic("revalidation begin must not run")
					}
					return "https://" + secret + ".example/", secret, errors.New(secret)
				},
				complete: func(context.Context, string, string) (string, string, time.Time, error) {
					return "", "", time.Time{}, nil
				},
				completeRevalidation: func(context.Context, string, string, string) (string, string, time.Time, error) {
					return "", "", time.Time{}, nil
				},
				authenticate: func(context.Context, string) (auth.SessionAuthentication, error) {
					return test.authentication, nil
				},
				revoke: func(context.Context, string) (bool, error) { return false, nil },
			}
			originalBegin := service.beginRevalidation
			service.beginRevalidation = func(ctx context.Context, sessionID int64, returnPath string) (string, string, error) {
				service.beginRevalidationCalls++
				return originalBegin(ctx, sessionID, returnPath)
			}
			originalAuthenticate := service.authenticate
			service.authenticate = func(ctx context.Context, token string) (auth.SessionAuthentication, error) {
				service.authenticateCalls++
				return originalAuthenticate(ctx, token)
			}
			handler, err := NewAuthenticatedHandler(
				callbackTestURLBuilder(t),
				service,
				emptyAreaIndexLister,
				panicAreaTopicPageLoader,
				store.MaximumTopicPage,
				panicTopicPostPageLoader,
				store.MaximumPostPage,
				"gotth_bb_session",
				true,
			)
			if err != nil {
				t.Fatalf("NewAuthenticatedHandler() returned error: %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, "/auth/revalidate", nil)
			if test.withCookie {
				request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || service.authenticateCalls != test.wantAuthCalls ||
				service.beginRevalidationCalls != test.wantBeginCalls || response.Header().Get("Location") != "" ||
				strings.Contains(response.Body.String(), secret) {
				t.Fatalf("response = (status %d, auth/begin calls %d/%d, headers %v, body %q)",
					response.Code, service.authenticateCalls, service.beginRevalidationCalls,
					response.Header(), response.Body.String())
			}
		})
	}
}

type authenticatedHandlerTestService struct {
	begin                     func(context.Context, string) (string, string, error)
	beginRevalidation         func(context.Context, int64, string) (string, string, error)
	complete                  func(context.Context, string, string) (string, string, time.Time, error)
	completeRevalidation      func(context.Context, string, string, string) (string, string, time.Time, error)
	authenticate              func(context.Context, string) (auth.SessionAuthentication, error)
	revoke                    func(context.Context, string) (bool, error)
	beginCalls                int
	beginRevalidationCalls    int
	completeCalls             int
	completeRevalidationCalls int
	authenticateCalls         int
	revokeCalls               int
}

func (service *authenticatedHandlerTestService) BeginInitialLogin(ctx context.Context, returnPath string) (string, string, error) {
	return service.begin(ctx, returnPath)
}

func (service *authenticatedHandlerTestService) CompleteInitialLogin(ctx context.Context, state, code string) (string, string, time.Time, error) {
	return service.complete(ctx, state, code)
}

func (service *authenticatedHandlerTestService) BeginRevalidation(ctx context.Context, sessionID int64, returnPath string) (string, string, error) {
	return service.beginRevalidation(ctx, sessionID, returnPath)
}

func (service *authenticatedHandlerTestService) CompleteRevalidation(ctx context.Context, state, code, oldToken string) (string, string, time.Time, error) {
	return service.completeRevalidation(ctx, state, code, oldToken)
}

func (service *authenticatedHandlerTestService) AuthenticateSession(ctx context.Context, token string) (auth.SessionAuthentication, error) {
	return service.authenticate(ctx, token)
}

func (service *authenticatedHandlerTestService) RevokeSession(ctx context.Context, token string) (bool, error) {
	return service.revoke(ctx, token)
}

func emptyAreaIndexLister(context.Context, auth.AccessContext) ([]db.Area, error) {
	return []db.Area{}, nil
}
