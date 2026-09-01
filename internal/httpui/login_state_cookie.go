package httpui

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

const (
	initialLoginStateCookieSuffix        = "_oidc_state"
	revalidationStateCookieSuffix        = "_oidc_revalidate_state"
	initialLoginStateCookieMaximumAgeSec = 5 * 60
)

// newInitialLoginStateCookie constructs the one-browser login-attempt binding.
// Its name is derived from the configured session-cookie name so it cannot
// collide with that cookie, and Max-Age matches the fixed server attempt life.
//
// Complexity: for n session-cookie-name bytes and p cookie-path bytes,
// valid-input time and auxiliary result space are tight Theta(n+p); state
// decoding uses fixed-size stack buffers. Early invalid input is Omega(1), and
// URLBuilder plus http.Cookie own path construction and final validation.
func newInitialLoginStateCookie(sessionCookieName string, builder URLBuilder, secure bool, state string) (http.Cookie, error) {
	validatedName, err := config.ParseSessionCookieName(sessionCookieName)
	if err != nil || validatedName != sessionCookieName {
		return http.Cookie{}, fmt.Errorf("login state cookie session name is invalid")
	}
	if len(state) != sessionCookieEncodedBytes {
		return http.Cookie{}, fmt.Errorf("login state cookie credential has an invalid length")
	}
	var encoded [sessionCookieEncodedBytes]byte
	copy(encoded[:], state)
	defer clear(encoded[:])
	var decoded [sessionCookieTokenBytes]byte
	decodedLength, err := base64.RawURLEncoding.Strict().Decode(decoded[:], encoded[:])
	defer clear(decoded[:])
	if err != nil || decodedLength != sessionCookieTokenBytes {
		return http.Cookie{}, fmt.Errorf("login state cookie credential has an invalid encoding")
	}
	path, err := builder.CookiePath()
	if err != nil {
		return http.Cookie{}, fmt.Errorf("build login state cookie path: %w", err)
	}
	cookie := http.Cookie{
		Name:     sessionCookieName + initialLoginStateCookieSuffix,
		Value:    state,
		Path:     path,
		MaxAge:   initialLoginStateCookieMaximumAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if err := cookie.Valid(); err != nil {
		return http.Cookie{}, fmt.Errorf("login state cookie is invalid: %w", err)
	}
	return cookie, nil
}
