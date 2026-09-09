package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunRejectsCanceledStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, func(string) (string, bool) { return "", false }); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled startup error = %v", err)
	}
}

func TestRunRequiresClosedConfiguration(t *testing.T) {
	if err := run(context.Background(), func(string) (string, bool) { return "", false }); err == nil || !strings.Contains(err.Error(), "OIDC_ISSUER_URL is required") {
		t.Fatalf("missing configuration error = %v", err)
	}
	environment := map[string]string{
		"OIDC_ISSUER_URL":                "https://auth.example.test/application/o/gotth-bb/",
		"AUTHENTIK_CONTROL_TOKEN_FILE":   "/run/secrets/shared",
		"AUTHENTIK_CONTROL_OBJECTS_FILE": "/run/secrets/shared",
		"AUTHENTIK_CONTROL_SOCKET":       "/run/gotth-bb-control/authentik-control.sock",
	}
	lookup := func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	}
	if err := run(context.Background(), lookup); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("shared token/object file error = %v", err)
	}
}
