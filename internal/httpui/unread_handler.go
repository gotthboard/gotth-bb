package httpui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/jackc/pgx/v5"
)

const (
	maximumMarkReadFormBytes = 4096
	firstUnreadTimeout       = 5 * time.Second
	markReadTimeout          = 2 * time.Second
)

type FirstUnreadLoader func(context.Context, auth.AccessContext, int64) (forum.FirstUnreadTarget, error)
type TopicReadMarker func(context.Context, auth.AccessContext, int64) error

type UnreadHTTPServices struct {
	FirstUnread FirstUnreadLoader
	MarkRead    TopicReadMarker
}

type unreadTopicIDContextKey struct{}
type unreadControlsContextKey struct{}

func unreadControlsEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(unreadControlsContextKey{}).(bool)
	return enabled
}

// newUnreadPreflightHandler closes the exact path/query grammar and installs
// the route deadline before any session, body, CSRF, or database work.
func newUnreadPreflightHandler(builder URLBuilder, next http.Handler) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("unread downstream handler is required")
	}
	view, err := newPageView(builder, "Unread state unavailable")
	if err != nil {
		return nil, fmt.Errorf("construct unread preflight view: %w", err)
	}
	view.CanonicalURL = ""
	serveFixed := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		response.Header().Set("Cache-Control", "private, no-store")
		if renderErr := renderResponse(response, request, status,
			errorPage(view, status, heading, message), errorContent(view, status, heading, message)); renderErr != nil {
			panic(renderErr)
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		identifierAndSuffix, prefixFound := strings.CutPrefix(request.URL.Path, "/topics/")
		identifier := ""
		suffixFound := false
		if request.Method == http.MethodGet {
			identifier, suffixFound = strings.CutSuffix(identifierAndSuffix, "/unread")
		} else if request.Method == http.MethodPost {
			identifier, suffixFound = strings.CutSuffix(identifierAndSuffix, "/read")
		}
		topicID, parseErr := parseTopicID(identifier)
		if !prefixFound || !suffixFound || strings.ContainsRune(identifier, '/') || request.URL.RawPath != "" || parseErr != nil {
			serveFixed(response, request, http.StatusNotFound, "Page not found", "The requested topic does not exist or is not visible to you.")
			return
		}
		if request.URL.RawQuery != "" || request.URL.ForceQuery {
			serveFixed(response, request, http.StatusBadRequest, "Invalid unread request", "The unread-state request is invalid.")
			return
		}
		timeout := firstUnreadTimeout
		if request.Method == http.MethodPost {
			timeout = markReadTimeout
		}
		ctx, cancel := context.WithTimeout(context.WithValue(request.Context(), unreadTopicIDContextKey{}, topicID), timeout)
		defer cancel()
		downstream := request.WithContext(ctx)
		defer func() { request.Pattern = downstream.Pattern }()
		next.ServeHTTP(response, downstream)
	}), nil
}

