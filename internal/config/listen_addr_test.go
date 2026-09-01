package config

import (
	"strings"
	"testing"
)

func TestParseListenAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		environment Environment
		want        string
	}{
		{name: "production IPv4 loopback", raw: "127.0.0.1:8080", environment: EnvironmentProduction, want: "127.0.0.1:8080"},
		{name: "production IPv6 loopback", raw: "[::1]:8080", environment: EnvironmentProduction, want: "[::1]:8080"},
		{name: "development wildcard", raw: "0.0.0.0:8080", environment: EnvironmentDevelopment, want: "0.0.0.0:8080"},
		{name: "test loopback", raw: "127.0.0.1:9090", environment: EnvironmentTest, want: "127.0.0.1:9090"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseListenAddr(test.raw, test.environment)
			if err != nil {
				t.Fatalf("ParseListenAddr(%q) returned error: %v", test.raw, err)
			}
			if got.String() != test.want {
				t.Fatalf("ParseListenAddr(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestParseListenAddrRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		environment Environment
	}{
		{name: "empty", raw: "", environment: EnvironmentDevelopment},
		{name: "hostname", raw: "localhost:8080", environment: EnvironmentDevelopment},
		{name: "missing port", raw: "127.0.0.1", environment: EnvironmentDevelopment},
		{name: "zero port", raw: "127.0.0.1:0", environment: EnvironmentDevelopment},
		{name: "IPv6 zone", raw: "[fe80::1%eth0]:8080", environment: EnvironmentDevelopment},
		{name: "public IPv4 in production", raw: "0.0.0.0:8080", environment: EnvironmentProduction},
		{name: "public IPv6 in production", raw: "[::]:8080", environment: EnvironmentProduction},
		{name: "unknown environment", raw: "127.0.0.1:8080", environment: Environment("staging")},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseListenAddr(test.raw, test.environment); err == nil {
				t.Fatalf("ParseListenAddr(%q) = %q, want error", test.raw, got)
			}
		})
	}
}

func TestParseListenAddrRedactsMalformedInput(t *testing.T) {
	t.Parallel()

	const secret = "do-not-log-listen-address"
	_, err := ParseListenAddr(secret, EnvironmentDevelopment)
	if err == nil {
		t.Fatal("ParseListenAddr accepted malformed input")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("ParseListenAddr error exposed configured value: %q", err)
	}
}
