package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/moderation"
	"git.dannyhunn.com/agents/gotth-bb/internal/observability"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const maximumModerationFormBytes = 8192

type TopicLockChanger func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.TopicTransitionResult, error)
type TopicVisibilityChanger func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.TopicTransitionResult, error)

// newModerationHandler constructs the authenticated topic-state browser
// boundary. It verifies CSRF and bounded form syntax before deriving the
// server request UUID and delegating final authority to the serialized service.
//
// Complexity: construction is tight Theta(1). For bounded form bytes
// n <= 8,192, delegated work D, and response bytes r, request time is
// O(n+D+r), Omega(1), and auxiliary space is O(n+r), Omega(1), without a tight
// bound because database and transport work vary. One service call is made at
// most once; no request is retried or detached.
func newModerationHandler(builder URLBuilder, changeLock TopicLockChanger, changeVisibility TopicVisibilityChanger) (http.Handler, error) {
	if changeLock == nil || changeVisibility == nil {
		return nil, fmt.Errorf("topic moderation changers are required")
	}
	loginURL, err := builder.Path("login")
	if err != nil {
		return nil, fmt.Errorf("build moderation login URL: %w", err)
	}
	revalidationURL, err := builder.Path("auth", "revalidate")
	if err != nil {
		return nil, fmt.Errorf("build moderation revalidation URL: %w", err)
	}
	errorView, err := newPageView(builder, "Moderation")
	if err != nil {
		return nil, fmt.Errorf("construct moderation error view: %w", err)
	}
	errorView.CanonicalURL = ""
	serveError := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		view := errorView
		view.Title = heading
		if renderErr := renderResponse(
			response, request, status,
			errorPage(view, status, heading, message),
			errorContent(view, status, heading, message),
		); renderErr != nil {
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
	serve := func(change func(context.Context, auth.AccessContext, int64, bool, string, pgtype.UUID) (moderation.TopicTransitionResult, error), enabled bool, expectedState string) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Cache-Control", "no-store")
			access, redirect := authorized(request)
			if redirect != "" {
				serveSessionRedirect(response, request, redirect)
				return
			}
			topicID, parseErr := parseTopicID(chi.URLParam(request, "topicID"))
			if request.URL.RawPath != "" || request.URL.RawQuery != "" || parseErr != nil {
				serveError(response, request, http.StatusNotFound, "Page not found", "The requested topic does not exist.")
				return
			}
			if csrfErr := validateCSRFRequest(request, maximumModerationFormBytes); csrfErr != nil {
				serveError(response, request, http.StatusForbidden, "Request verification failed", "Reload the topic and try again.")
				return
			}
			reason, formErr := parseModerationForm(request)
			if formErr != nil {
				serveError(response, request, http.StatusBadRequest, "Invalid moderation form", "Reload the topic and submit the form again.")
				return
			}
			requestID, requestIDErr := moderationRequestUUID(request.Context())
			if requestIDErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
				return
			}
			result, changeErr := change(request.Context(), access, topicID, enabled, reason, requestID)
			if changeErr != nil {
				switch {
				case errors.Is(changeErr, moderation.ErrTopicModerationInput):
					serveError(response, request, http.StatusUnprocessableEntity, "Invalid moderation reason", "Enter a single-line reason without surrounding whitespace.")
				case errors.Is(changeErr, moderation.ErrTopicModerationDenied):
					serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot perform this action.")
				case errors.Is(changeErr, moderation.ErrTopicModerationConflict):
					serveError(response, request, http.StatusConflict, "Topic state changed", "Reload the topic before trying another moderation action.")
				case errors.Is(changeErr, pgx.ErrNoRows):
					serveError(response, request, http.StatusNotFound, "Page not found", "The requested topic does not exist.")
				default:
					serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
				}
				return
			}
			if result.TopicID != topicID || string(result.State) != expectedState || result.AuditID <= 0 {
				serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
				return
			}
			location, buildErr := builder.Path("topics", strconv.FormatInt(topicID, 10))
			if buildErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
				return
			}
			serveMutationNavigation(response, request, location)
		}
	}

	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Post("/topics/{topicID}/lock", serve(changeLock, true, "locked"))
	router.Post("/topics/{topicID}/unlock", serve(changeLock, false, "open"))
	router.Post("/topics/{topicID}/hide", serve(changeVisibility, true, "hidden"))
	router.Post("/topics/{topicID}/restore", serve(changeVisibility, false, "open"))
	return recordRoutePattern(router), nil
}

// parseModerationForm reads one already-CSRF-bounded reason and rejects every
// missing, duplicate, or unknown field.
//
// Complexity: for n body bytes and f fields, time and auxiliary space are
// O(n+f), Omega(n), delegated to net/http form parsing.
func parseModerationForm(request *http.Request) (string, error) {
	if err := request.ParseForm(); err != nil {
		return "", fmt.Errorf("parse moderation form: %w", err)
	}
	allowed := map[string]bool{"_csrf": true, "reason": true}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return "", fmt.Errorf("moderation form field is missing, duplicated, or unknown")
		}
	}
	if len(request.PostForm["reason"]) != 1 {
		return "", fmt.Errorf("moderation form field is missing, duplicated, or unknown")
	}
	return request.PostForm.Get("reason"), nil
}

// moderationRequestUUID converts the fixed lowercase request identifier from
// the server middleware into the exact 128-bit database UUID value.
//
// Complexity: context lookup is O(d) for context depth d; validation and decode
// are tight Theta(1) over exactly 32 hexadecimal bytes, with tight Theta(1)
// auxiliary space.
func moderationRequestUUID(ctx context.Context) (pgtype.UUID, error) {
	if ctx == nil {
		return pgtype.UUID{}, fmt.Errorf("moderation request context is required")
	}
	requestID, ok := observability.RequestID(ctx)
	if !ok {
		return pgtype.UUID{}, fmt.Errorf("moderation request ID is unavailable")
	}
	return decodeModerationRequestID(requestID)
}

// decodeModerationRequestID admits only the request middleware's canonical
// lowercase hexadecimal format before decoding its exact 128 bits.
//
// Complexity: time and auxiliary space are tight Theta(1) over the fixed
// 32-byte input and 16-byte output.
func decodeModerationRequestID(requestID string) (pgtype.UUID, error) {
	if len(requestID) != 32 {
		return pgtype.UUID{}, fmt.Errorf("moderation request ID is unavailable")
	}
	for index := 0; index < len(requestID); index++ {
		character := requestID[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return pgtype.UUID{}, fmt.Errorf("moderation request ID is invalid")
		}
	}
	var decoded [16]byte
	for index := range decoded {
		high, low := requestID[index*2], requestID[index*2+1]
		if high >= 'a' {
			high = high - 'a' + 10
		} else {
			high -= '0'
		}
		if low >= 'a' {
			low = low - 'a' + 10
		} else {
			low -= '0'
		}
		decoded[index] = high<<4 | low
	}
	return pgtype.UUID{Bytes: decoded, Valid: true}, nil
}
