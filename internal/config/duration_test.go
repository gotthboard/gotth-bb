package config

import (
	"strings"
	"testing"
	"time"
)

func TestParsePositiveDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want time.Duration
	}{
		{raw: "30s", want: 30 * time.Second},
		{raw: "15m", want: 15 * time.Minute},
		{raw: "24h", want: 24 * time.Hour},
	}

	for _, test := range tests {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()

			got, err := parsePositiveDuration("SESSION_MAX_AGE", test.raw)
			if err != nil {
				t.Fatalf("parsePositiveDuration(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("parsePositiveDuration(%q) = %s, want %s", test.raw, got, test.want)
			}
		})
	}
}

func TestParsePositiveDurationRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "0", "0s", "-1s", "forever", "999999999999999999999h"} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := parsePositiveDuration("SESSION_MAX_AGE", raw); err == nil {
				t.Fatalf("parsePositiveDuration(%q) = %s, want error", raw, got)
			}
		})
	}
}

func TestParsePositiveDurationRedactsMalformedInput(t *testing.T) {
	t.Parallel()

	const secret = "do-not-log-duration"
	_, err := parsePositiveDuration("SESSION_MAX_AGE", secret)
	if err == nil {
		t.Fatal("parsePositiveDuration accepted malformed input")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("parsePositiveDuration error exposed configured value: %q", err)
	}
}
