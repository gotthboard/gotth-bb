package httpui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthRoutes(t *testing.T) {
	t.Parallel()

	handler := NewHandler()
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{name: "liveness", path: "/health/live", wantStatus: http.StatusOK, wantBody: "ok\n"},
		{name: "readiness fails before dependencies exist", path: "/health/ready", wantStatus: http.StatusServiceUnavailable, wantBody: "not ready\n"},
		{name: "unknown route", path: "/missing", wantStatus: http.StatusNotFound, wantBody: "404 page not found\n"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("GET %s status = %d, want %d", test.path, response.Code, test.wantStatus)
			}
			if response.Body.String() != test.wantBody {
				t.Fatalf("GET %s body = %q, want %q", test.path, response.Body.String(), test.wantBody)
			}
			if got := response.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Fatalf("GET %s Content-Type = %q", test.path, got)
			}
		})
	}
}

func TestHealthRoutesRejectMutationMethods(t *testing.T) {
	t.Parallel()

	handler := NewHandler()
	for _, path := range []string{"/health/live", "/health/ready"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodPost, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s status = %d, want %d", path, response.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}
