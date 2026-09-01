package observability

import (
	"io"
	"strings"
	"testing"
)

func TestGenerateRequestID(t *testing.T) {
	t.Parallel()

	source := strings.NewReader("0123456789abcdef")
	got, err := GenerateRequestID(source)
	if err != nil {
		t.Fatalf("GenerateRequestID() returned error: %v", err)
	}
	if got != "30313233343536373839616263646566" {
		t.Fatalf("GenerateRequestID() = %q", got)
	}
}

func TestGenerateRequestIDRejectsUnavailableEntropy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source io.Reader
	}{
		{name: "nil source", source: nil},
		{name: "short source", source: strings.NewReader("too short")},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got, err := GenerateRequestID(test.source); err == nil {
				t.Fatalf("GenerateRequestID() = %q, want error", got)
			}
		})
	}
}
