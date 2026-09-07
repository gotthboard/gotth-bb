package httpui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gotthboard/gotth-bb/internal/discovery"
)

const (
	searchRequestTimeout   = 5 * time.Second
	activityRequestTimeout = 2 * time.Second
	discoveryPermitCount   = 2
)

type searchRequestContextKey struct{}
type activityCursorContextKey struct{}
type directPostIDContextKey struct{}

type ActivityCursorVerifier func(string) (discovery.AuthenticatedCursor, error)

type activityCursorVerifier = ActivityCursorVerifier

type discoveryTimeouts struct {
	search   time.Duration
	activity time.Duration
}

// newDiscoveryPreflightHandler closes request grammar and cursor MACs before
// optional-session work, then holds one of two non-waiting permits through the
// bounded downstream response for search and activity.
//
// Complexity: construction is tight Theta(1). For q bounded raw-query/path
// bytes and downstream work D, request time is O(q+D), Omega(q), with no tight
// Theta bound because D includes session, PostgreSQL, rendering, and transport
// I/O; local auxiliary space is O(q), Omega(1). At most two downstream
// search/activity requests and their buffers exist per constructed handler.
func newDiscoveryPreflightHandler(builder URLBuilder, next http.Handler, verify activityCursorVerifier) (http.Handler, error) {
	return newDiscoveryPreflightHandlerWithTimeouts(builder, next, verify, discoveryTimeouts{search: searchRequestTimeout, activity: activityRequestTimeout})
}

func newDiscoveryPreflightHandlerWithTimeouts(builder URLBuilder, next http.Handler, verify activityCursorVerifier, timeouts discoveryTimeouts) (http.Handler, error) {
	if next == nil {
		return nil, fmt.Errorf("discovery downstream handler is required")
	}
	if verify == nil {
		return nil, fmt.Errorf("activity cursor verifier is required")
	}
	if timeouts.search <= 0 || timeouts.activity <= 0 {
		return nil, fmt.Errorf("discovery request timeouts are required")
	}
	view, err := newPageView(builder, "Discovery unavailable")
	if err != nil {
		return nil, fmt.Errorf("construct discovery preflight view: %w", err)
	}
	view.CanonicalURL = ""
	permits := make(chan struct{}, discoveryPermitCount)
	serveFixed := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		if renderErr := renderResponse(
			response, request, status,
			errorPage(view, status, heading, message),
			errorContent(view, status, heading, message),
		); renderErr != nil {
			panic(renderErr)
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ctx := request.Context()
		limited := false
		timeout := time.Duration(0)
		switch request.URL.Path {
		case "/search":
			request.Pattern = http.MethodGet + " /search"
			parsed, parseErr := discovery.ParseSearchRequest(request.URL.RawQuery)
			if parseErr != nil {
				serveFixed(response, request, http.StatusBadRequest, "Invalid search", "The search request is invalid.")
				return
			}
			ctx = context.WithValue(ctx, searchRequestContextKey{}, parsed)
			limited, timeout = true, timeouts.search
		case "/activity":
			request.Pattern = http.MethodGet + " /activity"
			encoded, parseErr := discovery.ParseActivityCursorParameter(request.URL.RawQuery)
			if parseErr != nil {
				serveFixed(response, request, http.StatusBadRequest, "Invalid activity request", "The activity request is invalid.")
				return
			}
			if encoded != "" {
				verified, verifyErr := verify(encoded)
				if verifyErr != nil {
					serveFixed(response, request, http.StatusBadRequest, "Invalid activity request", "The activity request is invalid.")
					return
				}
				ctx = context.WithValue(ctx, activityCursorContextKey{}, verified)
			}
			limited, timeout = true, timeouts.activity
		default:
			request.Pattern = http.MethodGet + " /posts/{postID}"
			identifier, found := strings.CutPrefix(request.URL.Path, "/posts/")
			postID, parseErr := parsePostID(identifier)
			if !found || strings.ContainsRune(identifier, '/') || parseErr != nil || request.URL.RawQuery != "" {
				serveFixed(response, request, http.StatusBadRequest, "Invalid post request", "The post request is invalid.")
				return
			}
			ctx = context.WithValue(ctx, directPostIDContextKey{}, postID)
		}
		if limited {
			select {
			case permits <- struct{}{}:
				defer func() { <-permits }()
			default:
				response.Header().Set("Retry-After", "1")
				serveFixed(response, request, http.StatusServiceUnavailable, "Discovery busy", "Search and recent activity are temporarily busy. Try again shortly.")
				return
			}
			deadlineContext, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			ctx = deadlineContext
		}
		downstreamRequest := request.WithContext(ctx)
		defer func() { request.Pattern = downstreamRequest.Pattern }()
		next.ServeHTTP(response, downstreamRequest)
	}), nil
}

func searchRequestFromContext(ctx context.Context) (discovery.SearchRequest, bool) {
	request, ok := ctx.Value(searchRequestContextKey{}).(discovery.SearchRequest)
	return request, ok
}

func discoveryCursorFromContext(ctx context.Context) (discovery.AuthenticatedCursor, bool) {
	cursor, ok := ctx.Value(activityCursorContextKey{}).(discovery.AuthenticatedCursor)
	return cursor, ok
}

func directPostIDFromContext(ctx context.Context) (int64, bool) {
	postID, ok := ctx.Value(directPostIDContextKey{}).(int64)
	return postID, ok
}
