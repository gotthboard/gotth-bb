package abuse

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestObserverEmitsOnlyFixedNonidentityFields(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	observer, err := NewObserver(slog.New(slog.NewJSONHandler(&output, nil)))
	if err != nil {
		t.Fatalf("NewObserver() returned error: %v", err)
	}
	observer.Observe(context.Background(), Event{
		Class: RejectionRequestRate, Route: RouteRequestAdmission,
		RequestID: strings.Repeat("a", 32), Status: 429, RetrySeconds: 17,
	})
	logged := output.String()
	for _, expected := range []string{
		`"msg":"abuse request rejected"`, `"class":"request_rate"`,
		`"route":"request-admission"`, `"request_id":"` + strings.Repeat("a", 32) + `"`,
		`"status":429`, `"retry_seconds":17`,
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("log %q is missing %q", logged, expected)
		}
	}
}

func TestObserverDropsMalformedEvents(t *testing.T) {
	t.Parallel()
	valid := Event{
		Class: RejectionRequestCapacity, Route: RouteRequestAdmission,
		RequestID: strings.Repeat("b", 32), Status: 503, RetrySeconds: 1,
	}
	tests := []Event{
		{},
		{Class: "raw-address", Route: RouteRequestAdmission, RequestID: valid.RequestID, Status: 429, RetrySeconds: 1},
		{Class: RejectionRequestCapacity, Route: "https://attacker.example/raw", RequestID: valid.RequestID, Status: 503, RetrySeconds: 1},
		{Class: RejectionRequestCapacity, Route: RouteRequestAdmission, RequestID: "not-an-id", Status: 503, RetrySeconds: 1},
		{Class: RejectionRequestCapacity, Route: RouteRequestAdmission, RequestID: strings.Repeat("A", 32), Status: 503, RetrySeconds: 1},
		{Class: RejectionRequestCapacity, Route: RouteRequestAdmission, RequestID: valid.RequestID, Status: 429, RetrySeconds: 1},
		{Class: RejectionRequestCapacity, Route: RouteRequestAdmission, RequestID: valid.RequestID, Status: 503, RetrySeconds: 2},
	}
	for index, event := range tests {
		var output bytes.Buffer
		observer, err := NewObserver(slog.New(slog.NewJSONHandler(&output, nil)))
		if err != nil {
			t.Fatalf("NewObserver() returned error: %v", err)
		}
		observer.Observe(context.Background(), event)
		if output.Len() != 0 {
			t.Fatalf("event %d produced log %q", index, output.String())
		}
	}
	if observer, err := NewObserver(nil); err == nil || observer != nil {
		t.Fatalf("NewObserver(nil) = (%v, %v)", observer, err)
	}
}

func TestObserverAcceptsEveryFixedTerminalClass(t *testing.T) {
	t.Parallel()
	for _, event := range []Event{
		{Class: RejectionRequestRate, Route: RouteRequestAdmission, RequestID: strings.Repeat("1", 32), Status: 429, RetrySeconds: 86_400},
		{Class: RejectionRequestCapacity, Route: RouteRequestAdmission, RequestID: strings.Repeat("2", 32), Status: 503, RetrySeconds: 1},
		{Class: RejectionPublicationRate, Route: RouteTopicPublication, RequestID: strings.Repeat("3", 32), Status: 429, RetrySeconds: 60},
		{Class: RejectionPublicationRate, Route: RouteReplyPublication, RequestID: strings.Repeat("4", 32), Status: 429, RetrySeconds: 60},
		{Class: RejectionBlocked, Route: RouteTopicPublication, RequestID: strings.Repeat("5", 32), Status: 422},
		{Class: RejectionBlocked, Route: RouteReplyPublication, RequestID: strings.Repeat("6", 32), Status: 422},
		{Class: RejectionBlocked, Route: RoutePostEdit, RequestID: strings.Repeat("7", 32), Status: 422},
		{Class: RejectionBlocked, Route: RouteCommunityRules, RequestID: strings.Repeat("8", 32), Status: 422},
	} {
		var output bytes.Buffer
		observer, err := NewObserver(slog.New(slog.NewJSONHandler(&output, nil)))
		if err != nil {
			t.Fatalf("NewObserver() returned error: %v", err)
		}
		observer.Observe(context.Background(), event)
		if output.Len() == 0 {
			t.Fatalf("event %+v was dropped", event)
		}
	}
}
