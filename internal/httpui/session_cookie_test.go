package httpui

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

func TestNewSessionCookieBuildsExactPathScopedCredential(t *testing.T) {
	t.Parallel()

	publicURL, err := config.ParsePublicBaseURL("https://forum.example/bb", "/bb", true)
	if err != nil {
		t.Fatalf("ParsePublicBaseURL() returned error: %v", err)
	}
	builder, err := NewURLBuilder(publicURL, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, 32))
	expiresAt := time.Date(2026, time.September, 2, 12, 0, 0, 123456000, time.UTC)
	cookie, err := newSessionCookie("gotth_bb_session", builder, true, token, expiresAt)
	if err != nil {
		t.Fatalf("newSessionCookie() returned error: %v", err)
	}
	if cookie.Name != "gotth_bb_session" || cookie.Value != token || cookie.Path != "/bb/" ||
		!cookie.Expires.Equal(expiresAt) || !cookie.HttpOnly || !cookie.Secure ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" || cookie.MaxAge != 0 {
		t.Fatalf("cookie = %+v", cookie)
	}
	if err := cookie.Valid(); err != nil {
		t.Fatalf("cookie.Valid() returned error: %v", err)
	}
	header := cookie.String()
	for _, required := range []string{
		"gotth_bb_session=" + token,
		"Path=/bb/",
		"Expires=Wed, 02 Sep 2026 12:00:00 GMT",
		"HttpOnly",
		"Secure",
		"SameSite=Lax",
	} {
		if !strings.Contains(header, required) {
			t.Fatalf("cookie header %q lacks %q", header, required)
		}
	}
}

func TestNewSessionCookieSupportsExplicitHTTPDevelopment(t *testing.T) {
	t.Parallel()

	publicURL, err := config.ParsePublicBaseURL("http://127.0.0.1:8080/bb", "/bb", false)
	if err != nil {
		t.Fatalf("ParsePublicBaseURL() returned error: %v", err)
	}
	builder, err := NewURLBuilder(publicURL, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	cookie, err := newSessionCookie("session", builder, false, token, time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC))
	if err != nil || cookie.Secure || strings.Contains(cookie.String(), "; Secure") {
		t.Fatalf("development cookie = (%+v, %v)", cookie, err)
	}
}

func TestNewSessionCookieRejectsInvalidInputsWithoutCookie(t *testing.T) {
	t.Parallel()

	publicURL, err := config.ParsePublicBaseURL("https://forum.example/bb", "/bb", true)
	if err != nil {
		t.Fatalf("ParsePublicBaseURL() returned error: %v", err)
	}
	builder, err := NewURLBuilder(publicURL, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	validToken := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	validExpiry := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		cookie  string
		builder URLBuilder
		token   string
		expires time.Time
	}{
		{name: "empty name", builder: builder, token: validToken, expires: validExpiry},
		{name: "invalid name", cookie: "bad name", builder: builder, token: validToken, expires: validExpiry},
		{name: "browser prefix", cookie: "__Host-session", builder: builder, token: validToken, expires: validExpiry},
		{name: "zero builder", cookie: "session", token: validToken, expires: validExpiry},
		{name: "empty token", cookie: "session", builder: builder, expires: validExpiry},
		{name: "short token", cookie: "session", builder: builder, token: base64.RawURLEncoding.EncodeToString(make([]byte, 31)), expires: validExpiry},
		{name: "invalid token encoding", cookie: "session", builder: builder, token: validToken[:len(validToken)-1] + "*", expires: validExpiry},
		{name: "zero expiry", cookie: "session", builder: builder, token: validToken},
		{name: "invalid expiry", cookie: "session", builder: builder, token: validToken, expires: time.Date(1500, time.January, 1, 0, 0, 0, 0, time.UTC)},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := newSessionCookie(test.cookie, test.builder, true, test.token, test.expires); err == nil || !reflect.DeepEqual(got, http.Cookie{}) {
				t.Fatalf("newSessionCookie() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
}
