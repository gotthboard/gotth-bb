package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseOIDCIssuerURL validates and preserves the exact Authentik issuer used
// for discovery and token issuer comparison.
//
// Complexity: for n input bytes, time O(n), Omega(1) on an early structural
// failure, and tight Theta(n) for valid input; auxiliary and retained result
// space are O(n), Omega(1), and tight Theta(n) for valid input. url.Parse and
// URL.String perform the delegated scans; diagnostics never retain raw input.
func ParseOIDCIssuerURL(raw string, production bool) (url.URL, error) {
	if raw == "" {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL is invalid")
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must be an absolute hierarchical URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must use HTTP or HTTPS")
	}
	if production && parsed.Scheme != "https" {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must use HTTPS in production")
	}
	const providerPathPrefix = "/application/o/"
	if !strings.HasPrefix(parsed.Path, providerPathPrefix) || !strings.HasSuffix(parsed.Path, "/") {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must use the Authentik per-provider issuer path")
	}
	applicationSlug := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, providerPathPrefix), "/")
	if applicationSlug == "" || strings.Contains(applicationSlug, "/") {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must contain exactly one Authentik application slug")
	}
	if parsed.RawPath != "" {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL application slug must not use percent encoding")
	}
	for index := 0; index < len(applicationSlug); index++ {
		character := applicationSlug[index]
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' {
			return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL contains an invalid Authentik application slug")
		}
	}
	switch applicationSlug {
	case "authorize", "token", "device", "userinfo", "introspect", "revoke":
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL uses a reserved Authentik application slug")
	}
	if parsed.User != nil {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must not contain a query")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must not contain a fragment")
	}
	if parsed.String() != raw {
		return url.URL{}, fmt.Errorf("OIDC_ISSUER_URL must use its exact canonical encoding")
	}

	return *parsed, nil
}
