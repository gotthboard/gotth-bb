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
)

func TestLogoutHandlerRevokesBeforeExpiringAndRedirecting(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		revoked bool
		secure  bool
	}{
		{name: "no row development", secure: false},
		{name: "one row production", revoked: true, secure: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			csrfCalls, revokeCalls := 0, 0
			handler, err := newLogoutHandler(
				func(ctx context.Context, token string) (bool, error) {
					revokeCalls++
					if ctx == nil || token != "opaque-token" || csrfCalls != 1 {
						t.Fatalf("revocation input/order = (%v, %q, CSRF calls %d)", ctx, token, csrfCalls)
					}
					return test.revoked, nil
				},
				func(request *http.Request) error {
					csrfCalls++
					if request.Method != http.MethodPost {
						t.Fatalf("CSRF request method = %q", request.Method)
					}
					return nil
				},
				"gotth_bb_session", callbackTestURLBuilder(t), test.secure,
			)
			if err != nil {
				t.Fatalf("newLogoutHandler() returned error: %v", err)
			}
			request := authenticatedLogoutRequest(t, "gotth_bb_session=opaque-token", true)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			cookies := response.Result().Cookies()
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/" || response.Body.Len() != 0 ||
				response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" ||
				csrfCalls != 1 || revokeCalls != 1 || len(cookies) != 1 || cookies[0].Name != "gotth_bb_session" ||
				cookies[0].Value != "" || cookies[0].Path != "/bb/" || cookies[0].MaxAge != -1 ||
				!cookies[0].HttpOnly || cookies[0].Secure != test.secure || cookies[0].SameSite != http.SameSiteLaxMode ||
				!cookies[0].Expires.Equal(time.Unix(1, 0).UTC()) {
				t.Fatalf("response = (status %d, headers %v, cookies %+v, calls %d/%d)", response.Code, response.Header(), cookies, csrfCalls, revokeCalls)
			}
		})
	}
}

func TestLogoutHandlerRejectsBeforeRevocation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		method       string
		auth         bool
		csrfError    error
		cookie       string
		wantStatus   int
		wantAllow    string
		wantLocation string
		wantCSRF     int
	}{
		{name: "method", method: http.MethodGet, auth: true, cookie: "session=token", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodPost},
		{name: "anonymous", method: http.MethodPost, cookie: "session=token", wantStatus: http.StatusUnauthorized},
		{name: "CSRF", method: http.MethodPost, auth: true, csrfError: errors.New("bad CSRF"), cookie: "session=token", wantStatus: http.StatusSeeOther, wantLocation: "/bb/?logout=verification-failed", wantCSRF: 1},
		{name: "missing cookie", method: http.MethodPost, auth: true, wantStatus: http.StatusBadRequest, wantCSRF: 1},
		{name: "duplicate cookie", method: http.MethodPost, auth: true, cookie: "session=one; session=two", wantStatus: http.StatusBadRequest, wantCSRF: 1},
		{name: "quoted cookie", method: http.MethodPost, auth: true, cookie: `session="token"`, wantStatus: http.StatusBadRequest, wantCSRF: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			csrfCalls := 0
			handler, err := newLogoutHandler(
				func(context.Context, string) (bool, error) { panic("revocation must not run") },
				func(*http.Request) error { csrfCalls++; return test.csrfError },
				"session", callbackTestURLBuilder(t), true,
			)
			if err != nil {
				t.Fatalf("newLogoutHandler() returned error: %v", err)
			}
			request := authenticatedLogoutRequest(t, test.cookie, test.auth)
			request.Method = test.method
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Allow") != test.wantAllow || csrfCalls != test.wantCSRF ||
				response.Header().Get("Set-Cookie") != "" || response.Header().Get("Location") != test.wantLocation ||
				(test.csrfError != nil && response.Body.Len() != 0) {
				t.Fatalf("response = (status %d, allow %q, headers %v, CSRF calls %d)", response.Code, response.Header().Get("Allow"), response.Header(), csrfCalls)
			}
		})
	}
}

func TestLogoutHandlerKeepsCookieOnRedactedRevocationFailure(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-logout-revocation-cause"
	handler, err := newLogoutHandler(
		func(context.Context, string) (bool, error) { return false, errors.New(secret) },
		func(*http.Request) error { return nil },
		"session", callbackTestURLBuilder(t), false,
	)
	if err != nil {
		t.Fatalf("newLogoutHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedLogoutRequest(t, "session=opaque-token", true))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Set-Cookie") != "" ||
		response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("failed response = (status %d, headers %v, body %q)", response.Code, response.Header(), response.Body.String())
	}
}

func TestLogoutHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	revoke := sessionRevoker(func(context.Context, string) (bool, error) { return false, nil })
	validate := csrfValidator(func(*http.Request) error { return nil })
	for _, test := range []struct {
		name     string
		revoke   sessionRevoker
		validate csrfValidator
		cookie   string
		builder  URLBuilder
	}{
		{name: "revoker", validate: validate, cookie: "session", builder: builder},
		{name: "CSRF validator", revoke: revoke, cookie: "session", builder: builder},
		{name: "cookie", revoke: revoke, validate: validate, builder: builder},
		{name: "invalid cookie", revoke: revoke, validate: validate, cookie: "bad name", builder: builder},
		{name: "builder", revoke: revoke, validate: validate, cookie: "session"},
		{name: "invalid path", revoke: revoke, validate: validate, cookie: "session", builder: URLBuilder{basePath: "/bad;path", initialized: true}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := newLogoutHandler(test.revoke, test.validate, test.cookie, test.builder, true); err == nil || got != nil {
				t.Fatalf("newLogoutHandler() = (%v, %v), want nil/error", got, err)
			}
		})
	}
}

func authenticatedLogoutRequest(t *testing.T, cookieHeader string, authenticated bool) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	if cookieHeader != "" {
		request.Header.Set("Cookie", cookieHeader)
	}
	if authenticated {
		authentication := auth.SessionAuthentication{
			Access:               auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember, ValidatedAt: time.Now()},
			RequiresRevalidation: true,
		}
		request = request.WithContext(context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication))
	}
	return request
}
