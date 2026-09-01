package app

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewHTTPHandlerComposesRequestBoundary(t *testing.T) {
	t.Parallel()

	const inboundID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const generatedID = "30313233343536373839616263646566"
	var logs bytes.Buffer
	times := []time.Time{time.Unix(100, 0), time.Unix(100, int64(time.Millisecond))}
	clockIndex := 0
	handler, err := NewHTTPHandler(
		http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			request.Pattern = "GET /"
			response.WriteHeader(http.StatusNoContent)
		}),
		slog.New(slog.NewJSONHandler(&logs, nil)),
		strings.NewReader("0123456789abcdef"),
		func() time.Time {
			value := times[clockIndex]
			clockIndex++
			return value
		},
	)
	if err != nil {
		t.Fatalf("NewHTTPHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", inboundID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("X-Request-ID"); got != generatedID {
		t.Fatalf("X-Request-ID = %q", got)
	}
	if !strings.Contains(logs.String(), `"request_id":"`+generatedID+`"`) || strings.Contains(logs.String(), inboundID) {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestNewHTTPHandlerFailsClosedWhenEntropyFails(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler, err := NewHTTPHandler(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("application ran without a request ID")
		}),
		slog.New(slog.NewJSONHandler(&logs, nil)),
		errReader{},
		time.Now,
	)
	if err != nil {
		t.Fatalf("NewHTTPHandler() returned error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable || logs.Len() != 0 {
		t.Fatalf("status = %d, logs = %q", response.Code, logs.String())
	}
}

func TestNewHTTPHandlerRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	clock := time.Now
	reader := strings.NewReader("0123456789abcdef")
	for _, testCase := range []struct {
		name        string
		application http.Handler
		logger      *slog.Logger
		reader      io.Reader
		clock       func() time.Time
	}{
		{name: "application", logger: logger, reader: reader, clock: clock},
		{name: "logger", application: handler, reader: reader, clock: clock},
		{name: "entropy", application: handler, logger: logger, clock: clock},
		{name: "clock", application: handler, logger: logger, reader: reader},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got, err := NewHTTPHandler(testCase.application, testCase.logger, testCase.reader, testCase.clock); err == nil || got != nil {
				t.Fatalf("NewHTTPHandler() = %v, %v", got, err)
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
