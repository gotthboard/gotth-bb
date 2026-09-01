package config

import (
	"fmt"
	"net/http"
	"strings"
)

const defaultSessionCookieName = "gotth_bb_session"

// ParseSessionCookieName validates the configured RFC cookie token and avoids
// prefix semantics that conflict with path-prefixed or HTTP development.
//
// Complexity: for n input bytes, time O(n), Omega(1) on an early prefix/token
// failure, and tight Theta(n) for valid caller input; auxiliary space O(1),
// Omega(1), and tight Theta(1). http.Cookie.Valid performs the delegated byte
// scan, and the returned caller string is not copied.
func ParseSessionCookieName(raw string) (string, error) {
	if raw == "" {
		return defaultSessionCookieName, nil
	}
	for _, prefix := range [...]string{"__Host-Http-", "__Host-", "__Secure-", "__Http-"} {
		if len(raw) >= len(prefix) && strings.EqualFold(raw[:len(prefix)], prefix) {
			return "", fmt.Errorf("SESSION_COOKIE_NAME must not use browser cookie prefixes")
		}
	}
	if err := (&http.Cookie{Name: raw}).Valid(); err != nil {
		return "", fmt.Errorf("SESSION_COOKIE_NAME must be a valid HTTP cookie token")
	}

	return raw, nil
}
