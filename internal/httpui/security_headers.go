package httpui

import (
	"fmt"
	"net/http"
)

const browserContentSecurityPolicy = "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'"

// NewBrowserSecurityHandler installs the fixed browser policy before the full
// request-ID, logging, recovery, and application chain writes a success,
// redirect, fragment, or error response. TLS and HSTS remain the edge proxy's
// responsibility because the application intentionally serves loopback HTTP.
//
// Complexity: construction and each request add tight Theta(1) time and
// auxiliary space: eight fixed header assignments and one delegated handler
// call. Header values have constant bounded length.
func NewBrowserSecurityHandler(next http.Handler) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("browser security handler is required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		headers := response.Header()
		headers.Set("Content-Security-Policy", browserContentSecurityPolicy)
		headers.Set("Cross-Origin-Opener-Policy", "same-origin")
		headers.Set("Cross-Origin-Resource-Policy", "same-origin")
		headers.Set("Origin-Agent-Cluster", "?1")
		headers.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	}), nil
}
