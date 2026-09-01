package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
)

func TestNewAuthenticatedHandlerActivatesAuthenticationWithoutProtectingInfrastructure(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32))
	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32))
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
	service.authenticate = func(_ context.Context, token string) (auth.SessionAuthentication, error) {
		service.authenticateCalls++
		if token != sessionToken {
			t.Fatalf("session token = %q", token)
		}
		return auth.SessionAuthentication{Access: auth.AccessContext{
			Authenticated: true,
			UserID:        17,
			Role:          auth.RoleMember,
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
	handler, err := NewAuthenticatedHandler(builder, service, "gotth_bb_session", true)
	if err != nil {
		t.Fatalf("NewAuthenticatedHandler() returned error: %v", err)
	}

	for _, test := range []struct {
		target     string
		wantStatus int
	}{
		{target: "/health/live", wantStatus: http.StatusOK},
		{target: "/static/app-1.0.0-alpha.1.css", wantStatus: http.StatusOK},
		{target: "/health/missing", wantStatus: http.StatusNotFound},
		{target: "/static/missing", wantStatus: http.StatusNotFound},
		{target: "/missing", wantStatus: http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodGet, test.target, nil)
		request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.wantStatus || service.authenticateCalls != 0 {
			t.Fatalf("infrastructure request %q = (status %d, authentication calls %d)", test.target, response.Code, service.authenticateCalls)
		}
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

	rootRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRequest.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: sessionToken})
	rootResponse := httptest.NewRecorder()
	handler.ServeHTTP(rootResponse, rootRequest)
	if rootResponse.Code != http.StatusOK || service.authenticateCalls != 1 || rootRequest.Pattern != "GET /" {
		t.Fatalf("root response = (status %d, auth calls %d, pattern %q)", rootResponse.Code, service.authenticateCalls, rootRequest.Pattern)
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
	if logoutResponse.Code != http.StatusSeeOther || service.authenticateCalls != 2 || service.revokeCalls != 1 || logoutRequest.Pattern != "POST /logout" {
		t.Fatalf("logout response = (status %d, auth/revoke calls %d/%d, pattern %q)", logoutResponse.Code, service.authenticateCalls, service.revokeCalls, logoutRequest.Pattern)
	}
}

func TestNewAuthenticatedHandlerRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	service := &authenticatedHandlerTestService{
		begin: func(context.Context, string) (string, string, error) { return "", "", nil },
		complete: func(context.Context, string, string) (string, string, time.Time, error) {
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
		cookie  string
	}{
		{name: "builder", service: service, cookie: "session"},
		{name: "service", builder: builder, cookie: "session"},
		{name: "cookie", builder: builder, service: service},
		{name: "invalid cookie", builder: builder, service: service, cookie: "bad name"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := NewAuthenticatedHandler(test.builder, test.service, test.cookie, true); err == nil || got != nil {
				t.Fatalf("NewAuthenticatedHandler() = (%v, %v), want nil/error", got, err)
			}
		})
	}
}

type authenticatedHandlerTestService struct {
	begin             func(context.Context, string) (string, string, error)
	complete          func(context.Context, string, string) (string, string, time.Time, error)
	authenticate      func(context.Context, string) (auth.SessionAuthentication, error)
	revoke            func(context.Context, string) (bool, error)
	beginCalls        int
	completeCalls     int
	authenticateCalls int
	revokeCalls       int
}

func (service *authenticatedHandlerTestService) BeginInitialLogin(ctx context.Context, returnPath string) (string, string, error) {
	return service.begin(ctx, returnPath)
}

func (service *authenticatedHandlerTestService) CompleteInitialLogin(ctx context.Context, state, code string) (string, string, time.Time, error) {
	return service.complete(ctx, state, code)
}

func (service *authenticatedHandlerTestService) AuthenticateSession(ctx context.Context, token string) (auth.SessionAuthentication, error) {
	return service.authenticate(ctx, token)
}

func (service *authenticatedHandlerTestService) RevokeSession(ctx context.Context, token string) (bool, error) {
	return service.revoke(ctx, token)
}
