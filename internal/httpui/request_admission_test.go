package httpui

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/observability"
)

type captureAbuseObserver struct{ events []abuse.Event }

type unreadRequestBody struct {
	reads  int
	closes int
}

func (body *unreadRequestBody) Read([]byte) (int, error) {
	body.reads++
	return 0, fmt.Errorf("request body must not be read")
}

func (body *unreadRequestBody) Close() error {
	body.closes++
	return fmt.Errorf("request body must not be closed")
}

func (observer *captureAbuseObserver) Observe(_ context.Context, event abuse.Event) {
	observer.events = append(observer.events, event)
}

func newTestRequestLimiter(t *testing.T, capacity, limit uint32) *abuse.RequestLimiter {
	t.Helper()
	limiter, err := abuse.NewRequestLimiter(
		bytes.NewReader(bytes.Repeat([]byte{0x51}, 32)), capacity, limit, time.Minute,
		func() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) },
	)
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	return limiter
}

func TestRequestAdmissionChargesBeforeApplicationWork(t *testing.T) {
	t.Parallel()
	observer := &captureAbuseObserver{}
	nextCalls := 0
	handler, err := NewRequestAdmissionHandler(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		nextCalls++
		response.WriteHeader(http.StatusNoContent)
	}), newTestRequestLimiter(t, 4, 1), observer, false)
	if err != nil {
		t.Fatalf("NewRequestAdmissionHandler() returned error: %v", err)
	}
	handler, err = observability.NewRequestIDMiddleware(handler, func() (string, error) {
		return strings.Repeat("a", 32), nil
	})
	if err != nil {
		t.Fatalf("NewRequestIDMiddleware() returned error: %v", err)
	}
	for call, wantStatus := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodPost, "http://board.example/topics", strings.NewReader("unread-secret"))
		request.RemoteAddr = "192.0.2.10:4567"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != wantStatus {
			t.Fatalf("call %d status = %d, want %d", call, response.Code, wantStatus)
		}
		if call == 1 && (response.Body.String() != "too many requests\n" || response.Header().Get("Retry-After") != "60" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "text/plain; charset=utf-8") {
			t.Fatalf("limited response = headers %#v body %q", response.Header(), response.Body.String())
		}
	}
	if nextCalls != 1 || len(observer.events) != 1 {
		t.Fatalf("calls = next %d, events %d", nextCalls, len(observer.events))
	}
	event := observer.events[0]
	if event.Class != abuse.RejectionRequestRate || event.Route != abuse.RouteRequestAdmission || event.Status != 429 || event.RetrySeconds != 60 || event.RequestID != strings.Repeat("a", 32) {
		t.Fatalf("event = %+v", event)
	}
}

func TestRequestAdmissionCapacityFailureIsFixedAndBounded(t *testing.T) {
	t.Parallel()
	observer := &captureAbuseObserver{}
	handler, err := NewRequestAdmissionHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), newTestRequestLimiter(t, 1, 10), observer, false)
	if err != nil {
		t.Fatalf("NewRequestAdmissionHandler() returned error: %v", err)
	}
	first := httptest.NewRequest(http.MethodGet, "http://board.example/", nil)
	first.RemoteAddr = "192.0.2.1:1"
	handler.ServeHTTP(httptest.NewRecorder(), first)
	second := httptest.NewRequest(http.MethodGet, "http://board.example/", nil)
	second.RemoteAddr = "192.0.2.2:2"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, second)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "service unavailable\n" || response.Header().Get("Retry-After") != "1" || len(observer.events) != 1 || observer.events[0].Class != abuse.RejectionRequestCapacity {
		t.Fatalf("capacity response = status %d, body %q, headers %#v, events %+v", response.Code, response.Body.String(), response.Header(), observer.events)
	}
}

func TestRequestAdmissionFailureDoesNotTouchRequestBody(t *testing.T) {
	t.Parallel()
	limiter := newTestRequestLimiter(t, 1, 1)
	nextCalls := 0
	handler, err := NewRequestAdmissionHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalls++
	}), limiter, &captureAbuseObserver{}, false)
	if err != nil {
		t.Fatalf("NewRequestAdmissionHandler() returned error: %v", err)
	}
	client := "192.0.2.4:80"
	seed := httptest.NewRequest(http.MethodGet, "http://board.example/", nil)
	seed.RemoteAddr = client
	handler.ServeHTTP(httptest.NewRecorder(), seed)
	body := &unreadRequestBody{}
	request := httptest.NewRequest(http.MethodPost, "http://board.example/topics", nil)
	request.RemoteAddr = client
	request.Body = body
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || nextCalls != 1 || body.reads != 0 || body.closes != 0 {
		t.Fatalf("response = %d, next calls %d, body reads %d closes %d", response.Code, nextCalls, body.reads, body.closes)
	}
}

func TestRequestAdmissionProductionIdentityIsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		peer      string
		forwarded []string
		want      int
	}{
		{name: "trusted IPv4", peer: "127.0.0.1:123", forwarded: []string{"192.0.2.1"}, want: http.StatusNoContent},
		{name: "trusted IPv6", peer: "[::1]:123", forwarded: []string{"2001:db8::1"}, want: http.StatusNoContent},
		{name: "mapped client", peer: "127.0.0.1:123", forwarded: []string{"::ffff:192.0.2.1"}, want: http.StatusNoContent},
		{name: "non-loopback peer", peer: "192.0.2.8:123", forwarded: []string{"192.0.2.1"}, want: http.StatusBadRequest},
		{name: "missing forwarded", peer: "127.0.0.1:123", want: http.StatusBadRequest},
		{name: "chain", peer: "127.0.0.1:123", forwarded: []string{"192.0.2.1, 192.0.2.2"}, want: http.StatusBadRequest},
		{name: "multiple fields", peer: "127.0.0.1:123", forwarded: []string{"192.0.2.1", "192.0.2.2"}, want: http.StatusBadRequest},
		{name: "address with port", peer: "127.0.0.1:123", forwarded: []string{"192.0.2.1:90"}, want: http.StatusBadRequest},
		{name: "zone", peer: "127.0.0.1:123", forwarded: []string{"fe80::1%eth0"}, want: http.StatusBadRequest},
		{name: "whitespace", peer: "127.0.0.1:123", forwarded: []string{" 192.0.2.1"}, want: http.StatusBadRequest},
		{name: "noncanonical", peer: "127.0.0.1:123", forwarded: []string{"2001:0db8::1"}, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, err := NewRequestAdmissionHandler(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(http.StatusNoContent)
			}), newTestRequestLimiter(t, 8, 8), &captureAbuseObserver{}, true)
			if err != nil {
				t.Fatalf("NewRequestAdmissionHandler() returned error: %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, "https://board.example/", nil)
			request.RemoteAddr = test.peer
			for _, value := range test.forwarded {
				request.Header.Add("X-Forwarded-For", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if test.want == http.StatusBadRequest && (response.Body.String() != "bad request\n" || response.Header().Get("Cache-Control") != "no-store") {
				t.Fatalf("bad identity response = headers %#v body %q", response.Header(), response.Body.String())
			}
		})
	}
}

func TestRequestAdmissionDevelopmentRejectsForwarding(t *testing.T) {
	t.Parallel()
	handler, err := NewRequestAdmissionHandler(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), newTestRequestLimiter(t, 8, 8), &captureAbuseObserver{}, false)
	if err != nil {
		t.Fatalf("NewRequestAdmissionHandler() returned error: %v", err)
	}
	for _, test := range []struct {
		peer      string
		forwarded string
		want      int
	}{
		{peer: "192.0.2.1:80", want: http.StatusNoContent},
		{peer: "[::ffff:192.0.2.1]:80", want: http.StatusNoContent},
		{peer: "192.0.2.1:80", forwarded: "192.0.2.2", want: http.StatusBadRequest},
		{peer: "192.0.2.1", want: http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://board.example/", nil)
		request.RemoteAddr = test.peer
		if test.forwarded != "" {
			request.Header.Set("X-Forwarded-For", test.forwarded)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("peer %q forwarded %q status = %d, want %d", test.peer, test.forwarded, response.Code, test.want)
		}
	}
}

func TestRequestAdmissionExemptionIsClosed(t *testing.T) {
	t.Parallel()
	handler, err := NewRequestAdmissionHandler(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), newTestRequestLimiter(t, 1, 1), &captureAbuseObserver{}, false)
	if err != nil {
		t.Fatalf("NewRequestAdmissionHandler() returned error: %v", err)
	}
	paths := []string{
		"/health/live", "/health/ready", "/static/" + appStylesheetFilename,
		"/static/htmx-2.0.10.min.js", "/static/" + discoveryResponseFilename,
		"/static/" + markdownToolbarFilename,
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range paths {
			request := httptest.NewRequest(method, "http://board.example"+path, nil)
			request.RemoteAddr = "192.0.2.1:80"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("%s %s status = %d", method, path, response.Code)
			}
		}
	}
	for index, requestTarget := range []string{"/", "/health/live/", "/static/unknown.css"} {
		request := httptest.NewRequest(http.MethodGet, "http://board.example"+requestTarget, nil)
		request.RemoteAddr = "192.0.2.1:80"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if index > 0 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("near-miss %s status = %d, want %d", requestTarget, response.Code, want)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://board.example/health/live", nil)
	request.RemoteAddr = "192.0.2.2:80"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST health status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestRequestAdmissionRejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	limiter := newTestRequestLimiter(t, 1, 1)
	observer := &captureAbuseObserver{}
	for _, test := range []struct {
		name     string
		next     http.Handler
		limiter  *abuse.RequestLimiter
		observer abuse.Observer
	}{
		{name: "nil next", limiter: limiter, observer: observer},
		{name: "nil limiter", next: next, observer: observer},
		{name: "nil observer", next: next, limiter: limiter},
	} {
		if handler, err := NewRequestAdmissionHandler(test.next, test.limiter, test.observer, false); err == nil || handler != nil {
			t.Fatalf("%s = (%v, %v)", test.name, handler, err)
		}
	}
}
