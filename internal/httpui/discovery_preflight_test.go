package httpui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/discovery"
)

type blockingDiscoveryWriter struct {
	header  http.Header
	entered chan<- struct{}
	release <-chan struct{}
}

func (writer *blockingDiscoveryWriter) Header() http.Header { return writer.header }
func (*blockingDiscoveryWriter) WriteHeader(int)            {}
func (writer *blockingDiscoveryWriter) Write(value []byte) (int, error) {
	writer.entered <- struct{}{}
	<-writer.release
	return len(value), nil
}

func TestDiscoveryPreflightRejectsInputBeforeDownstream(t *testing.T) {
	t.Parallel()

	builder := mustURLBuilder(t, "/bb")
	var calls atomic.Int32
	downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	preflight, err := newDiscoveryPreflightHandler(builder, downstream, func(string) (discovery.AuthenticatedCursor, error) {
		calls.Add(100)
		return discovery.AuthenticatedCursor{}, context.Canceled
	})
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandler() returned error: %v", err)
	}

	for _, target := range []string{
		"/search", "/search?area=private", "/activity?cursor=short", "/posts/7?unexpected=1",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		preflight.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s response = (%d, %v, %q)", target, response.Code, response.Header(), response.Body.String())
		}
		if request.Pattern == "" || strings.Contains(request.Pattern, "?") {
			t.Fatalf("%s route pattern = %q", target, request.Pattern)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("downstream/MAC calls = %d, want zero", calls.Load())
	}
}

func TestDiscoveryPreflightVerifiesCursorBeforeDownstream(t *testing.T) {
	t.Parallel()

	builder := mustURLBuilder(t, "")
	var verified, downstream atomic.Int32
	handler, err := newDiscoveryPreflightHandler(builder, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		downstream.Add(1)
		if _, ok := discoveryCursorFromContext(request.Context()); !ok {
			t.Error("verified cursor missing from request context")
		}
		response.WriteHeader(http.StatusNoContent)
	}), func(encoded string) (discovery.AuthenticatedCursor, error) {
		verified.Add(1)
		if len(encoded) != discovery.EncodedCursorLength {
			t.Fatalf("encoded cursor length = %d", len(encoded))
		}
		return discovery.AuthenticatedCursor{}, nil
	})
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandler() returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/activity?cursor="+repeatByte('A', discovery.EncodedCursorLength), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || verified.Load() != 1 || downstream.Load() != 1 {
		t.Fatalf("response=%d verified=%d downstream=%d", response.Code, verified.Load(), downstream.Load())
	}
}

func TestDiscoveryPreflightRejectsThirdConcurrentRequestWithoutWaiting(t *testing.T) {
	t.Parallel()

	builder := mustURLBuilder(t, "")
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	downstream := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		entered <- struct{}{}
		<-release
		response.WriteHeader(http.StatusNoContent)
	})
	handler, err := newDiscoveryPreflightHandler(builder, downstream, func(string) (discovery.AuthenticatedCursor, error) {
		return discovery.AuthenticatedCursor{}, nil
	})
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandler() returned error: %v", err)
	}

	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/search?q=term", nil))
		}()
	}
	<-entered
	<-entered
	response := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/activity", nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" || time.Since(start) > time.Second {
		t.Fatalf("saturated response = (%d, %v) after %s", response.Code, response.Header(), time.Since(start))
	}
	close(release)
	wait.Wait()
}

func TestDiscoveryPreflightHoldsPermitsThroughResponseWrite(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	handler, err := newDiscoveryPreflightHandler(mustURLBuilder(t, ""), http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("complete response"))
	}), func(string) (discovery.AuthenticatedCursor, error) { return discovery.AuthenticatedCursor{}, nil })
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandler() returned error: %v", err)
	}
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			writer := &blockingDiscoveryWriter{header: make(http.Header), entered: entered, release: release}
			handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/search?q=term", nil))
		}()
	}
	<-entered
	<-entered
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/activity", nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("write-phase saturation response = (%d, %v)", response.Code, response.Header())
	}
	close(release)
	wait.Wait()
}

func TestDiscoveryPreflightBoundsDownstreamContext(t *testing.T) {
	t.Parallel()

	handler, err := newDiscoveryPreflightHandlerWithTimeouts(
		mustURLBuilder(t, ""),
		http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			<-request.Context().Done()
			if !errors.Is(request.Context().Err(), context.DeadlineExceeded) {
				t.Errorf("downstream context error = %v", request.Context().Err())
			}
			response.WriteHeader(http.StatusServiceUnavailable)
		}),
		func(string) (discovery.AuthenticatedCursor, error) { return discovery.AuthenticatedCursor{}, nil },
		discoveryTimeouts{search: 30 * time.Millisecond, activity: 20 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("newDiscoveryPreflightHandlerWithTimeouts() returned error: %v", err)
	}
	start := time.Now()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/activity", nil))
	if elapsed := time.Since(start); response.Code != http.StatusServiceUnavailable || elapsed < 15*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("bounded response = (status %d, elapsed %s)", response.Code, elapsed)
	}
}

func TestDiscoveryPreflightRejectsIncompleteConstruction(t *testing.T) {
	t.Parallel()

	builder := mustURLBuilder(t, "")
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	verify := func(string) (discovery.AuthenticatedCursor, error) { return discovery.AuthenticatedCursor{}, nil }
	for _, test := range []struct {
		name     string
		builder  URLBuilder
		next     http.Handler
		verify   activityCursorVerifier
		timeouts discoveryTimeouts
	}{
		{name: "downstream", builder: builder, verify: verify, timeouts: discoveryTimeouts{search: time.Second, activity: time.Second}},
		{name: "verifier", builder: builder, next: next, timeouts: discoveryTimeouts{search: time.Second, activity: time.Second}},
		{name: "timeouts", builder: builder, next: next, verify: verify},
		{name: "builder", next: next, verify: verify, timeouts: discoveryTimeouts{search: time.Second, activity: time.Second}},
	} {
		if handler, err := newDiscoveryPreflightHandlerWithTimeouts(test.builder, test.next, test.verify, test.timeouts); err == nil || handler != nil {
			t.Fatalf("%s construction = (%v, %v)", test.name, handler, err)
		}
	}
}

func repeatByte(value byte, count int) string {
	buffer := make([]byte, count)
	for index := range buffer {
		buffer[index] = value
	}
	return string(buffer)
}

func mustURLBuilder(t *testing.T, basePath string) URLBuilder {
	t.Helper()
	public, err := url.Parse("https://forum.example.test" + basePath)
	if err != nil {
		t.Fatalf("parse public URL: %v", err)
	}
	builder, err := NewURLBuilder(*public, basePath)
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	return builder
}
