package httpui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

const maxOIDCCallbackQueryBytes = 8192

type completedBrowserLogin struct {
	token      string
	returnPath string
	expiresAt  time.Time
}

// newInitialLoginCallbackHandler constructs the exact GET callback boundary.
// It admits only one state and code, invokes completion once, revalidates the
// internal redirect, validates the session cookie, then sets both headers before
// committing one 303 response. Every failure response is non-cacheable and
// generic.
//
// Complexity: construction scans n cookie-name and p base-path bytes in
// O(n+p), Omega(1), and tight Theta(n+p) for valid input, retaining Theta(p)
// path state in the builder copy. For q raw-query bytes and delegated completion
// cost D, successful request time is O(q+D+n+p), Omega(q), with no tighter
// Theta bound because D includes network/database I/O; auxiliary space is
// O(q+p), Omega(q) on a valid callback. q is capped at 8,192 bytes.
func newInitialLoginCallbackHandler(
	complete func(context.Context, string, string) (completedBrowserLogin, error),
	cookieName string,
	builder URLBuilder,
	secure bool,
) (http.Handler, error) {
	if complete == nil {
		return nil, fmt.Errorf("initial login completion is required")
	}
	validatedCookieName, err := config.ParseSessionCookieName(cookieName)
	if err != nil || validatedCookieName != cookieName {
		return nil, fmt.Errorf("initial login session cookie name is invalid")
	}
	if _, err := builder.CookiePath(); err != nil {
		return nil, fmt.Errorf("initial login URL builder is invalid: %w", err)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Pragma", "no-cache")
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(request.URL.RawQuery) > maxOIDCCallbackQueryBytes {
			http.Error(response, "authentication failed", http.StatusRequestURITooLong)
			return
		}
		query, err := url.ParseQuery(request.URL.RawQuery)
		states, statePresent := query["state"]
		codes, codePresent := query["code"]
		if err != nil || len(query) != 2 || !statePresent || len(states) != 1 || states[0] == "" ||
			!codePresent || len(codes) != 1 || codes[0] == "" {
			http.Error(response, "authentication failed", http.StatusBadRequest)
			return
		}
		completed, err := complete(request.Context(), states[0], codes[0])
		if err != nil {
			http.Error(response, "authentication failed", http.StatusBadRequest)
			return
		}
		returnPath, err := builder.ValidateReturnPath(completed.returnPath)
		if err != nil || returnPath == "" {
			http.Error(response, "authentication failed", http.StatusInternalServerError)
			return
		}
		cookie, err := newSessionCookie(cookieName, builder, secure, completed.token, completed.expiresAt)
		if err != nil {
			http.Error(response, "authentication failed", http.StatusInternalServerError)
			return
		}
		http.SetCookie(response, &cookie)
		response.Header().Set("Location", returnPath)
		response.WriteHeader(http.StatusSeeOther)
	}), nil
}