// newUnreadHandler implements the authenticated first-unread navigation and
// sole explicit mark-read HTTP mutation. The preflight handler must wrap it.
func newUnreadHandler(builder URLBuilder, services UnreadHTTPServices) (http.Handler, error) {
	if services.FirstUnread == nil || services.MarkRead == nil {
		return nil, fmt.Errorf("browser unread services are required")
	}
	view, err := newPageView(builder, "Unread state")
	if err != nil {
		return nil, fmt.Errorf("construct unread view: %w", err)
	}
	view.CanonicalURL = ""
	serveFixed := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		response.Header().Set("Cache-Control", "private, no-store")
		if renderErr := renderResponse(response, request, status,
			errorPage(view, status, heading, message), errorContent(view, status, heading, message)); renderErr != nil {
			panic(renderErr)
		}
	}
	authorized := func(response http.ResponseWriter, request *http.Request, returnPath string) (auth.AccessContext, bool) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			login, buildErr := builder.PathWithQuery([]string{"login"}, url.Values{"return": {returnPath}})
			if buildErr != nil {
				serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
				return auth.AccessContext{}, false
			}
			serveSessionRedirect(response, request, login)
			return auth.AccessContext{}, false
		}
		if authentication.RequiresRevalidation {
			revalidation, buildErr := builder.PathWithQuery([]string{"auth", "revalidate"}, url.Values{"return": {returnPath}})
			if buildErr != nil {
				serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
				return auth.AccessContext{}, false
			}
			serveSessionRedirect(response, request, revalidation)
			return auth.AccessContext{}, false
		}
		return authentication.Access, true
	}

	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/topics/{topicID}/unread", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "private, no-store")
		topicID, ok := request.Context().Value(unreadTopicIDContextKey{}).(int64)
		if !ok || topicID <= 0 {
			serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
			return
		}
		identifier := strconv.FormatInt(topicID, 10)
		unreadPath, buildErr := builder.Path("topics", identifier, "unread")
		if buildErr != nil {
			serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
			return
		}
		access, current := authorized(response, request, unreadPath)
		if !current {
			return
		}
		target, loadErr := services.FirstUnread(request.Context(), access, topicID)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveFixed(response, request, http.StatusNotFound, "Page not found", "The requested topic does not exist or is not visible to you.")
				return
			}
			serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
			return
		}
		location, buildErr := firstUnreadLocation(builder, topicID, target)
		if buildErr != nil {
			serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
			return
		}
		serveMutationNavigation(response, request, location)
	})
	router.Post("/topics/{topicID}/read", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "private, no-store")
		topicID, ok := request.Context().Value(unreadTopicIDContextKey{}).(int64)
		if !ok || topicID <= 0 {
			serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
			return
		}
		location, buildErr := builder.Path("topics", strconv.FormatInt(topicID, 10))
		if buildErr != nil {
			serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
			return
		}
		access, current := authorized(response, request, location)
		if !current {
			return
		}
		if csrfErr := validateCSRFRequest(request, maximumMarkReadFormBytes); csrfErr != nil {
			serveFixed(response, request, http.StatusForbidden, "Request verification failed", "Reload the topic and try again.")
			return
		}
		if bodyErr := validateMarkReadBody(request); bodyErr != nil {
			serveFixed(response, request, http.StatusBadRequest, "Invalid mark-read form", "Reload the topic and try again.")
			return
		}
		if markErr := services.MarkRead(request.Context(), access, topicID); markErr != nil {
			if errors.Is(markErr, pgx.ErrNoRows) {
				serveFixed(response, request, http.StatusNotFound, "Page not found", "The requested topic does not exist or is not visible to you.")
				return
			}
			serveFixed(response, request, http.StatusServiceUnavailable, "Unread state unavailable", "Unread state is temporarily unavailable.")
			return
		}
		serveMutationNavigation(response, request, location)
	})
	return recordRoutePattern(router), nil
}

func validateMarkReadBody(request *http.Request) error {
	if len(request.Header.Values(csrfHeaderName)) == 1 {
		if request.ContentLength > 0 {
			return fmt.Errorf("mark-read header request body is not empty")
		}
		if request.Body == nil {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maximumMarkReadFormBytes+1))
		if err != nil || len(body) != 0 {
			return fmt.Errorf("mark-read header request body is not empty")
		}
		return nil
	}
	if err := request.ParseForm(); err != nil || len(request.PostForm) != 1 || len(request.PostForm[csrfFormFieldName]) != 1 {
		return fmt.Errorf("mark-read form is malformed")
	}
	return nil
}

func firstUnreadLocation(builder URLBuilder, topicID int64, target forum.FirstUnreadTarget) (string, error) {
	identifier := strconv.FormatInt(topicID, 10)
	if target.PostID == 0 {
		if target.Page != 0 || target.Direct {
			return "", fmt.Errorf("empty first-unread target is malformed")
		}
		return builder.Path("topics", identifier)
	}
	if target.Direct {
		if target.Page != 0 {
			return "", fmt.Errorf("direct first-unread target is malformed")
		}
		return builder.Path("posts", strconv.FormatInt(target.PostID, 10))
	}
	if target.Page < 1 || target.Page > 10_000 {
		return "", fmt.Errorf("paged first-unread target is malformed")
	}
	query := url.Values(nil)
	if target.Page > 1 {
		query = url.Values{"page": {strconv.FormatInt(int64(target.Page), 10)}}
	}
	return builder.PathWithQueryAndFragment([]string{"topics", identifier}, query, "post-"+strconv.FormatInt(target.PostID, 10))
}
