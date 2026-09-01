package observability

import (
	"context"
	"testing"
)

func TestRequestID(t *testing.T) {
	t.Parallel()

	if got, ok := RequestID(context.Background()); ok || got != "" {
		t.Fatalf("RequestID(empty) = %q, %v", got, ok)
	}
	configured := context.WithValue(context.Background(), requestIDContextKey{}, "request-123")
	if got, ok := RequestID(configured); !ok || got != "request-123" {
		t.Fatalf("RequestID(configured) = %q, %v", got, ok)
	}
	wrongType := context.WithValue(context.Background(), requestIDContextKey{}, 123)
	if got, ok := RequestID(wrongType); ok || got != "" {
		t.Fatalf("RequestID(wrong type) = %q, %v", got, ok)
	}
}
