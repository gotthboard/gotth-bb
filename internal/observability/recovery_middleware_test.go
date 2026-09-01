package observability

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRecoveryMiddlewarePassesSuccessfulResponse(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler, err := NewRecoveryMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent || logs.Len() != 0 {
		t.Fatalf("status = %d, logs = %q", response.Code, logs.String())
	}
}

func TestNewRecoveryMiddlewareHandlesUncommittedPanic(t *testing.T) {
	t.Parallel()

	const requestID = "30313233343536373839616263646566"
	const panicSecret = "do-not-log-panic-value"
	var logs bytes.Buffer
	handler, err := NewRecoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(panicSecret)
	}), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(context.WithValue(request.Context(), requestIDContextKey{}, requestID))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), requestID) || strings.Contains(response.Body.String(), panicSecret) {
		t.Fatalf("body = %q", response.Body.String())
	}
	if !strings.Contains(logs.String(), requestID) || !strings.Contains(logs.String(), `"error_class":"panic"`) {
		t.Fatalf("logs lack request evidence: %q", logs.String())
	}
	if strings.Contains(logs.String(), panicSecret) {
		t.Fatalf("logs exposed panic value: %q", logs.String())
	}
}

func TestNewRecoveryMiddlewareRestoresOnlyPreApplicationHeaders(t *testing.T) {
	t.Parallel()

	const requestID = "30313233343536373839616263646566"
	var logs bytes.Buffer
	recovery, err := NewRecoveryMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Request-ID", "handler-overwrite")
		response.Header().Set("Set-Cookie", "session=secret")
		response.Header().Set("Location", "https://example.invalid/stale")
		response.Header().Set("X-Secret", "do-not-emit")
		panic("failed")
	}), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
	}
	handler, err := NewRequestIDMiddleware(recovery, func() (string, error) {
		return requestID, nil
	})
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("X-Request-ID"); got != requestID {
		t.Fatalf("X-Request-ID = %q", got)
	}
	for _, name := range []string{"Set-Cookie", "Location", "X-Secret"} {
		if got := response.Header().Get(name); got != "" {
			t.Fatalf("%s survived recovery: %q", name, got)
		}
	}
}

func TestRecoveryCompositionHandlesRejectedStatusAsUncommitted(t *testing.T) {
	t.Parallel()

	const requestID = "30313233343536373839616263646566"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	recovery, err := NewRecoveryMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(99)
	}), logger)
	if err != nil {
		t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
	}
	times := []time.Time{time.Unix(100, 0), time.Unix(100, int64(time.Millisecond))}
	clockIndex := 0
	accessLog, err := NewAccessLogMiddleware(recovery, logger, func() time.Time {
		value := times[clockIndex]
		clockIndex++
		return value
	})
	if err != nil {
		t.Fatalf("NewAccessLogMiddleware() returned error: %v", err)
	}
	handler, err := NewRequestIDMiddleware(accessLog, func() (string, error) {
		return requestID, nil
	})
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(logs.String(), `"status":500`) {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestRecoveryCompositionHandlesPanicAfterInformationalStatus(t *testing.T) {
	t.Parallel()

	const requestID = "30313233343536373839616263646566"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	recovery, err := NewRecoveryMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusEarlyHints)
		panic("failed after informational response")
	}), logger)
	if err != nil {
		t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
	}
	times := []time.Time{time.Unix(100, 0), time.Unix(100, int64(time.Millisecond))}
	clockIndex := 0
	accessLog, err := NewAccessLogMiddleware(recovery, logger, func() time.Time {
		value := times[clockIndex]
		clockIndex++
		return value
	})
	if err != nil {
		t.Fatalf("NewAccessLogMiddleware() returned error: %v", err)
	}
	handler, err := NewRequestIDMiddleware(accessLog, func() (string, error) {
		return requestID, nil
	})
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
	}
	response := &statusSequenceWriter{header: make(http.Header)}
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := fmt.Sprint(response.statuses); got != "[103 500]" {
		t.Fatalf("forwarded statuses = %s", got)
	}
	if !strings.Contains(logs.String(), `"status":500`) {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestNewRecoveryMiddlewareClosesCommittedPanicWithoutLeakingValue(t *testing.T) {
	t.Parallel()

	const panicSecret = "do-not-repanic-this-value"
	var logs bytes.Buffer
	handler, err := NewRecoveryMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("partial"))
		panic(panicSecret)
	}), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
	}

	response := httptest.NewRecorder()
	recovered := capturePanic(func() {
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	})
	if recovered == nil {
		t.Fatal("committed panic was swallowed instead of closing the response")
	}
	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered panic = %v, want http.ErrAbortHandler", recovered)
	}
	if strings.Contains(logs.String(), panicSecret) {
		t.Fatalf("panic value leaked through %q or logs %q", recovered, logs.String())
	}
	if response.Body.String() != "partial" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestNewRecoveryMiddlewarePropagatesQuietAbortWithoutLogging(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		committed bool
	}{
		{name: "uncommitted"},
		{name: "committed", committed: true},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			handler, err := NewRecoveryMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				if testCase.committed {
					_, _ = response.Write([]byte("partial"))
				}
				panic(http.ErrAbortHandler)
			}), slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatalf("NewRecoveryMiddleware() returned error: %v", err)
			}

			response := httptest.NewRecorder()
			recovered := capturePanic(func() {
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			})
			if recovered != http.ErrAbortHandler {
				t.Fatalf("recovered panic = %v, want http.ErrAbortHandler", recovered)
			}
			if logs.Len() != 0 {
				t.Fatalf("quiet abort emitted recovery log: %q", logs.String())
			}
		})
	}
}

func TestNewRecoveryMiddlewareRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	if got, err := NewRecoveryMiddleware(nil, logger); err == nil || got != nil {
		t.Fatalf("NewRecoveryMiddleware(nil, logger) = %v, %v", got, err)
	}
	if got, err := NewRecoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil); err == nil || got != nil {
		t.Fatalf("NewRecoveryMiddleware(handler, nil) = %v, %v", got, err)
	}
}

func capturePanic(run func()) (recovered any) {
	defer func() {
		recovered = recover()
	}()
	run()
	return nil
}
