package httpui

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

type sessionRevoker func(context.Context, string) (bool, error)
type csrfValidator func(*http.Request) error

const logoutVerificationFailureQuery = "logout=verification-failed"

// newLogoutHandler constructs the exact authenticated POST boundary for local
// logout. It requires CSRF validation before reading the credential for one
// server-side revocation, and expires browser state only after that operation
// succeeds or reports an idempotent no-op. Revalidation staleness never blocks
// logout. A stale verification token redirects to one fixed recovery marker
// without changing session state. Other failures are generic and non-cacheable.
//
// Complexity: construction scans n cookie-name and p path bytes in O(n+p),
// Omega(1), and tight Theta(n+p) for valid input. For bounded Cookie-header
// bytes c plus delegated CSRF cost C and revocation cost R, successful request
// time is O(c+C+R), Omega(c), with no tighter Theta bound because R may perform
// PostgreSQL I/O. Request auxiliary space is O(c), owned by net/http parsing.
// No validation or revocation operation is retried or detached.
func newLogoutHandler(
	revoke sessionRevoker,
	validateCSRF csrfValidator,
	cookieName string,
	builder URLBuilder,
	secure bool,
) (http.Handler, error) {
	if revoke == nil {
		return nil, fmt.Errorf("logout session revoker is required")
	}
	if validateCSRF == nil {
		return nil, fmt.Errorf("logout CSRF validator is required")
	}
	validatedCookieName, err := config.ParseSessionCookieName(cookieName)
	if err != nil || validatedCookieName != cookieName {
		return nil, fmt.Errorf("logout session cookie name is invalid")
	}
	returnPath, err := builder.CookiePath()
	if err != nil {
		return nil, fmt.Errorf("logout URL builder is invalid: %w", err)
	}
	verificationFailurePath := returnPath + "?" + logoutVerificationFailureQuery
	expiredCookie := http.Cookie{
		Name:     cookieName,
		Path:     returnPath,
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if err := expiredCookie.Valid(); err != nil {
		return nil, fmt.Errorf("expired logout cookie is invalid: %w", err)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Pragma", "no-cache")
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated {
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		if err := validateCSRF(request); err != nil {
			response.Header().Set("Location", verificationFailurePath)
			response.WriteHeader(http.StatusSeeOther)
			return
		}
		cookies := request.CookiesNamed(cookieName)
		if len(cookies) != 1 || cookies[0].Quoted {
			http.Error(response, "logout failed", http.StatusBadRequest)
			return
		}
		if _, err := revoke(request.Context(), cookies[0].Value); err != nil {
			http.Error(response, "logout unavailable", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(response, &expiredCookie)
		response.Header().Set("Location", returnPath)
		response.WriteHeader(http.StatusSeeOther)
	}), nil
}
