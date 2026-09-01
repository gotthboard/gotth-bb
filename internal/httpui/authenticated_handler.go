package httpui

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
)

const maxLogoutFormBytes = 4096

// AuthenticationService is the exact initial-login and local-session surface
// consumed by the browser router.
type AuthenticationService interface {
	BeginInitialLogin(context.Context, string) (string, string, error)
	CompleteInitialLogin(context.Context, string, string) (string, string, time.Time, error)
	AuthenticateSession(context.Context, string) (auth.SessionAuthentication, error)
	RevokeSession(context.Context, string) (bool, error)
}

// NewAuthenticatedHandler activates the login, callback, authenticated shell,
// and local logout boundaries around the public router. Only the exact forum
// root and logout route perform session lookup; infrastructure and unknown
// paths remain usable when the session store is unavailable.
//
// Complexity: construction is tight Theta(1) time and auxiliary space around
// fixed handler state. Each request performs a bounded path-prefix dispatch;
// delegated route, OIDC, PostgreSQL, cookie, CSRF, template, and transport costs
// retain their documented bounds. No operation is retried or detached.
func NewAuthenticatedHandler(builder URLBuilder, service AuthenticationService, sessionCookieName string, secure bool) (http.Handler, error) {
	if service == nil {
		return nil, fmt.Errorf("browser authentication service is required")
	}
	publicHandler, err := NewHandler(builder)
	if err != nil {
		return nil, fmt.Errorf("construct public browser routes: %w", err)
	}
	loginHandler, err := newLoginStartHandler(
		service.BeginInitialLogin, initialLoginStateCookieSuffix, sessionCookieName, builder, secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct initial login route: %w", err)
	}
	callbackHandler, err := newInitialLoginCallbackHandler(
		func(ctx context.Context, state, code string) (completedBrowserLogin, error) {
			token, returnPath, expiresAt, completionErr := service.CompleteInitialLogin(ctx, state, code)
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
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/login":
			request.Pattern = request.Method + " /login"
			loginHandler.ServeHTTP(response, request)
		case "/auth/callback":
			request.Pattern = request.Method + " /auth/callback"
			callbackHandler.ServeHTTP(response, request)
		case "/logout":
			request.Pattern = request.Method + " /logout"
			authenticatedLogoutHandler.ServeHTTP(response, request)
		case "/":
			authenticatedPublicHandler.ServeHTTP(response, request)
		default:
			publicHandler.ServeHTTP(response, request)
		}
	}), nil
}
