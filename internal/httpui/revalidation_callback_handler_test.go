package httpui

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewAuthenticationCallbackHandlerCompletesRevalidationAndRotatesCookies(t *testing.T) {
	t.Parallel()

	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32))
	oldToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x73}, 32))
	newToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x74}, 32))
	expiresAt := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	calls := 0
	handler, err := newAuthenticationCallbackHandler(
		func(context.Context, string, string) (completedBrowserLogin, error) {
			panic("initial completion must not run")
		},
		func(_ context.Context, gotState, code, gotOldToken string) (completedBrowserLogin, error) {
			calls++
			if gotState != state || code != "revalidation-code" || gotOldToken != oldToken {
				t.Fatalf("revalidation completion = (%q, %q, %q)", gotState, code, gotOldToken)
			}
			return completedBrowserLogin{token: newToken, returnPath: "/bb/topics/9", expiresAt: expiresAt}, nil
		},
		"gotth_bb_session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newAuthenticationCallbackHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?code=revalidation-code&state="+state, nil)
	request.AddCookie(&http.Cookie{Name: "gotth_bb_session_oidc_revalidate_state", Value: state})
	request.AddCookie(&http.Cookie{Name: "gotth_bb_session", Value: oldToken})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if response.Code != http.StatusSeeOther || calls != 1 || response.Header().Get("Location") != "/bb/topics/9" ||
		len(cookies) != 2 || cookies[0].Name != "gotth_bb_session_oidc_revalidate_state" || cookies[0].Value != "" ||
		cookies[0].MaxAge != -1 || !cookies[0].Expires.Equal(time.Unix(1, 0).UTC()) ||
		cookies[1].Name != "gotth_bb_session" || cookies[1].Value != newToken || !cookies[1].Expires.Equal(expiresAt) {
		t.Fatalf("revalidation response = (status %d, calls %d, location %q, cookies %+v)",
			response.Code, calls, response.Header().Get("Location"), cookies)
	}
}

func TestNewAuthenticationCallbackHandlerRejectsRevalidationWithoutOneCanonicalOldSession(t *testing.T) {
	t.Parallel()

	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x75}, 32))
	validToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x76}, 32))
	invalidToken := validToken[:len(validToken)-1] + "*"
	for _, test := range []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "duplicate", header: "gotth_bb_session=" + validToken + "; gotth_bb_session=" + validToken},
		{name: "quoted", header: `gotth_bb_session="` + validToken + `"`},
		{name: "short", header: "gotth_bb_session=short"},
		{name: "invalid encoding", header: "gotth_bb_session=" + invalidToken},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newAuthenticationCallbackHandler(
				func(context.Context, string, string) (completedBrowserLogin, error) {
					panic("initial completion must not run")
				},
				func(context.Context, string, string, string) (completedBrowserLogin, error) {
					panic("revalidation completion must not run")
				},
				"gotth_bb_session", callbackTestURLBuilder(t), true,
			)
			if err != nil {
				t.Fatalf("newAuthenticationCallbackHandler() returned error: %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code&state="+state, nil)
			request.AddCookie(&http.Cookie{Name: "gotth_bb_session_oidc_revalidate_state", Value: state})
			if test.header != "" {
				request.Header.Add("Cookie", test.header)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			serialized := response.Header().Values("Set-Cookie")
			if response.Code != http.StatusBadRequest || len(serialized) != 1 ||
				!strings.Contains(serialized[0], "gotth_bb_session_oidc_revalidate_state=;") ||
				response.Header().Get("Location") != "" {
				t.Fatalf("response = (status %d, headers %v)", response.Code, response.Header())
			}
		})
	}
}

func TestNewAuthenticationCallbackHandlerRejectsAmbiguousStateNamespaces(t *testing.T) {
	t.Parallel()

	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x77}, 32))
	handler, err := newAuthenticationCallbackHandler(
		func(context.Context, string, string) (completedBrowserLogin, error) {
			panic("initial completion must not run")
		},
		func(context.Context, string, string, string) (completedBrowserLogin, error) {
			panic("revalidation completion must not run")
		},
		"gotth_bb_session", callbackTestURLBuilder(t), true,
	)
	if err != nil {
		t.Fatalf("newAuthenticationCallbackHandler() returned error: %v", err)
	}
	for _, header := range []string{
		"gotth_bb_session_oidc_state=" + state + "; gotth_bb_session_oidc_revalidate_state=" + state,
		"gotth_bb_session_oidc_state=other; gotth_bb_session_oidc_revalidate_state=other",
	} {
		request := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code&state="+state, nil)
		request.Header.Set("Cookie", header)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || response.Header().Get("Set-Cookie") != "" {
			t.Fatalf("ambiguous response = (status %d, headers %v)", response.Code, response.Header())
		}
	}
}

func TestNewAuthenticationCallbackHandlerRequiresBothCompletionBoundaries(t *testing.T) {
	t.Parallel()

	initial := func(context.Context, string, string) (completedBrowserLogin, error) {
		return completedBrowserLogin{}, nil
	}
	revalidate := func(context.Context, string, string, string) (completedBrowserLogin, error) {
		return completedBrowserLogin{}, nil
	}
	for _, test := range []struct {
		initial    func(context.Context, string, string) (completedBrowserLogin, error)
		revalidate func(context.Context, string, string, string) (completedBrowserLogin, error)
	}{
		{revalidate: revalidate},
		{initial: initial},
	} {
		if got, err := newAuthenticationCallbackHandler(
			test.initial, test.revalidate, "gotth_bb_session", callbackTestURLBuilder(t), true,
		); err == nil || got != nil {
			t.Fatalf("newAuthenticationCallbackHandler() = (%v, %v), want nil/error", got, err)
		}
	}
}
