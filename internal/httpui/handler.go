package httpui

import (
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewHandler constructs the internal HTTP routing shell from one validated
// browser URL authority. Caddy removes the external base prefix before requests
// reach these routes; every rendered browser URL adds it back through builder.
//
// Complexity: construction uses tight Theta(1) time and auxiliary space for a
// fixed route/view table. Chi owns request matching; template/static/transport
// costs are delegated to their documented boundaries.
func NewHandler(builder URLBuilder) (http.Handler, error) {
	rootView, err := newPageView(builder, "Discussion areas")
	if err != nil {
		return nil, fmt.Errorf("construct public shell view: %w", err)
	}
	notFoundView := rootView
	notFoundView.Title = "Page not found"
	notFoundView.CanonicalURL = ""

	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/", func(response http.ResponseWriter, request *http.Request) {
		if err := renderResponse(response, request, http.StatusOK, areaIndexPage(rootView), areaIndexContent(rootView)); err != nil {
			panic(err)
		}
	})
	router.Get("/health/live", serveLiveness)
	router.Get("/health/ready", serveNotReady)
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

// serveNotReady fails closed until the database-backed readiness contract is
// implemented; an incomplete alpha foundation must not advertise readiness.
//
// Complexity: this writes a fixed ten-byte body in Θ(1) time and Θ(1)
// auxiliary space; http.Error delegates fixed-size ResponseWriter I/O.
func serveNotReady(response http.ResponseWriter, _ *http.Request) {
	http.Error(response, "not ready", http.StatusServiceUnavailable)
}
