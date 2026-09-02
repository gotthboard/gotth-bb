package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	values := validConfigEnvironment()
	got, err := Load(mapLookup(values))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if got.Environment != EnvironmentProduction {
		t.Fatalf("Environment = %q", got.Environment)
	}
	if got.ListenAddr.String() != "127.0.0.1:8080" {
		t.Fatalf("ListenAddr = %q", got.ListenAddr)
	}
	if got.PublicBaseURL.String() != "https://alhstudios.com/bb" || got.BasePath != "/bb" {
		t.Fatalf("public location = %q and %q", got.PublicBaseURL.String(), got.BasePath)
	}
	if got.databaseURL.value != values["DATABASE_URL"] {
		t.Fatal("DatabaseURL did not preserve configured secret")
	}
	if got.OIDCIssuerURL.String() != values["OIDC_ISSUER_URL"] || got.OIDCClientID != "gotth-bb" {
		t.Fatalf("OIDC identity = %q and %q", got.OIDCIssuerURL.String(), got.OIDCClientID)
	}
	if got.oidcClientSecret.value != values["OIDC_CLIENT_SECRET"] {
		t.Fatal("OIDCClientSecret did not preserve configured secret")
	}
	if got.BootstrapAdminSubject != values["BOOTSTRAP_ADMIN_SUBJECT"] || got.RegistrationURL.String() != values["REGISTRATION_URL"] || !got.RegistrationEnabled {
		t.Fatalf("setup identity = (%q, %q)", got.BootstrapAdminSubject, got.RegistrationURL.String())
	}
	if got.SessionCookieName != "gotth_bb_session" {
		t.Fatalf("SessionCookieName = %q", got.SessionCookieName)
	}
	if got.SessionMaxAge != 24*time.Hour || got.SessionIdleTimeout != 30*time.Minute || got.AuthRevalidateInterval != 15*time.Minute {
		t.Fatalf("session durations = %s, %s, %s", got.SessionMaxAge, got.SessionIdleTimeout, got.AuthRevalidateInterval)
	}
	if got.LogLevel != slog.LevelDebug {
		t.Fatalf("LogLevel = %s", got.LogLevel)
	}
}

func TestLoadUsesOptionalDefaultsAndAllowsDevelopmentPublicClient(t *testing.T) {
	t.Parallel()

	values := validConfigEnvironment()
	values["APP_ENV"] = "development"
	values["LISTEN_ADDR"] = "0.0.0.0:8080"
	values["PUBLIC_BASE_URL"] = "http://127.0.0.1:8080/bb"
	values["OIDC_ISSUER_URL"] = "http://127.0.0.1:9000/application/o/gotth-bb/"
	values["REGISTRATION_URL"] = "http://127.0.0.1:9000/if/flow/gotth-bb-enrollment/"
	delete(values, "OIDC_CLIENT_SECRET")
	delete(values, "SESSION_COOKIE_NAME")
	delete(values, "LOG_LEVEL")

	got, err := Load(mapLookup(values))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got.oidcClientSecret.value != "" {
		t.Fatal("development public client secret must remain empty")
	}
	if got.SessionCookieName != "gotth_bb_session" || got.LogLevel != slog.LevelInfo {
		t.Fatalf("defaults = cookie %q, log %s", got.SessionCookieName, got.LogLevel)
	}
}

func TestLoadRejectsMissingRequiredSettings(t *testing.T) {
	t.Parallel()

	required := []string{
		"APP_ENV",
		"LISTEN_ADDR",
		"PUBLIC_BASE_URL",
		"BASE_PATH",
		"DATABASE_URL",
		"OIDC_ISSUER_URL",
		"OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET",
		"BOOTSTRAP_ADMIN_SUBJECT",
		"REGISTRATION_URL",
		"REGISTRATION_ENABLED",
		"SESSION_MAX_AGE",
		"SESSION_IDLE_TIMEOUT",
		"AUTH_REVALIDATE_INTERVAL",
	}
	for _, name := range required {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			values := validConfigEnvironment()
			delete(values, name)
			if got, err := Load(mapLookup(values)); err == nil {
				t.Fatalf("Load() without %s = %+v, want error", name, got)
			}
		})
	}
}

