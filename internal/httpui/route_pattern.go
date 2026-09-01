package httpui

import (
	"context"
	"net/http"
)

type routePatternSink struct {
	value string
}

type routePatternSinkContextKey struct{}

// captureRoutePattern copies Chi's matched pattern into a request-scoped sink
// after routing, including while a handler panic unwinds.
//
// Complexity: for d nested route patterns containing p bytes, delegated Chi
// assembly is O(d+p) time and O(p) auxiliary space; the local assignment and
// defer are tight Theta(1).
func captureRoutePattern(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if sink, ok := request.Context().Value(routePatternSinkContextKey{}).(*routePatternSink); ok {
				if request.Pattern != "" {
					sink.value = request.Method + " " + request.Pattern
				}
			}
		}()
		next.ServeHTTP(response, request)
	})
}

// recordRoutePattern bridges the pattern captured on Chi's internal request
// clone back to the request observed by outer standard-library middleware.
//
// Complexity: each request adds tight Theta(1) local time and auxiliary space,
// including one sink and one context/request wrapper; downstream routing cost
// is external.
func recordRoutePattern(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		sink := &routePatternSink{}
		routedRequest := request.WithContext(context.WithValue(request.Context(), routePatternSinkContextKey{}, sink))
		defer func() { request.Pattern = sink.value }()
		next.ServeHTTP(response, routedRequest)
	})
}
