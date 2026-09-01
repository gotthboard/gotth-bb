package httpui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserSecurityHeadersSetsFixedPolicyBeforeHandler(t *testing.T) {
	t.Parallel()

	called := false
	handler := browserSecurityHeaders(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		called = true
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("handler ran before browser security headers were installed")
		}
		response.WriteHeader(http.StatusUnprocessableEntity)
	}))
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
