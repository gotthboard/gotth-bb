package observability

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewAccessLogMiddlewareLogsBoundedRequestEvidence(t *testing.T) {
	t.Parallel()

	const requestID = "30313233343536373839616263646566"
	const querySecret = "do-not-log-query"
	var logs bytes.Buffer
	times := []time.Time{time.Unix(100, 0), time.Unix(100, int64(25*time.Millisecond))}
	clockIndex := 0
	clock := func() time.Time {
		value := times[clockIndex]
		clockIndex++
		return value
	}
	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte("hello"))
	})
	handler, err := NewAccessLogMiddleware(next, slog.New(slog.NewJSONHandler(&logs, nil)), clock)
	if err != nil {
		t.Fatalf("NewAccessLogMiddleware() returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/topics?token="+querySecret, nil)
	request.Pattern = "POST /topics"
	request = request.WithContext(context.WithValue(request.Context(), requestIDContextKey{}, requestID))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	logged := logs.String()
	for _, evidence := range []string{
		`"request_id":"` + requestID + `"`,
		`"route":"POST /topics"`,
		`"method":"POST"`,
		`"status":201`,
		`"bytes":5`,
		`"duration_ms":25`,
	} {
		if !strings.Contains(logged, evidence) {
			t.Fatalf("log %q lacks %q", logged, evidence)
		}
	}
	if strings.Contains(logged, querySecret) {
		t.Fatalf("log exposed query value: %q", logged)
	}
}

func TestNewAccessLogMiddlewareTreatsEmptyResponseAsSuccess(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	times := []time.Time{time.Unix(101, 0), time.Unix(100, 0)}
	clockIndex := 0
	clock := func() time.Time {
		value := times[clockIndex]
		clockIndex++
		return value
	}
	handler, err := NewAccessLogMiddleware(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		slog.New(slog.NewJSONHandler(&logs, nil)),
		clock,
	)
	if err != nil {
		t.Fatalf("NewAccessLogMiddleware() returned error: %v", err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(logs.String(), `"status":200`) {
		t.Fatalf("log = %q", logs.String())
	}
	if !strings.Contains(logs.String(), `"duration_ms":0`) {
		t.Fatalf("negative duration was not clamped: %q", logs.String())
	}
}

func TestAccessLogCompositionRecordsQuietAbortWithoutFalseStatus(t *testing.T) {
	t.Parallel()

	const requestID = "30313233343536373839616263646566"
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
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			recovery, err := NewRecoveryMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				if testCase.committed {
					_, _ = response.Write([]byte("partial"))
				}
				panic(http.ErrAbortHandler)
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
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Pattern = "GET /"
			response := httptest.NewRecorder()
			recovered := capturePanic(func() {
				handler.ServeHTTP(response, request)
			})

			if recovered != http.ErrAbortHandler {
				t.Fatalf("recovered panic = %v, want http.ErrAbortHandler", recovered)
			}
			logged := logs.String()
			for _, evidence := range []string{
				`"msg":"request aborted"`,
				`"error_class":"abort"`,
				`"request_id":"` + requestID + `"`,
				`"route":"GET /"`,
			} {
				if !strings.Contains(logged, evidence) {
					t.Fatalf("log %q lacks %q", logged, evidence)
				}
			}
			if strings.Contains(logged, `"status":`) || strings.Contains(logged, `"msg":"request completed"`) {
				t.Fatalf("abort was logged as a completed response: %q", logged)
			}
		})
	}
}

func TestNewAccessLogMiddlewarePropagatesUnexpectedPanicWithoutCompletionLog(t *testing.T) {
	t.Parallel()

	const panicValue = "unexpected-outer-panic"
	var logs bytes.Buffer
	times := []time.Time{time.Unix(100, 0), time.Unix(100, int64(time.Millisecond))}
	clockIndex := 0
	handler, err := NewAccessLogMiddleware(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(panicValue)
		}),
		slog.New(slog.NewJSONHandler(&logs, nil)),
		func() time.Time {
			value := times[clockIndex]
			clockIndex++
			return value
		},
	)
	if err != nil {
		t.Fatalf("NewAccessLogMiddleware() returned error: %v", err)
	}

	recovered := capturePanic(func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
	if recovered != panicValue {
		t.Fatalf("recovered panic = %v", recovered)
	}
	if logs.Len() != 0 {
		t.Fatalf("unexpected panic emitted a false completion log: %q", logs.String())
	}
}

func TestNewAccessLogMiddlewareRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	clock := func() time.Time { return time.Time{} }
	if got, err := NewAccessLogMiddleware(nil, logger, clock); err == nil || got != nil {
		t.Fatalf("NewAccessLogMiddleware(nil, logger, clock) = %v, %v", got, err)
	}
	if got, err := NewAccessLogMiddleware(handler, nil, clock); err == nil || got != nil {
		t.Fatalf("NewAccessLogMiddleware(handler, nil, clock) = %v, %v", got, err)
	}
	if got, err := NewAccessLogMiddleware(handler, logger, nil); err == nil || got != nil {
		t.Fatalf("NewAccessLogMiddleware(handler, logger, nil) = %v, %v", got, err)
	}
}
