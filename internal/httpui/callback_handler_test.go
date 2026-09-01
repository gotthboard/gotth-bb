package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

func TestNewInitialLoginCallbackHandlerSetsCookieAndRedirects(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x3c}, 32))
	expiresAt := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	calls := 0
	handler, err := newInitialLoginCallbackHandler(
		func(_ context.Context, state, code string) (completedBrowserLogin, error) {
			calls++
			if state != "browser-state" || code != "authorization-code" {
				t.Fatalf("completion input = (%q, %q)", state, code)
			}
			return completedBrowserLogin{token: token, returnPath: "/bb/topics/7?view=new", expiresAt: expiresAt}, nil
		},
		"gotth_bb_session", builder, true,
	)
	if err != nil {
		t.Fatalf("newInitialLoginCallbackHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?code=authorization-code&state=browser-state", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || calls != 1 || response.Header().Get("Location") != "/bb/topics/7?view=new" || response.Body.Len() != 0 {
		t.Fatalf("response = (status %d, calls %d, location %q, body %q)", response.Code, calls, response.Header().Get("Location"), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
	setCookies := response.Result().Cookies()
	if len(setCookies) != 1 || setCookies[0].Name != "gotth_bb_session" || setCookies[0].Value != token ||
		setCookies[0].Path != "/bb/" || !setCookies[0].HttpOnly || !setCookies[0].Secure ||
		setCookies[0].SameSite != http.SameSiteLaxMode || !setCookies[0].Expires.Equal(expiresAt) {
		t.Fatalf("Set-Cookie = %+v", setCookies)
	}
}

func TestNewInitialLoginCallbackHandlerRejectsMalformedRequestsBeforeCompletion(t *testing.T) {
	t.Parallel()

	handler, err := newInitialLoginCallbackHandler(
		func(context.Context, string, string) (completedBrowserLogin, error) {
			panic("completion must not run")
		},
		"gotth_bb_session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newInitialLoginCallbackHandler() returned error: %v", err)
	}
	for _, test := range []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantAllow  string
	}{
		{name: "wrong method", method: http.MethodPost, target: "/auth/callback?code=code&state=state", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "empty query", method: http.MethodGet, target: "/auth/callback", wantStatus: http.StatusBadRequest},
		{name: "missing state", method: http.MethodGet, target: "/auth/callback?code=code", wantStatus: http.StatusBadRequest},
		{name: "missing code", method: http.MethodGet, target: "/auth/callback?state=state", wantStatus: http.StatusBadRequest},
		{name: "duplicate state", method: http.MethodGet, target: "/auth/callback?code=code&state=one&state=two", wantStatus: http.StatusBadRequest},
		{name: "duplicate code", method: http.MethodGet, target: "/auth/callback?code=one&code=two&state=state", wantStatus: http.StatusBadRequest},
		{name: "extra key", method: http.MethodGet, target: "/auth/callback?code=code&state=state&iss=https%3A%2F%2Fauth.example", wantStatus: http.StatusBadRequest},
		{name: "malformed encoding", method: http.MethodGet, target: "/auth/callback?code=%zz&state=state", wantStatus: http.StatusBadRequest},
		{name: "oversized query", method: http.MethodGet, target: "/auth/callback?code=" + strings.Repeat("a", maxOIDCCallbackQueryBytes) + "&state=state", wantStatus: http.StatusRequestURITooLong},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, test.target, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Allow") != test.wantAllow ||
				response.Header().Get("Set-Cookie") != "" || response.Header().Get("Location") != "" {
				t.Fatalf("response = (status %d, allow %q, headers %v)", response.Code, response.Header().Get("Allow"), response.Header())
			}
		})
	}
}

func TestNewInitialLoginCallbackHandlerCollapsesCompletionFailure(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-callback-failure"
	handler, err := newInitialLoginCallbackHandler(
		func(context.Context, string, string) (completedBrowserLogin, error) {
			return completedBrowserLogin{}, errors.New(secret)
		},
		"gotth_bb_session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newInitialLoginCallbackHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/auth/callback?code=secret-code&state=secret-state", nil))
	serialized := response.Header().Values("Set-Cookie")
	if response.Code != http.StatusBadRequest || len(serialized) != 0 || response.Header().Get("Location") != "" ||
		strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "secret-code") || strings.Contains(response.Body.String(), "secret-state") {
		t.Fatalf("failed response = (status %d, headers %v, body %q)", response.Code, response.Header(), response.Body.String())
	}
}

