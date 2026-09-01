package httpui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRecordRoutePatternRunsAfterSuccessAndPanic(t *testing.T) {
	t.Parallel()

	for _, panicAfterMatch := range []bool{false, true} {
		router := chi.NewRouter()
		router.Use(captureRoutePattern)
		router.Get("/topics/{topicID}", func(response http.ResponseWriter, _ *http.Request) {
			if panicAfterMatch {
				panic("test panic")
			}
			response.WriteHeader(http.StatusNoContent)
		})
		request := httptest.NewRequest(http.MethodGet, "/topics/01JTEST", nil)
		response := httptest.NewRecorder()
		handler := recordRoutePattern(router)
		if panicAfterMatch {
			_ = captureHandlerPanic(func() { handler.ServeHTTP(response, request) })
		} else {
			handler.ServeHTTP(response, request)
		}
		if request.Pattern != "GET /topics/{topicID}" {
			t.Fatalf("panic = %v, request pattern = %q", panicAfterMatch, request.Pattern)
		}
	}
}
