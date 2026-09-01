package httpui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/config"
)

const (
	maxInitialLoginQueryBytes    = 8192
	maxOIDCAuthorizationURLBytes = 8192
)

// newLoginStartHandler constructs the exact GET login-start boundary for
// either initial login or revalidation. The caller selects one of the two fixed
// state-cookie namespaces; browser input never supplies that selection. It
// validates an optional internal return target before invoking the service once,
// then validates and commits one state cookie plus one provider redirect. Every
// failure is generic, non-cacheable, and returns no browser state.
//
// Complexity: construction scans n cookie-name and p base-path bytes in tight
// Theta(n+p) time and retains Theta(p) builder state. For q request-query bytes,
// u authorization-URL bytes, and delegated begin cost D, successful request
// time is O(q*log(q)+u+D+n+p), Omega(q+u), and auxiliary space O(q+u+n+p).
// q and u are each capped at 8,192 bytes; D performs one bounded database write
// without retry.
func newLoginStartHandler(
	begin func(context.Context, string) (string, string, error),
	stateCookieSuffix string,
	sessionCookieName string,
	builder URLBuilder,
	secure bool,
) (http.Handler, error) {
	if begin == nil {
		return nil, fmt.Errorf("initial login start is required")
	}
	if stateCookieSuffix != initialLoginStateCookieSuffix && stateCookieSuffix != revalidationStateCookieSuffix {
		return nil, fmt.Errorf("login state cookie suffix is invalid")
	}
	validatedCookieName, err := config.ParseSessionCookieName(sessionCookieName)
	if err != nil || validatedCookieName != sessionCookieName {
		return nil, fmt.Errorf("initial login session cookie name is invalid")
	}
	defaultReturnPath, err := builder.CookiePath()
	if err != nil {
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
		if len(request.URL.RawQuery) > maxInitialLoginQueryBytes {
			http.Error(response, "authentication failed", http.StatusRequestURITooLong)
			return
		}
		returnPath := defaultReturnPath
		if request.URL.RawQuery != "" || request.URL.ForceQuery {
			query, queryErr := url.ParseQuery(request.URL.RawQuery)
			returns, present := query["return"]
			if queryErr != nil || len(query) != 1 || !present || len(returns) != 1 || returns[0] == "" {
				http.Error(response, "authentication failed", http.StatusBadRequest)
				return
			}
			validatedReturnPath, validationErr := builder.ValidateReturnPath(returns[0])
			if validationErr != nil || validatedReturnPath == "" {
				http.Error(response, "authentication failed", http.StatusBadRequest)
				return
			}
			returnPath = validatedReturnPath
		}
		authorizationURL, state, err := begin(request.Context(), returnPath)
		if err != nil {
			http.Error(response, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		if len(authorizationURL) == 0 || len(authorizationURL) > maxOIDCAuthorizationURLBytes || !utf8.ValidString(authorizationURL) {
			http.Error(response, "authentication failed", http.StatusInternalServerError)
			return
		}
		parsedAuthorizationURL, err := url.Parse(authorizationURL)
		if err != nil || parsedAuthorizationURL.Scheme != "http" && parsedAuthorizationURL.Scheme != "https" ||
			parsedAuthorizationURL.Host == "" || parsedAuthorizationURL.Hostname() == "" || parsedAuthorizationURL.Path == "" || parsedAuthorizationURL.User != nil ||
			parsedAuthorizationURL.Fragment != "" || parsedAuthorizationURL.String() != authorizationURL {
			http.Error(response, "authentication failed", http.StatusInternalServerError)
			return
		}
		authorizationQuery, err := url.ParseQuery(parsedAuthorizationURL.RawQuery)
		states, statePresent := authorizationQuery["state"]
		if err != nil || authorizationQuery.Encode() != parsedAuthorizationURL.RawQuery || !statePresent || len(states) != 1 || states[0] != state {
			http.Error(response, "authentication failed", http.StatusInternalServerError)
			return
		}
		cookie, err := newInitialLoginStateCookie(sessionCookieName, builder, secure, state)
		if err != nil {
			http.Error(response, "authentication failed", http.StatusInternalServerError)
			return
		}
		cookie.Name = sessionCookieName + stateCookieSuffix
		http.SetCookie(response, &cookie)
		response.Header().Set("Location", authorizationURL)
		response.WriteHeader(http.StatusSeeOther)
	}), nil
}
