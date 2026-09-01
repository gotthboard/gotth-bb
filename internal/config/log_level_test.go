package config

import (
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want slog.Level
	}{
		{raw: "", want: slog.LevelInfo},
		{raw: "debug", want: slog.LevelDebug},
		{raw: "info", want: slog.LevelInfo},
		{raw: "warn", want: slog.LevelWarn},
		{raw: "error", want: slog.LevelError},
	}

	for _, test := range tests {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()

			got, err := ParseLogLevel(test.raw)
			if err != nil {
				t.Fatalf("ParseLogLevel(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ParseLogLevel(%q) = %s, want %s", test.raw, got, test.want)
			}
		})
	}
}

func TestParseLogLevelRejectsUnknownValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"Debug", "warning", "INFO+1", " info", "info "} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseLogLevel(raw); err == nil {
				t.Fatalf("ParseLogLevel(%q) = %s, want error", raw, got)
			}
		})
	}
}
