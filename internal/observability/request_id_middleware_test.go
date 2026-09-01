package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRequestIDMiddleware(t *testing.T) {
	t.Parallel()

	const generated = "30313233343536373839616263646566"
	next := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestID, ok := RequestID(request.Context())
		if !ok || requestID != generated {
			t.Fatalf("RequestID(context) = %q, %v", requestID, ok)
		}
		response.WriteHeader(http.StatusNoContent)
	})
	handler, err := NewRequestIDMiddleware(next, func() (string, error) {
		return generated, nil
	})
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "attacker-controlled")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("X-Request-ID"); got != generated {
		t.Fatalf("X-Request-ID = %q", got)
	}
}

func TestNewRequestIDMiddlewareFailsClosedWithoutEntropy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		generator func() (string, error)
	}{
		{name: "entropy error", generator: func() (string, error) { return "", errors.New("entropy failed") }},
		{name: "empty identifier", generator: func() (string, error) { return "", nil }},
		{name: "short identifier", generator: func() (string, error) { return "abc", nil }},
		{name: "uppercase identifier", generator: func() (string, error) { return "3031323334353637383961626364656A", nil }},
		{name: "header injection", generator: func() (string, error) { return "303132333435363738396162636465\n", nil }},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			nextCalled := false
			handler, err := NewRequestIDMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				nextCalled = true
			}), test.generator)
			if err != nil {
				t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
			}

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			if nextCalled {
				t.Fatal("downstream handler ran without a request ID")
			}
			if got := response.Header().Get("X-Request-ID"); got != "" {
				t.Fatalf("failed request exposed X-Request-ID %q", got)
			}
		})
	}
}

func TestNewRequestIDMiddlewareRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	validGenerator := func() (string, error) { return "request-id", nil }
	if got, err := NewRequestIDMiddleware(nil, validGenerator); err == nil || got != nil {
		t.Fatalf("NewRequestIDMiddleware(nil, generator) = %v, %v", got, err)
	}
	if got, err := NewRequestIDMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil); err == nil || got != nil {
		t.Fatalf("NewRequestIDMiddleware(handler, nil) = %v, %v", got, err)
	}
}