func TestNewInitialLoginCallbackHandlerEnforcesRawQueryBoundary(t *testing.T) {
	t.Parallel()

	for _, size := range []int{
		maxOIDCCallbackQueryBytes - 1,
		maxOIDCCallbackQueryBytes,
		maxOIDCCallbackQueryBytes + 1,
		maxOIDCCallbackQueryBytes * 2,
	} {
		size := size
		t.Run(time.Duration(size).String(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler, err := newInitialLoginCallbackHandler(
				func(context.Context, string, string) (completedBrowserLogin, error) {
					calls++
					return completedBrowserLogin{}, errors.New("expected completion rejection")
				},
				"gotth_bb_session", callbackTestURLBuilder(t), true,
			)
			if err != nil {
				t.Fatalf("newInitialLoginCallbackHandler() returned error: %v", err)
			}
			const fixedQueryBytes = len("code=") + len("&state=state")
			target := "/auth/callback?code=" + strings.Repeat("a", size-fixedQueryBytes) + "&state=state"
			request := httptest.NewRequest(http.MethodGet, target, nil)
			if len(request.URL.RawQuery) != size {
				t.Fatalf("raw query length = %d, want %d", len(request.URL.RawQuery), size)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			wantStatus, wantCalls := http.StatusBadRequest, 1
			if size > maxOIDCCallbackQueryBytes {
				wantStatus, wantCalls = http.StatusRequestURITooLong, 0
			}
			if response.Code != wantStatus || calls != wantCalls {
				t.Fatalf("boundary response = (status %d, calls %d), want (%d, %d)", response.Code, calls, wantStatus, wantCalls)
			}
		})
	}
}

func TestNewInitialLoginCallbackHandlerRejectsUnsafeSuccessfulResult(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	validToken := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	validExpiry := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	for _, result := range []completedBrowserLogin{
		{token: validToken, returnPath: "https://evil.example/", expiresAt: validExpiry},
		{token: "invalid", returnPath: "/bb/", expiresAt: validExpiry},
		{token: validToken, returnPath: "/bb/"},
	} {
		result := result
		t.Run(url.QueryEscape(result.returnPath+result.token), func(t *testing.T) {
			t.Parallel()
			handler, err := newInitialLoginCallbackHandler(
				func(context.Context, string, string) (completedBrowserLogin, error) { return result, nil },
				"gotth_bb_session", builder, true,
			)
			if err != nil {
				t.Fatalf("newInitialLoginCallbackHandler() returned error: %v", err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/auth/callback?code=code&state=state", nil))
			if response.Code != http.StatusInternalServerError || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Location") != "" {
				t.Fatalf("response = (status %d, headers %v)", response.Code, response.Header())
			}
		})
	}
}

func TestNewInitialLoginCallbackHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	complete := func(context.Context, string, string) (completedBrowserLogin, error) {
		return completedBrowserLogin{}, nil
	}
	for _, test := range []struct {
		name     string
		complete func(context.Context, string, string) (completedBrowserLogin, error)
		cookie   string
		builder  URLBuilder
	}{
		{name: "completion", cookie: "gotth_bb_session", builder: builder},
		{name: "cookie", complete: complete, builder: builder},
		{name: "builder", complete: complete, cookie: "gotth_bb_session"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := newInitialLoginCallbackHandler(test.complete, test.cookie, test.builder, true); err == nil || got != nil {
				t.Fatalf("newInitialLoginCallbackHandler() = (%v, %v), want nil/error", got, err)
			}
		})
	}
}

func callbackTestURLBuilder(t *testing.T) URLBuilder {
	t.Helper()
	publicURL, err := config.ParsePublicBaseURL("https://forum.example/bb", "/bb", true)
	if err != nil {
		t.Fatalf("ParsePublicBaseURL() returned error: %v", err)
	}
	builder, err := NewURLBuilder(publicURL, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	return builder
}
