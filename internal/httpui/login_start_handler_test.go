package httpui

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestNewInitialLoginStartHandlerSetsStateCookieAndRedirects(t *testing.T) {
	t.Parallel()

	state := base64.RawURLEncoding.EncodeToString(bytesOf(0x73, 32))
	calls := 0
	handler, err := newLoginStartHandler(
		func(_ context.Context, returnPath string) (string, string, error) {
			calls++
			if returnPath != "/bb/topics/7?view=new" {
				t.Fatalf("return path = %q", returnPath)
			}
			return "https://auth.example/application/o/authorize/?client_id=gotth-bb&state=" + state, state, nil
		},
		initialLoginStateCookieSuffix, "gotth_bb_session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newLoginStartHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/login?return="+url.QueryEscape("/bb/topics/7?view=new"), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || calls != 1 ||
		response.Header().Get("Location") != "https://auth.example/application/o/authorize/?client_id=gotth-bb&state="+state ||
		response.Body.Len() != 0 {
		t.Fatalf("response = (status %d, calls %d, location %q, body %q)",
			response.Code, calls, response.Header().Get("Location"), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "gotth_bb_session_oidc_state" || cookies[0].Value != state ||
		cookies[0].Path != "/bb/" || cookies[0].MaxAge != 300 || !cookies[0].HttpOnly || !cookies[0].Secure ||
		cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("Set-Cookie = %+v", cookies)
	}
}

func TestNewInitialLoginStartHandlerUsesApplicationRootByDefault(t *testing.T) {
	t.Parallel()

	state := base64.RawURLEncoding.EncodeToString(bytesOf(0x31, 32))
	handler, err := newLoginStartHandler(
		func(_ context.Context, returnPath string) (string, string, error) {
			if returnPath != "/bb/" {
				t.Fatalf("default return path = %q", returnPath)
			}
			return "http://auth.example/authorize?state=" + state, state, nil
		},
		initialLoginStateCookieSuffix, "session", callbackTestURLBuilder(t), false,
	)
	if err != nil {
		t.Fatalf("newLoginStartHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") == "" || response.Result().Cookies()[0].Secure {
		t.Fatalf("default response = (status %d, headers %v)", response.Code, response.Header())
	}
}

func TestNewInitialLoginStartHandlerCanIssueRevalidationStateCookie(t *testing.T) {
	t.Parallel()

	state := base64.RawURLEncoding.EncodeToString(bytesOf(0x32, 32))
	handler, err := newLoginStartHandler(
		func(context.Context, string) (string, string, error) {
			return "https://auth.example/authorize?state=" + state, state, nil
		},
		revalidationStateCookieSuffix, "gotth_bb_session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newLoginStartHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/auth/revalidate", nil))
	cookies := response.Result().Cookies()
	if response.Code != http.StatusSeeOther || len(cookies) != 1 ||
		cookies[0].Name != "gotth_bb_session_oidc_revalidate_state" || cookies[0].Value != state {
		t.Fatalf("revalidation response = (status %d, cookies %+v)", response.Code, cookies)
	}
}

func TestNewInitialLoginStartHandlerRejectsMalformedRequestsBeforeBegin(t *testing.T) {
	t.Parallel()

	handler, err := newLoginStartHandler(
		func(context.Context, string) (string, string, error) { panic("begin must not run") },
		initialLoginStateCookieSuffix, "session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newLoginStartHandler() returned error: %v", err)
	}
	for _, test := range []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantAllow  string
	}{
		{name: "wrong method", method: http.MethodPost, target: "/login", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "dangling query", method: http.MethodGet, target: "/login?", wantStatus: http.StatusBadRequest},
		{name: "empty return", method: http.MethodGet, target: "/login?return=", wantStatus: http.StatusBadRequest},
		{name: "duplicate return", method: http.MethodGet, target: "/login?return=%2Fbb%2F&return=%2Fbb%2Ftopics", wantStatus: http.StatusBadRequest},
		{name: "extra query", method: http.MethodGet, target: "/login?return=%2Fbb%2F&next=%2Fbb%2F", wantStatus: http.StatusBadRequest},
		{name: "malformed encoding", method: http.MethodGet, target: "/login?return=%zz", wantStatus: http.StatusBadRequest},
		{name: "external return", method: http.MethodGet, target: "/login?return=https%3A%2F%2Fevil.example%2F", wantStatus: http.StatusBadRequest},
		{name: "noncanonical return", method: http.MethodGet, target: "/login?return=%2Fbb%2Fsearch%3Fq%3Dhello%2520world", wantStatus: http.StatusBadRequest},
		{name: "oversized query", method: http.MethodGet, target: "/login?return=" + strings.Repeat("a", maxInitialLoginQueryBytes), wantStatus: http.StatusRequestURITooLong},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
			if response.Code != test.wantStatus || response.Header().Get("Allow") != test.wantAllow ||
				response.Header().Get("Set-Cookie") != "" || response.Header().Get("Location") != "" {
				t.Fatalf("response = (status %d, allow %q, headers %v)", response.Code, response.Header().Get("Allow"), response.Header())
			}
		})
	}
}

