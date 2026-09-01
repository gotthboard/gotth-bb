package config

import "testing"

func TestParseEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want Environment
	}{
		{raw: "development", want: EnvironmentDevelopment},
		{raw: "test", want: EnvironmentTest},
		{raw: "production", want: EnvironmentProduction},
	}

	for _, test := range tests {
		test := test
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()

			got, err := ParseEnvironment(test.raw)
			if err != nil {
				t.Fatalf("ParseEnvironment(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ParseEnvironment(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestParseEnvironmentRejectsUnknownValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "prod", "Production", " production", "production "} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseEnvironment(raw); err == nil {
				t.Fatalf("ParseEnvironment(%q) = %q, want error", raw, got)
			}
		})
	}
}
