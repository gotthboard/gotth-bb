package config

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const registrationFlowPathPrefix = "/if/flow/"

// ParseRegistrationURL validates one canonical Authentik enrollment executor
// on the exact configured issuer origin. The application owns all query values
// added later; configuration cannot smuggle a return target.
//
// Complexity: for n configured URL bytes and i issuer bytes, time and
// auxiliary space are O(n+i), Omega(1), with tight Theta(n+i) for valid input.
func ParseRegistrationURL(raw string, issuer url.URL, production bool) (url.URL, error) {
	canonicalIssuer, err := ParseOIDCIssuerURL(issuer.String(), production)
	if err != nil {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL requires a valid OIDC issuer")
	}
	if len(raw) == 0 || len(raw) > 2048 || !utf8.ValidString(raw) {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL has an invalid size or encoding")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.Opaque != "" || parsed.User != nil || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.String() != raw {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL must be a canonical absolute URL without credentials, query, or fragment")
	}
	if production && parsed.Scheme != "https" {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL must use HTTPS in production")
	}
	if parsed.Scheme != canonicalIssuer.Scheme || parsed.Host != canonicalIssuer.Host {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL must use the exact OIDC issuer origin")
	}
	flowPath, ok := strings.CutPrefix(parsed.Path, registrationFlowPathPrefix)
	if !ok || !strings.HasSuffix(flowPath, "/") {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL must use an Authentik enrollment flow path")
	}
	slug := strings.TrimSuffix(flowPath, "/")
	if slug == "" || strings.ContainsRune(slug, '/') {
		return url.URL{}, fmt.Errorf("REGISTRATION_URL must contain exactly one flow slug")
	}
	for _, character := range slug {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return url.URL{}, fmt.Errorf("REGISTRATION_URL contains an invalid flow slug")
	}
	return *parsed, nil
}