func TestNewInitialLoginStartHandlerEnforcesRawQueryBoundary(t *testing.T) {
	t.Parallel()

	handler, err := newLoginStartHandler(
		func(context.Context, string) (string, string, error) {
			panic("invalid return path must not reach begin")
		},
		initialLoginStateCookieSuffix, "session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newLoginStartHandler() returned error: %v", err)
	}
	for _, size := range []int{
		maxInitialLoginQueryBytes - 1,
		maxInitialLoginQueryBytes,
		maxInitialLoginQueryBytes + 1,
		maxInitialLoginQueryBytes * 2,
	} {
		size := size
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()
			const prefix = "return="
			request := httptest.NewRequest(http.MethodGet, "/login?"+prefix+strings.Repeat("a", size-len(prefix)), nil)
			if len(request.URL.RawQuery) != size {
				t.Fatalf("raw query length = %d, want %d", len(request.URL.RawQuery), size)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			wantStatus := http.StatusBadRequest
			if size > maxInitialLoginQueryBytes {
				wantStatus = http.StatusRequestURITooLong
			}
			if response.Code != wantStatus {
				t.Fatalf("boundary response status = %d, want %d", response.Code, wantStatus)
			}
		})
	}
}

func TestNewInitialLoginStartHandlerCollapsesBeginFailure(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak-login-start-failure"
	handler, err := newLoginStartHandler(
		func(context.Context, string) (string, string, error) {
			return "https://" + secret + ".example/", secret, errors.New(secret)
		},
		initialLoginStateCookieSuffix, "session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newLoginStartHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login", nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Set-Cookie") != "" ||
		response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("failed response = (status %d, headers %v, body %q)", response.Code, response.Header(), response.Body.String())
	}
}

func TestNewInitialLoginStartHandlerRejectsUnsafeSuccessfulResult(t *testing.T) {
	t.Parallel()

	validState := base64.RawURLEncoding.EncodeToString(bytesOf(0x44, 32))
	for _, result := range []struct {
		name  string
		raw   string
		state string
	}{
		{name: "empty URL", state: validState},
		{name: "relative URL", raw: "/authorize", state: validState},
		{name: "script URL", raw: "javascript:alert(1)", state: validState},
		{name: "missing path", raw: "https://auth.example?state=" + validState, state: validState},
		{name: "credentials", raw: "https://user:pass@auth.example/authorize", state: validState},
		{name: "fragment", raw: "https://auth.example/authorize#state", state: validState},
		{name: "oversized URL", raw: "https://auth.example/authorize?state=" + strings.Repeat("a", maxOIDCAuthorizationURLBytes), state: validState},
		{name: "missing URL state", raw: "https://auth.example/authorize", state: validState},
		{name: "mismatched URL state", raw: "https://auth.example/authorize?state=other", state: validState},
		{name: "invalid state", raw: "https://auth.example/authorize?state=invalid", state: "invalid"},
	} {
		result := result
		t.Run(result.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newLoginStartHandler(
				func(context.Context, string) (string, string, error) { return result.raw, result.state, nil },
				initialLoginStateCookieSuffix, "session", callbackTestURLBuilder(t), true,
			)
			if err != nil {
				t.Fatalf("newLoginStartHandler() returned error: %v", err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login", nil))
			if response.Code != http.StatusInternalServerError || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Location") != "" {
				t.Fatalf("response = (status %d, headers %v)", response.Code, response.Header())
			}
		})
	}
}

func TestNewInitialLoginStartHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	begin := func(context.Context, string) (string, string, error) { return "", "", nil }
	for _, test := range []struct {
		name        string
		begin       func(context.Context, string) (string, string, error)
		stateSuffix string
		cookie      string
		builder     URLBuilder
	}{
		{name: "begin", stateSuffix: initialLoginStateCookieSuffix, cookie: "session", builder: builder},
		{name: "state suffix", begin: begin, stateSuffix: "_untrusted", cookie: "session", builder: builder},
		{name: "cookie", begin: begin, stateSuffix: initialLoginStateCookieSuffix, builder: builder},
		{name: "builder", begin: begin, stateSuffix: initialLoginStateCookieSuffix, cookie: "session"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := newLoginStartHandler(test.begin, test.stateSuffix, test.cookie, test.builder, true); err == nil || got != nil {
				t.Fatalf("newLoginStartHandler() = (%v, %v), want nil/error", got, err)
			}
		})
	}
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
