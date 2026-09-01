package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ParsePublicBaseURL validates the configured external origin and binds its
// path to the separately configured browser prefix. Callers must retain the
// returned value rather than reconstructing public URLs from request headers.
//
// Complexity: for n URL bytes and m base-path bytes, validation is O(n+m)
// worst-case, Ω(1) on an early structural failure, and Θ(n+m) for valid
// input. Auxiliary space is O(n+m), dominated by url.Parse and delegated base-
// path validation; both delegated parsers are linear in their input sizes.
func ParsePublicBaseURL(raw, basePath string, production bool) (url.URL, error) {
	canonicalPath, err := ParseBasePath(basePath)
	if err != nil {
		return url.URL{}, err
	}
	if raw == "" {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL is invalid")
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL must be an absolute hierarchical URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL must use HTTP or HTTPS")
	}
	if production && parsed.Scheme != "https" {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL must use HTTPS in production")
	}
	if parsed.User != nil {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL must not contain a query")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL must not contain a fragment")
	}
	if parsed.Path != canonicalPath {
		return url.URL{}, fmt.Errorf("PUBLIC_BASE_URL path must equal BASE_PATH")
	}

	return *parsed, nil
}
