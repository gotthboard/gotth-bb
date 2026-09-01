package httpui

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

// newAuthenticationCallbackHandler constructs the exact GET callback boundary
// shared by initial login and revalidation. Exactly one matching fixed state-
// cookie namespace selects the completion path; the selected service boundary
// still verifies the durable attempt purpose. Revalidation additionally
// requires one canonical old session credential.
//
// Complexity: construction scans n cookie-name and p base-path bytes in tight
// Theta(n+p) time and retains Theta(p) path state. For q raw-query and c cookie-
// header bytes plus delegated completion cost D, successful request time is
// O(q+c+D+n+p), Omega(q+c), and auxiliary space O(q+c+p), Omega(q+c). q is
// capped at 8,192 bytes; no operation retries or detaches.
func newAuthenticationCallbackHandler(
	completeInitial func(context.Context, string, string) (completedBrowserLogin, error),
	completeRevalidation func(context.Context, string, string, string) (completedBrowserLogin, error),
	cookieName string,
	builder URLBuilder,
	secure bool,
) (http.Handler, error) {
	if completeInitial == nil {
		return nil, fmt.Errorf("initial login completion is required")
	}
	if completeRevalidation == nil {
		return nil, fmt.Errorf("revalidation completion is required")
	}
	validatedCookieName, err := config.ParseSessionCookieName(cookieName)
	if err != nil || validatedCookieName != cookieName {
		return nil, fmt.Errorf("authentication callback session cookie name is invalid")
	}
	if _, err := builder.CookiePath(); err != nil {
		return nil, fmt.Errorf("authentication callback URL builder is invalid: %w", err)
	}
	initialStateCookieName := cookieName + initialLoginStateCookieSuffix
	revalidationStateCookieName := cookieName + revalidationStateCookieSuffix
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
		initialStateCookies := request.CookiesNamed(initialStateCookieName)
		revalidationStateCookies := request.CookiesNamed(revalidationStateCookieName)
		if len(initialStateCookies) > 1 || len(revalidationStateCookies) > 1 {
			http.Error(response, "authentication failed", http.StatusBadRequest)
			return
		}
		matchesState := func(cookies []*http.Cookie) bool {
			return len(cookies) == 1 && !cookies[0].Quoted &&
				len(states[0]) == sessionCookieEncodedBytes && len(cookies[0].Value) == sessionCookieEncodedBytes &&
				subtle.ConstantTimeCompare([]byte(cookies[0].Value), []byte(states[0])) == 1
		}
		initial := matchesState(initialStateCookies)
		revalidation := matchesState(revalidationStateCookies)
		if initial == revalidation {
			http.Error(response, "authentication failed", http.StatusBadRequest)
			return
		}
		selectedStateCookieName := initialStateCookieName
		if revalidation {
			selectedStateCookieName = revalidationStateCookieName
		}
		expiredStateCookie, err := newInitialLoginStateCookie(cookieName, builder, secure, states[0])
		if err != nil {
			http.Error(response, "authentication failed", http.StatusBadRequest)
			return
		}
		expiredStateCookie.Name = selectedStateCookieName
		expiredStateCookie.Value = ""
		expiredStateCookie.Expires = time.Unix(1, 0).UTC()
		expiredStateCookie.MaxAge = -1
		http.SetCookie(response, &expiredStateCookie)
		var completed completedBrowserLogin
		if initial {
			completed, err = completeInitial(request.Context(), states[0], codes[0])
		} else {
			sessionCookies := request.CookiesNamed(cookieName)
			if len(sessionCookies) != 1 || sessionCookies[0].Quoted || len(sessionCookies[0].Value) != sessionCookieEncodedBytes {
				http.Error(response, "authentication failed", http.StatusBadRequest)
				return
			}
			var encoded [sessionCookieEncodedBytes]byte
			copy(encoded[:], sessionCookies[0].Value)
			defer clear(encoded[:])
			var decoded [sessionCookieTokenBytes]byte
			decodedLength, decodeErr := base64.RawURLEncoding.Strict().Decode(decoded[:], encoded[:])
			clear(decoded[:])
			if decodeErr != nil || decodedLength != sessionCookieTokenBytes {
				http.Error(response, "authentication failed", http.StatusBadRequest)
				return
			}
			completed, err = completeRevalidation(request.Context(), states[0], codes[0], sessionCookies[0].Value)
		}
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

const maxOIDCCallbackQueryBytes = 8192

type completedBrowserLogin struct {
	token      string
	returnPath string
	expiresAt  time.Time
}
