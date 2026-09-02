package httpui

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ReadinessChecker proves the running process may safely receive traffic.
// Error details remain inside the service boundary; the public health response
// is deliberately fixed.
type ReadinessChecker func(context.Context) error

// NewHandler constructs the fail-closed internal HTTP routing shell from one
// validated browser URL authority. A configured edge proxy passes internal
// paths to these routes; every rendered browser URL uses builder.
//
// Complexity: construction uses tight Theta(1) time and auxiliary space for a
// fixed route/view table. Chi owns request matching; visible-area/topic,
// template/static, and transport costs are delegated to their documented
// boundaries. The root, area-topic, and topic-post handlers receive the same
// validated URL authority and caller-supplied access-aware stores.
func NewHandler(
	builder URLBuilder,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
) (http.Handler, error) {
	return newHandler(
		builder, listAreas, loadAreaTopics, maximumTopicPage, loadTopicPosts,
		maximumPostPage, unavailableReadiness,
	)
}

func newHandler(
	builder URLBuilder,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
	checkReadiness ReadinessChecker,
) (http.Handler, error) {
	if checkReadiness == nil {
		return nil, fmt.Errorf("readiness checker is required")
	}
	rootView, err := newPageView(builder, "Discussion areas")
	if err != nil {
		return nil, fmt.Errorf("construct public shell view: %w", err)
	}
	rootHandler, err := newAreaIndexHandler(builder, rootView, listAreas)
	if err != nil {
		return nil, fmt.Errorf("construct area index route: %w", err)
	}
	areaTopicHandler, err := newAreaTopicListHandler(builder, maximumTopicPage, loadAreaTopics)
	if err != nil {
		return nil, fmt.Errorf("construct area topic route: %w", err)
	}
	topicPostHandler, err := newTopicPostListHandler(builder, maximumPostPage, loadTopicPosts)
	if err != nil {
		return nil, fmt.Errorf("construct topic post route: %w", err)
	}
	notFoundView := rootView
	notFoundView.Title = "Page not found"
	notFoundView.CanonicalURL = ""

	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/", rootHandler.ServeHTTP)
	router.Get("/areas/{slug}", areaTopicHandler.ServeHTTP)
	router.Get("/topics/{topicID}", topicPostHandler.ServeHTTP)
	router.Get("/health/live", serveLiveness)
	router.Get("/health/ready", readinessHandler(checkReadiness))
	stylesheet := staticAssetHandler("text/css; charset=utf-8", appStylesheet)
	htmx := staticAssetHandler("text/javascript; charset=utf-8", htmxScript)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		router.Method(method, "/static/app-1.0.0-alpha.1.css", stylesheet)
		router.Method(method, "/static/htmx-2.0.10.min.js", htmx)
	}
	router.NotFound(func(response http.ResponseWriter, request *http.Request) {
		if err := renderResponse(
			response,
			request,
			http.StatusNotFound,
			errorPage(notFoundView, http.StatusNotFound, "Page not found", "The requested page does not exist or is not visible to you."),
			errorContent(notFoundView, http.StatusNotFound, "Page not found", "The requested page does not exist or is not visible to you."),
		); err != nil {
			panic(err)
		}
	})
	return recordRoutePattern(router), nil
}

// serveLiveness reports only that the process can execute its HTTP loop.
//
// Complexity: this writes a fixed three-byte body in Θ(1) time and Θ(1)
// auxiliary space; ResponseWriter I/O may block according to the transport.
func serveLiveness(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, "ok\n")
}

func unavailableReadiness(context.Context) error {
	return fmt.Errorf("readiness is not configured")
}

// readinessHandler exposes one fixed response while delegating the bounded
// release, database, and governance proof to the configured checker.
//
// Complexity: local time and auxiliary space are tight Theta(1); total time is
// the delegated checker cost plus fixed-size ResponseWriter I/O.
func readinessHandler(check ReadinessChecker) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := check(request.Context()); err != nil {
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(response, "not ready\n")
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(response, "ok\n")
	}
}
