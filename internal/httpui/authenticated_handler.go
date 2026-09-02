package httpui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
)

const maxLogoutFormBytes = 4096

// AuthenticationService is the exact initial-login and local-session surface
// consumed by the browser router.
type AuthenticationService interface {
	BeginInitialLogin(context.Context, string) (string, string, error)
	BeginRevalidation(context.Context, int64, string) (string, string, error)
	CompleteInitialLogin(context.Context, string, string) (string, string, time.Time, error)
	CompleteRevalidation(context.Context, string, string, string) (string, string, time.Time, error)
	AuthenticateSession(context.Context, string) (auth.SessionAuthentication, error)
	RevokeSession(context.Context, string) (bool, error)
}

// NewAuthenticatedHandler activates the login, callback, authenticated shell,
// and local logout boundaries around the public router. Only the exact forum
// root, canonical one-segment GET area/topic routes, revalidation, and logout
// perform session lookup; infrastructure, malformed/noncanonical read paths,
// wrong methods, and unknown paths remain usable when the session store is
// unavailable.
//
// Complexity: construction is tight Theta(1) time and auxiliary space around
// fixed handler state. For path bytes p and delegated handler cost D, request
// dispatch is O(p+D) time and Omega(1); local auxiliary space is tight
// Theta(1), plus space owned by the delegated handler. Area/topic prefix and
// segment checks scan p, and canonical numeric topic parsing scans at most 19
// bytes. OIDC, PostgreSQL, cookie, CSRF, template, and transport costs retain
// their documented bounds. No operation is retried or detached.
func NewAuthenticatedHandler(
	builder URLBuilder,
	service AuthenticationService,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
	sessionCookieName string,
	secure bool,
) (http.Handler, error) {
	if service == nil {
		return nil, fmt.Errorf("browser authentication service is required")
	}
	publicHandler, err := NewHandler(builder, listAreas, loadAreaTopics, maximumTopicPage, loadTopicPosts, maximumPostPage)
	if err != nil {
		return nil, fmt.Errorf("construct public browser routes: %w", err)
	}
	loginHandler, err := newLoginStartHandler(
		service.BeginInitialLogin, initialLoginStateCookieSuffix, sessionCookieName, builder, secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct initial login route: %w", err)
	}
	revalidationHandler, err := newLoginStartHandler(
		func(ctx context.Context, returnPath string) (string, string, error) {
			authentication := sessionAuthenticationFromContext(ctx)
			if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
				return "", "", fmt.Errorf("authenticated session is required for revalidation")
			}
			return service.BeginRevalidation(ctx, authentication.SessionID, returnPath)
		},
		revalidationStateCookieSuffix,
		sessionCookieName,
		builder,
		secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct revalidation route: %w", err)
	}
	authorizedRevalidationHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			response.Header().Set("Cache-Control", "no-store")
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		revalidationHandler.ServeHTTP(response, request)
	})
	callbackHandler, err := newAuthenticationCallbackHandler(
		func(ctx context.Context, state, code string) (completedBrowserLogin, error) {
			token, returnPath, expiresAt, completionErr := service.CompleteInitialLogin(ctx, state, code)
			return completedBrowserLogin{token: token, returnPath: returnPath, expiresAt: expiresAt}, completionErr
		},
		func(ctx context.Context, state, code, oldToken string) (completedBrowserLogin, error) {
			token, returnPath, expiresAt, completionErr := service.CompleteRevalidation(ctx, state, code, oldToken)
			return completedBrowserLogin{token: token, returnPath: returnPath, expiresAt: expiresAt}, completionErr
		},
		sessionCookieName,
		builder,
		secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct initial login callback route: %w", err)
	}
	logoutHandler, err := newLogoutHandler(
		service.RevokeSession,
		func(request *http.Request) error { return validateCSRFRequest(request, maxLogoutFormBytes) },
		sessionCookieName,
		builder,
		secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct logout route: %w", err)
	}
	authenticatedPublicHandler, err := newSessionAuthenticationHandler(publicHandler, service.AuthenticateSession, sessionCookieName, builder, secure)
	if err != nil {
		return nil, fmt.Errorf("construct browser session boundary: %w", err)
	}
	authenticatedLogoutHandler, err := newSessionAuthenticationHandler(logoutHandler, service.AuthenticateSession, sessionCookieName, builder, secure)
	if err != nil {
		return nil, fmt.Errorf("construct logout session boundary: %w", err)
	}
	authenticatedRevalidationHandler, err := newSessionAuthenticationHandler(
		authorizedRevalidationHandler, service.AuthenticateSession, sessionCookieName, builder, secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct revalidation session boundary: %w", err)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/login":
			request.Pattern = request.Method + " /login"
			loginHandler.ServeHTTP(response, request)
		case "/auth/callback":
			request.Pattern = request.Method + " /auth/callback"
			callbackHandler.ServeHTTP(response, request)
		case "/auth/revalidate":
			request.Pattern = request.Method + " /auth/revalidate"
			authenticatedRevalidationHandler.ServeHTTP(response, request)
		case "/logout":
			request.Pattern = request.Method + " /logout"
			authenticatedLogoutHandler.ServeHTTP(response, request)
		case "/":
			authenticatedPublicHandler.ServeHTTP(response, request)
		default:
			if request.Method == http.MethodGet && request.URL.RawPath == "" {
				slug, areaPath := strings.CutPrefix(request.URL.Path, "/areas/")
				if areaPath && slug != "" && !strings.ContainsRune(slug, '/') {
					authenticatedPublicHandler.ServeHTTP(response, request)
					return
				}
				identifier, topicPath := strings.CutPrefix(request.URL.Path, "/topics/")
				if topicPath && identifier != "" && !strings.ContainsRune(identifier, '/') {
					if _, identifierErr := parseTopicID(identifier); identifierErr != nil {
						publicHandler.ServeHTTP(response, request)
						return
					}
					authenticatedPublicHandler.ServeHTTP(response, request)
					return
				}
			}
			publicHandler.ServeHTTP(response, request)
		}
	}), nil
}
