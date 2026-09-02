package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/moderation"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ModerationUserStatusLoader func(context.Context, auth.AccessContext, int64) (store.ModerationUserStatus, error)
type UserSuspensionChanger func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.UserSuspensionResult, error)

// newUserModerationHandler constructs the protected local account-status and
// suspend/reinstate browser boundary. Reads expose the already privacy-bounded
// store projection. Mutations validate CSRF/form/request authority before one
// service call and then redirect through the builder-owned status URL.
//
// Complexity: construction is tight Theta(1). For bounded path/form bytes
// n <= 8,192, delegated work D, and response bytes r, request time is
// O(n+D+r), Omega(1), and auxiliary space is O(n+r), Omega(1), without one
// tight bound because PostgreSQL and transport work vary. A request invokes at
// most one loader or changer and is never retried or detached.
func newUserModerationHandler(builder URLBuilder, load ModerationUserStatusLoader, change UserSuspensionChanger) (http.Handler, error) {
	if load == nil || change == nil {
		return nil, fmt.Errorf("user moderation services are required")
	}
	loginURL, err := builder.Path("login")
	if err != nil {
		return nil, fmt.Errorf("build user moderation login URL: %w", err)
	}
	revalidationURL, err := builder.Path("auth", "revalidate")
	if err != nil {
		return nil, fmt.Errorf("build user moderation revalidation URL: %w", err)
	}
	errorView, err := newPageView(builder, "Account moderation")
	if err != nil {
		return nil, fmt.Errorf("construct user moderation error view: %w", err)
	}
	errorView.CanonicalURL = ""
	serveError := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		view := errorView
		view.Title = heading
		if renderErr := renderResponse(response, request, status,
			errorPage(view, status, heading, message), errorContent(view, status, heading, message)); renderErr != nil {
			panic(renderErr)
		}
	}
	authorized := func(request *http.Request) (auth.AccessContext, string) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			return auth.AccessContext{}, loginURL
		}
		if authentication.RequiresRevalidation {
			return auth.AccessContext{}, revalidationURL
		}
		return authentication.Access, ""
	}
	parseTarget := func(request *http.Request) (int64, bool) {
		userID, parseErr := parseUserID(chi.URLParam(request, "userID"))
		return userID, request.URL.RawPath == "" && request.URL.RawQuery == "" && parseErr == nil
	}
	statusURL := func(userID int64) (string, error) {
		return builder.Path("moderation", "users", strconv.FormatInt(userID, 10))
	}
	get := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request)
		if redirect != "" {
			servePublishingRedirect(response, request, redirect)
			return
		}
		userID, validTarget := parseTarget(request)
		if !validTarget {
			serveError(response, request, http.StatusNotFound, "Page not found", "The requested account does not exist or is not available to you.")
			return
		}
		status, loadErr := load(request.Context(), access, userID)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveError(response, request, http.StatusNotFound, "Page not found", "The requested account does not exist or is not available to you.")
				return
			}
			serveError(response, request, http.StatusServiceUnavailable, "Account status unavailable", "Account status is temporarily unavailable.")
			return
		}
		roleLabel, validStatus := validModerationUserStatus(status, userID)
		if !validStatus {
			serveError(response, request, http.StatusServiceUnavailable, "Account status unavailable", "Account status is temporarily unavailable.")
			return
		}
		token := csrfTokenFromContext(request.Context())
		if len(token) != sessionCookieEncodedBytes {
			serveError(response, request, http.StatusServiceUnavailable, "Account status unavailable", "Account status is temporarily unavailable.")
			return
		}
		identifier := strconv.FormatInt(userID, 10)
		view, viewErr := newPageView(builder, status.DisplayName, "moderation", "users", identifier)
		action := "suspend"
		submitLabel := "Suspend account"
		statusLabel := "Active"
		if status.Suspended {
			action = "reinstate"
			submitLabel = "Reinstate account"
			statusLabel = "Suspended"
		}
		actionURL, actionErr := builder.Path("moderation", "users", identifier, action)
		if viewErr != nil || actionErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, "Account status unavailable", "Account status is temporarily unavailable.")
			return
		}
		presentation := moderationUserView{
			DisplayName: status.DisplayName, RoleLabel: roleLabel, StatusLabel: statusLabel,
			CreatedAt: formatModerationTime(status.CreatedAt.Time), LastLoginAt: formatModerationTime(status.LastLoginAt.Time),
			MutedUntil: formatOptionalModerationTime(status.MutedUntil), ActionURL: actionURL,
			CSRFToken: token, SubmitLabel: submitLabel,
		}
		if status.Suspended {
			presentation.SuspendedAt = formatOptionalModerationTime(status.SuspendedAt)
			presentation.SuspendedUntil = formatOptionalModerationTime(status.SuspendedUntil)
			presentation.SuspensionReason = status.SuspensionReason.String
		}
		if renderErr := renderResponse(response, request, http.StatusOK,
			moderationUserPage(view, presentation), moderationUserContent(view, presentation)); renderErr != nil {
			panic(renderErr)
		}
	})
	mutate := func(suspend bool) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Cache-Control", "no-store")
			access, redirect := authorized(request)
			if redirect != "" {
				servePublishingRedirect(response, request, redirect)
				return
			}
			userID, validTarget := parseTarget(request)
			if !validTarget {
				serveError(response, request, http.StatusNotFound, "Page not found", "The requested account does not exist or is not available to you.")
				return
			}
			if csrfErr := validateCSRFRequest(request, maximumModerationFormBytes); csrfErr != nil {
				serveError(response, request, http.StatusForbidden, "Request verification failed", "Reload the account status page and try again.")
				return
			}
			reason, formErr := parseModerationForm(request)
			if formErr != nil {
				serveError(response, request, http.StatusBadRequest, "Invalid moderation form", "Reload the account status page and submit the form again.")
				return
			}
			requestID, requestIDErr := moderationRequestUUID(request.Context())
			if requestIDErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Account moderation unavailable", "Account moderation is temporarily unavailable.")
				return
			}
			result, changeErr := change(request.Context(), access, userID, suspend, reason, requestID)
			if changeErr != nil {
				switch {
				case errors.Is(changeErr, moderation.ErrUserModerationInput):
					serveError(response, request, http.StatusUnprocessableEntity, "Invalid moderation reason", "Enter a single-line reason without surrounding whitespace.")
				case errors.Is(changeErr, moderation.ErrUserModerationDenied):
					serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot perform this action.")
				case errors.Is(changeErr, moderation.ErrUserModerationConflict):
					serveError(response, request, http.StatusConflict, "Account state changed", "Reload the account status page before trying another moderation action.")
				case errors.Is(changeErr, moderation.ErrAdministratorContinuity):
					serveError(response, request, http.StatusConflict, "Administrator required", "The final active administrator cannot be suspended.")
				case errors.Is(changeErr, pgx.ErrNoRows):
					serveError(response, request, http.StatusNotFound, "Page not found", "The requested account does not exist or is not available to you.")
				default:
					serveError(response, request, http.StatusServiceUnavailable, "Account moderation unavailable", "Account moderation is temporarily unavailable.")
				}
				return
			}
			if result.UserID != userID || result.Suspended != suspend || result.AuditID <= 0 {
				serveError(response, request, http.StatusServiceUnavailable, "Account moderation unavailable", "Account moderation is temporarily unavailable.")
				return
			}
			location, buildErr := statusURL(userID)
			if buildErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Account moderation unavailable", "Account moderation is temporarily unavailable.")
				return
			}
			servePublishingRedirect(response, request, location)
		}
	}
	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/moderation/users/{userID}", get)
	router.Post("/moderation/users/{userID}/suspend", mutate(true))
	router.Post("/moderation/users/{userID}/reinstate", mutate(false))
	return recordRoutePattern(router), nil
}

