package httpui

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

type sessionAuthenticator func(context.Context, string) (auth.SessionAuthentication, error)

// newSessionAuthenticationHandler loads at most one exact opaque session
// cookie, resolves its current local access snapshot, and places that snapshot
// in the downstream request context. Missing, malformed, duplicate, quoted,
// and inactive credentials are anonymous; browser state that cannot
// authenticate is expired. Store failures stop the request with a generic 503.
//
// Complexity: construction scans n cookie-name and p path bytes in O(n+p),
// Omega(1), and tight Theta(n+p) for valid input. For c bounded Cookie-header
// bytes and delegated authentication cost A, request time is O(c+A), Omega(c),
// with no tighter Theta bound because A may perform PostgreSQL I/O. Request
// auxiliary space is O(c), owned primarily by net/http cookie parsing, plus at
// most two fixed context nodes. No authentication operation is retried or
// detached.
func newSessionAuthenticationHandler(
	next http.Handler,
	authenticate sessionAuthenticator,
	cookieName string,
	builder URLBuilder,
	secure bool,
) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("session authentication downstream handler is required")
	}
	if authenticate == nil {
		return nil, fmt.Errorf("session authenticator is required")
	}
	validatedCookieName, err := config.ParseSessionCookieName(cookieName)
	if err != nil || validatedCookieName != cookieName {
		return nil, fmt.Errorf("session authentication cookie name is invalid")
	}
	cookiePath, err := builder.CookiePath()
	if err != nil {
		return nil, fmt.Errorf("session authentication URL builder is invalid: %w", err)
	}
	expiredCookie := http.Cookie{
		Name:     cookieName,
		Path:     cookiePath,
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if err := expiredCookie.Valid(); err != nil {
		return nil, fmt.Errorf("expired session cookie is invalid: %w", err)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Add("Vary", "Cookie")
		cookies := request.CookiesNamed(cookieName)
		authentication := auth.SessionAuthentication{}
		csrfToken := ""
		expireBrowserState := false
		if len(cookies) == 1 && !cookies[0].Quoted {
			resolved, authenticationErr := authenticate(request.Context(), cookies[0].Value)
			if authenticationErr != nil {
				response.Header().Set("Cache-Control", "no-store")
				http.Error(response, "authentication unavailable", http.StatusServiceUnavailable)
				return
			}
			authentication = resolved
			expireBrowserState = !authentication.Access.Authenticated
			if authentication.Access.Authenticated {
				csrfToken, authenticationErr = deriveCSRFToken(cookies[0].Value)
				if authenticationErr != nil {
					http.SetCookie(response, &expiredCookie)
					response.Header().Set("Cache-Control", "no-store")
					http.Error(response, "authentication failed", http.StatusInternalServerError)
					return
				}
			}
		} else if len(cookies) != 0 {
			expireBrowserState = true
		}
		if expireBrowserState {
			http.SetCookie(response, &expiredCookie)
		}
		requestContext := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, authentication)
		if csrfToken != "" {
			requestContext = context.WithValue(requestContext, csrfTokenContextKey{}, csrfToken)
		}
		downstreamRequest := request.WithContext(requestContext)
		defer func() { request.Pattern = downstreamRequest.Pattern }()
		next.ServeHTTP(response, downstreamRequest)
	}), nil
}
