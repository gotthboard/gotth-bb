package httpui

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

const (
	sessionCookieTokenBytes   = 32
	sessionCookieEncodedBytes = 43
)

// newSessionCookie constructs one host-only opaque session cookie at the
// builder-owned application subtree. It validates the complete result before
// returning because net/http.SetCookie silently drops an invalid cookie name.
//
// Complexity: for n cookie-name bytes and p cookie-path bytes, valid-input time
// is tight Theta(n+p), with O(n+p) worst-case and Omega(1) on an early failure;
// valid-input auxiliary and retained-result space are O(p), Omega(p), and tight
// Theta(p) because URLBuilder allocates the cookie path; credential decode uses
// fixed-size stack buffers. URLBuilder and http.Cookie own delegated path
// construction and final RFC cookie validation.
func newSessionCookie(name string, builder URLBuilder, secure bool, token string, expiresAt time.Time) (http.Cookie, error) {
	validatedName, err := config.ParseSessionCookieName(name)
	if err != nil || validatedName != name {
		return http.Cookie{}, fmt.Errorf("session cookie name is invalid")
	}
	if expiresAt.IsZero() {
		return http.Cookie{}, fmt.Errorf("session cookie expiry is required")
	}
	if len(token) != sessionCookieEncodedBytes {
		return http.Cookie{}, fmt.Errorf("session cookie credential has an invalid length")
	}
	var encoded [sessionCookieEncodedBytes]byte
	copy(encoded[:], token)
	defer clear(encoded[:])
	var decoded [sessionCookieTokenBytes]byte
	decodedLength, err := base64.RawURLEncoding.Strict().Decode(decoded[:], encoded[:])
	defer clear(decoded[:])
	if err != nil || decodedLength != sessionCookieTokenBytes {
		return http.Cookie{}, fmt.Errorf("session cookie credential has an invalid encoding")
	}
	path, err := builder.CookiePath()
	if err != nil {
		return http.Cookie{}, fmt.Errorf("build session cookie path: %w", err)
	}
	cookie := http.Cookie{
		Name:     name,
		Value:    token,
		Path:     path,
		Expires:  expiresAt.UTC(),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if err := cookie.Valid(); err != nil {
		return http.Cookie{}, fmt.Errorf("session cookie is invalid: %w", err)
	}
	return cookie, nil
}
