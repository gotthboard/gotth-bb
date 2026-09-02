package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/config"
	"git.dannyhunn.com/agents/gotth-bb/internal/governance"
	"github.com/jackc/pgx/v5/pgtype"
)

const maximumAdministratorSetupFormBytes = 4096

const (
	administratorSetupRequestPath = "/setup"
	administratorClaimRequestPath = "/setup/administrator"
)

type InitialAdministratorSetupLoader func(context.Context, auth.SessionAuthentication) (governance.InitialAdministratorSetupStatus, error)
type InitialAdministratorClaimer func(context.Context, auth.SessionAuthentication, pgtype.UUID) (governance.InitialAdministratorClaimResult, error)

func newRegistrationRedirectHandler(builder URLBuilder, registrationURL url.URL) (http.Handler, error) {
	loginURL, err := builder.Absolute("login")
	if err != nil {
		return nil, fmt.Errorf("build registration return URL: %w", err)
	}
	if registrationURL.Scheme == "" || registrationURL.Host == "" || registrationURL.Path == "" || registrationURL.User != nil || registrationURL.RawQuery != "" || registrationURL.Fragment != "" {
		return nil, fmt.Errorf("registration URL is invalid")
	}
	target := registrationURL
	target.RawQuery = url.Values{"next": {loginURL}}.Encode()
	location := target.String()
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Pragma", "no-cache")
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Location", location)
		response.WriteHeader(http.StatusSeeOther)
	}), nil
}

func newAdministratorSetupHandler(
	builder URLBuilder,
	load InitialAdministratorSetupLoader,
	claim InitialAdministratorClaimer,
	sessionCookieName string,
	secure bool,
) (http.Handler, error) {
	if load == nil || claim == nil {
		return nil, fmt.Errorf("administrator setup services are required")
	}
	validatedCookieName, err := config.ParseSessionCookieName(sessionCookieName)
	if err != nil || validatedCookieName != sessionCookieName {
		return nil, fmt.Errorf("administrator setup session cookie name is invalid")
	}
	setupURL, err := builder.Path("setup")
	if err != nil {
		return nil, fmt.Errorf("build administrator setup URL: %w", err)
	}
	claimURL, err := builder.Path("setup", "administrator")
	if err != nil {
		return nil, fmt.Errorf("build administrator claim URL: %w", err)
	}
	loginURL, err := builder.PathWithQuery([]string{"login"}, url.Values{"return": {setupURL}})
	if err != nil {
		return nil, fmt.Errorf("build administrator setup login URL: %w", err)
	}
	revalidationURL, err := builder.PathWithQuery([]string{"auth", "revalidate"}, url.Values{"return": {setupURL}})
	if err != nil {
		return nil, fmt.Errorf("build administrator setup revalidation URL: %w", err)
	}
	homeURL, err := builder.Path()
	if err != nil {
		return nil, fmt.Errorf("build administrator setup home URL: %w", err)
	}
	freshLoginURL, err := builder.PathWithQuery([]string{"login"}, url.Values{"return": {homeURL}})
	if err != nil {
		return nil, fmt.Errorf("build post-setup login URL: %w", err)
	}
	view, err := newPageView(builder, "First administrator setup", "setup")
	if err != nil {
		return nil, fmt.Errorf("construct administrator setup view: %w", err)
	}
	expiredCookie := http.Cookie{
		Name: sessionCookieName, Path: homeURL, Expires: time.Unix(1, 0).UTC(), MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}
	if err := expiredCookie.Valid(); err != nil {
		return nil, fmt.Errorf("administrator setup expired cookie is invalid: %w", err)
	}
	serveError := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		errorView := view
		errorView.Title = heading
		errorView.CanonicalURL = ""
		if renderErr := renderResponse(response, request, status, errorPage(errorView, status, heading, message), errorContent(errorView, status, heading, message)); renderErr != nil {
			panic(renderErr)
		}
	}
	redirect := func(response http.ResponseWriter, location string) {
		response.Header().Set("Location", location)
		response.WriteHeader(http.StatusSeeOther)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Pragma", "no-cache")
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			serveError(response, request, http.StatusNotFound, "Page not found", "The requested setup page does not exist.")
			return
		}
		authentication := sessionAuthenticationFromContext(request.Context())
		switch request.URL.Path {
		case administratorSetupRequestPath:
			if request.Method != http.MethodGet {
				response.Header().Set("Allow", http.MethodGet)
				serveError(response, request, http.StatusMethodNotAllowed, "Method not allowed", "Use the setup confirmation page.")
				return
			}
			status, loadErr := load(request.Context(), authentication)
			if loadErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Setup unavailable", "Administrator setup is temporarily unavailable.")
				return
			}
			if !status.Open {
				serveError(response, request, http.StatusNotFound, "Page not found", "Administrator setup is closed.")
				return
			}
			if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
				redirect(response, loginURL)
				return
			}
			if authentication.RequiresRevalidation {
				redirect(response, revalidationURL)
				return
			}
			if !status.Eligible {
				serveError(response, request, http.StatusForbidden, "Setup denied", "This identity is not authorized to claim first administration.")
				return
			}
			setup := administratorSetupView{ActionURL: claimURL, CSRFToken: csrfTokenFromContext(request.Context())}
			if renderErr := renderResponse(response, request, http.StatusOK, administratorSetupPage(view, setup), administratorSetupContent(view, setup)); renderErr != nil {
				panic(renderErr)
			}
		case administratorClaimRequestPath:
			if request.Method != http.MethodPost {
				response.Header().Set("Allow", http.MethodPost)
				serveError(response, request, http.StatusMethodNotAllowed, "Method not allowed", "Submit the administrator setup form.")
				return
			}
			if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
				redirect(response, loginURL)
				return
			}
			if authentication.RequiresRevalidation {
				redirect(response, revalidationURL)
				return
			}
			if csrfErr := validateCSRFRequest(request, maximumAdministratorSetupFormBytes); csrfErr != nil {
				serveError(response, request, http.StatusForbidden, "Request verification failed", "Reload the setup page and try again.")
				return
			}
			if parseErr := request.ParseForm(); parseErr != nil || len(request.PostForm) != 1 || len(request.PostForm[csrfFormFieldName]) != 1 {
				serveError(response, request, http.StatusBadRequest, "Invalid setup form", "Reload the setup page and submit the form again.")
				return
			}
			requestID, requestIDErr := moderationRequestUUID(request.Context())
			if requestIDErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Setup unavailable", "Administrator setup is temporarily unavailable.")
				return
			}
			result, claimErr := claim(request.Context(), authentication, requestID)
			if claimErr != nil {
				switch {
				case errors.Is(claimErr, governance.ErrAdministratorSetupClosed):
					serveError(response, request, http.StatusConflict, "Setup closed", "First-administrator setup has already completed.")
				case errors.Is(claimErr, governance.ErrAdministratorSetupDenied):
					serveError(response, request, http.StatusForbidden, "Setup denied", "This identity is not authorized to claim first administration.")
				default:
					serveError(response, request, http.StatusServiceUnavailable, "Setup unavailable", "Administrator setup is temporarily unavailable.")
				}
				return
			}
			if result.UserID != authentication.Access.UserID || result.AuditID <= 0 || result.RevokedSessionID != authentication.SessionID {
				serveError(response, request, http.StatusServiceUnavailable, "Setup unavailable", "Administrator setup is temporarily unavailable.")
				return
			}
			http.SetCookie(response, &expiredCookie)
			redirect(response, freshLoginURL)
		default:
			serveError(response, request, http.StatusNotFound, "Page not found", "The requested setup page does not exist.")
		}
	}), nil
}
