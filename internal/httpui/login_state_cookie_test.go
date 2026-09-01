package httpui

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestNewInitialLoginStateCookieConstructsBoundedBrowserState(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6d}, 32))
	cookie, err := newInitialLoginStateCookie("gotth_bb_session", builder, true, state)
	if err != nil {
		t.Fatalf("newInitialLoginStateCookie() returned error: %v", err)
	}
	if cookie.Name != "gotth_bb_session_oidc_state" || cookie.Value != state || cookie.Path != "/bb/" ||
		!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.MaxAge != 300 || cookie.Domain != "" || !cookie.Expires.IsZero() {
		t.Fatalf("cookie = %+v", cookie)
	}
	if err := cookie.Valid(); err != nil {
		t.Fatalf("cookie.Valid() returned error: %v", err)
	}
	header := cookie.String()
	for _, required := range []string{
		"gotth_bb_session_oidc_state=" + state,
		"Path=/bb/",
		"Max-Age=300",
		"HttpOnly",
		"Secure",
		"SameSite=Lax",
	} {
		if !strings.Contains(header, required) {
			t.Fatalf("cookie header %q lacks %q", header, required)
		}
	}
}

func TestNewInitialLoginStateCookieSupportsHTTPDevelopment(t *testing.T) {
	t.Parallel()

	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, 32))
	cookie, err := newInitialLoginStateCookie("session", callbackTestURLBuilder(t), false, state)
	if err != nil || cookie.Secure || strings.Contains(cookie.String(), "; Secure") {
		t.Fatalf("development cookie = (%+v, %v)", cookie, err)
	}
}

func TestNewInitialLoginStateCookieRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	builder := callbackTestURLBuilder(t)
	validState := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
	for _, test := range []struct {
		name    string
		cookie  string
		builder URLBuilder
		state   string
	}{
		{name: "empty configured name", builder: builder, state: validState},
		{name: "invalid configured name", cookie: "bad name", builder: builder, state: validState},
		{name: "browser prefix", cookie: "__Host-session", builder: builder, state: validState},
		{name: "zero builder", cookie: "session", state: validState},
		{name: "empty state", cookie: "session", builder: builder},
		{name: "short state", cookie: "session", builder: builder, state: base64.RawURLEncoding.EncodeToString(make([]byte, 31))},
		{name: "invalid state encoding", cookie: "session", builder: builder, state: validState[:len(validState)-1] + "*"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := newInitialLoginStateCookie(test.cookie, test.builder, true, test.state); err == nil || !reflect.DeepEqual(got, http.Cookie{}) {
				t.Fatalf("newInitialLoginStateCookie() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
}