func TestLoadRejectsInvalidRelationships(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "nil lookup", change: nil},
		{name: "invalid environment", change: func(values map[string]string) { values["APP_ENV"] = "prod" }},
		{name: "invalid base path", change: func(values map[string]string) { values["BASE_PATH"] = "/bb/" }},
		{name: "invalid listen address", change: func(values map[string]string) { values["LISTEN_ADDR"] = "localhost:8080" }},
		{name: "invalid public base URL", change: func(values map[string]string) { values["PUBLIC_BASE_URL"] = "http://alhstudios.com/bb" }},
		{name: "empty database URL", change: func(values map[string]string) { values["DATABASE_URL"] = "" }},
		{name: "invalid OIDC issuer", change: func(values map[string]string) { values["OIDC_ISSUER_URL"] = "https://auth.example.com/" }},
		{name: "empty OIDC client ID", change: func(values map[string]string) { values["OIDC_CLIENT_ID"] = "" }},
		{name: "empty production OIDC secret", change: func(values map[string]string) { values["OIDC_CLIENT_SECRET"] = "" }},
		{name: "invalid bootstrap administrator subject", change: func(values map[string]string) { values["BOOTSTRAP_ADMIN_SUBJECT"] = "bad\nsubject" }},
		{name: "invalid registration URL", change: func(values map[string]string) {
			values["REGISTRATION_URL"] = "https://other.example.com/if/flow/gotth-bb-enrollment/"
		}},
		{name: "invalid registration enabled", change: func(values map[string]string) { values["REGISTRATION_ENABLED"] = "yes" }},
		{name: "invalid session maximum", change: func(values map[string]string) { values["SESSION_MAX_AGE"] = "forever" }},
		{name: "invalid session idle timeout", change: func(values map[string]string) { values["SESSION_IDLE_TIMEOUT"] = "forever" }},
		{name: "invalid auth revalidation interval", change: func(values map[string]string) { values["AUTH_REVALIDATE_INTERVAL"] = "forever" }},
		{name: "idle exceeds maximum", change: func(values map[string]string) { values["SESSION_IDLE_TIMEOUT"] = "25h" }},
		{name: "revalidation exceeds maximum", change: func(values map[string]string) { values["AUTH_REVALIDATE_INTERVAL"] = "25h" }},
		{name: "invalid cookie name", change: func(values map[string]string) { values["SESSION_COOKIE_NAME"] = "session name" }},
		{name: "invalid log level", change: func(values map[string]string) { values["LOG_LEVEL"] = "warning" }},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if test.change == nil {
				if got, err := Load(nil); err == nil {
					t.Fatalf("Load(nil) = %+v, want error", got)
				}
				return
			}
			values := validConfigEnvironment()
			test.change(values)
			if got, err := Load(mapLookup(values)); err == nil {
				t.Fatalf("Load() = %+v, want error", got)
			}
		})
	}
}

func TestLoadRedactsMalformedSetting(t *testing.T) {
	t.Parallel()

	const secret = "do-not-log-loader-input"
	values := validConfigEnvironment()
	values["OIDC_ISSUER_URL"] = "https://" + secret + "%zz.example.com/application/o/gotth-bb/"
	_, err := Load(mapLookup(values))
	if err == nil {
		t.Fatal("Load() accepted malformed issuer")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error exposed configured value: %q", err)
	}
}

func validConfigEnvironment() map[string]string {
	return map[string]string{
		"APP_ENV":                  "production",
		"LISTEN_ADDR":              "127.0.0.1:8080",
		"PUBLIC_BASE_URL":          "https://alhstudios.com/bb",
		"BASE_PATH":                "/bb",
		"DATABASE_URL":             "postgres://gotth:database-password@127.0.0.1/gotth_bb",
		"OIDC_ISSUER_URL":          "https://auth.example.com/application/o/gotth-bb/",
		"OIDC_CLIENT_ID":           "gotth-bb",
		"OIDC_CLIENT_SECRET":       "oidc-client-secret",
		"BOOTSTRAP_ADMIN_SUBJECT":  "fixed-opaque-subject",
		"REGISTRATION_URL":         "https://auth.example.com/if/flow/gotth-bb-enrollment/",
		"REGISTRATION_ENABLED":     "true",
		"SESSION_COOKIE_NAME":      "",
		"SESSION_MAX_AGE":          "24h",
		"SESSION_IDLE_TIMEOUT":     "30m",
		"AUTH_REVALIDATE_INTERVAL": "15m",
		"LOG_LEVEL":                "debug",
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
