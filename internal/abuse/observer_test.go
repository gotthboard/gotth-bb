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