// moderationRoleLabel maps the closed local role to presentation prose.
//
// Complexity: time and auxiliary space are tight Theta(1).
func moderationRoleLabel(role policy.Role) (string, bool) {
	switch role {
	case policy.RoleMember:
		return "Member", true
	case policy.RoleModerator:
		return "Moderator", true
	case policy.RoleAdministrator:
		return "Administrator", true
	default:
		return "", false
	}
}

// validModerationViewTime rejects null, infinite, and zero presentation times.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validModerationViewTime(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite && !value.Time.IsZero()
}

// validModerationUserStatus closes every persisted-state invariant consumed by
// the account-status template. A false effective Suspended value may retain a
// complete future or expired suspension record; only a malformed record is
// rejected here.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validModerationUserStatus(status store.ModerationUserStatus, expectedUserID int64) (string, bool) {
	roleLabel, validRole := moderationRoleLabel(status.Role)
	if status.UserID != expectedUserID || status.DisplayName == "" || !validRole ||
		!validModerationViewTime(status.CreatedAt) || !validModerationViewTime(status.UpdatedAt) ||
		!validModerationViewTime(status.LastLoginAt) || status.UpdatedAt.Time.Before(status.CreatedAt.Time) ||
		status.LastLoginAt.Time.Before(status.CreatedAt.Time) ||
		status.MutedUntil.Valid && (!validModerationViewTime(status.MutedUntil) || !status.MutedUntil.Time.After(status.CreatedAt.Time)) {
		return "", false
	}
	if !status.SuspendedAt.Valid {
		if status.Suspended || status.SuspendedUntil.Valid || status.SuspensionReason.Valid {
			return "", false
		}
		return roleLabel, true
	}
	if !validModerationViewTime(status.SuspendedAt) || status.SuspendedAt.Time.Before(status.CreatedAt.Time) ||
		!status.SuspensionReason.Valid || status.SuspensionReason.String == "" ||
		status.SuspendedUntil.Valid && (!validModerationViewTime(status.SuspendedUntil) || !status.SuspendedUntil.Time.After(status.SuspendedAt.Time)) {
		return "", false
	}
	return roleLabel, true
}

// formatModerationTime emits one stable UTC account-status timestamp.
//
// Complexity: time and auxiliary space are tight Theta(1) for time.Time's
// bounded textual representation.
func formatModerationTime(value time.Time) string {
	return value.UTC().Format("Jan 2, 2006 15:04 MST")
}

// formatOptionalModerationTime preserves SQL null as absent presentation.
//
// Complexity: time and auxiliary space are tight Theta(1).
func formatOptionalModerationTime(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return formatModerationTime(value.Time)
}
