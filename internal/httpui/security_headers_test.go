package httpui

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/observability"
)

func TestBrowserSecurityHeadersSetsFixedPolicyBeforeHandler(t *testing.T) {
	t.Parallel()

	called := false
	handler, err := NewBrowserSecurityHandler(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		called = true
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("handler ran before browser security headers were installed")
		}
		response.WriteHeader(http.StatusUnprocessableEntity)
	}))
	if err != nil {
		t.Fatalf("NewBrowserSecurityHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/invalid", nil))
	if !called || response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("called = %v, status = %d", called, response.Code)
	}
	want := map[string]string{
		"Content-Security-Policy":      "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Origin-Agent-Cluster":         "?1",
		"Permissions-Policy":           "camera=(), geolocation=(), microphone=(), payment=(), usb=()",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
	}
	for name, value := range want {
		if got := response.Header().Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if got := response.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("Strict-Transport-Security = %q, want edge-owned empty value", got)
	}
}

func TestBrowserSecurityHeadersSurviveRecoveryResponse(t *testing.T) {
	t.Parallel()

	recovery, err := observability.NewRecoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("test panic")
	}), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
	}
	handler, err := NewBrowserSecurityHandler(recovery)
	if err != nil {
		t.Fatalf("NewBrowserSecurityHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Security-Policy") != browserContentSecurityPolicy {
		t.Fatalf("recovery response = (%d, CSP %q)", response.Code, response.Header().Get("Content-Security-Policy"))
	}
}

func TestNewBrowserSecurityHandlerRejectsNilHandler(t *testing.T) {
	t.Parallel()

	if got, err := NewBrowserSecurityHandler(nil); err == nil || got != nil {
		t.Fatalf("NewBrowserSecurityHandler(nil) = (%v, %v), want (nil, error)", got, err)
	}
}
