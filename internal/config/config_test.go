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
	if got.ActivityCursorKeyringFile != "/run/secrets/activity-cursor-keyring" {
		t.Fatalf("ActivityCursorKeyringFile = %q", got.ActivityCursorKeyringFile)
	}
	if got.Abuse.RulesFile != "/run/config/gotth-bb-abuse-rules" || got.Abuse.RequestLimit != 300 || got.Abuse.RequestWindow != time.Minute || got.Abuse.RequestClientCapacity != 4096 || got.Abuse.PublishLimit != 10 || got.Abuse.NewAccountPublishLimit != 3 || got.Abuse.PublishWindow != 10*time.Minute || got.Abuse.NewAccountPeriod != 24*time.Hour {
		t.Fatalf("Abuse = %+v", got.Abuse)
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

func TestLoadAdmitsBetaThirtyMinuteRevalidationInterval(t *testing.T) {
	t.Parallel()

	values := validConfigEnvironment()
	values["AUTH_REVALIDATE_INTERVAL"] = "30m"
	got, err := Load(mapLookup(values))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got.AuthRevalidateInterval != 30*time.Minute {
		t.Fatalf("AuthRevalidateInterval = %s, want 30m", got.AuthRevalidateInterval)
	}
}

func TestParseAuthentikControlSocketRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"authentik-control.sock",
		"/",
		"/run/../run/authentik-control.sock",
		"/run/other.sock",
		"/run/control\nauthentik-control.sock",
		"/" + strings.Repeat("a", 100) + "/authentik-control.sock",
	} {
		if _, err := ParseAuthentikControlSocket(value); err == nil {
			t.Fatalf("ParseAuthentikControlSocket(%q) accepted unsafe path", value)
		}
	}
	if got, err := ParseAuthentikControlSocket("/run/gotth-bb-control/authentik-control.sock"); err != nil || got == "" {
		t.Fatalf("valid control socket = %q, %v", got, err)
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
		"AUTHENTIK_CONTROL_OBJECTS_FILE",
		"AUTHENTIK_CONTROL_SOCKET",
		"INVITATION_FINGERPRINT_KEY_FILE",
		"BOOTSTRAP_ADMIN_SUBJECT",
		"REGISTRATION_URL",
		"REGISTRATION_ENABLED",
		"SESSION_MAX_AGE",
		"SESSION_IDLE_TIMEOUT",
		"AUTH_REVALIDATE_INTERVAL",
		"ACTIVITY_CURSOR_KEYRING_FILE",
		"ABUSE_RULES_FILE",
		"REQUEST_RATE_LIMIT",
		"REQUEST_RATE_WINDOW",
		"REQUEST_RATE_CLIENT_CAPACITY",
		"PUBLISH_RATE_LIMIT",
		"NEW_ACCOUNT_PUBLISH_RATE_LIMIT",
		"PUBLISH_RATE_WINDOW",
		"NEW_ACCOUNT_PERIOD",
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
		{name: "relative activity cursor keyring", change: func(values map[string]string) { values["ACTIVITY_CURSOR_KEYRING_FILE"] = "cursor.json" }},
		{name: "unclean activity cursor keyring", change: func(values map[string]string) { values["ACTIVITY_CURSOR_KEYRING_FILE"] = "/run/secrets/../cursor.json" }},
		{name: "root activity cursor keyring", change: func(values map[string]string) { values["ACTIVITY_CURSOR_KEYRING_FILE"] = "/" }},
		{name: "relative abuse rules", change: func(values map[string]string) { values["ABUSE_RULES_FILE"] = "rules" }},
		{name: "unclean abuse rules", change: func(values map[string]string) { values["ABUSE_RULES_FILE"] = "/run/../rules" }},
		{name: "root abuse rules", change: func(values map[string]string) { values["ABUSE_RULES_FILE"] = "/" }},
		{name: "NUL abuse rules", change: func(values map[string]string) { values["ABUSE_RULES_FILE"] = "/run/rules\x00file" }},
		{name: "long abuse rules", change: func(values map[string]string) { values["ABUSE_RULES_FILE"] = "/" + strings.Repeat("a", 4096) }},
		{name: "signed request limit", change: func(values map[string]string) { values["REQUEST_RATE_LIMIT"] = "+1" }},
		{name: "leading-zero request limit", change: func(values map[string]string) { values["REQUEST_RATE_LIMIT"] = "01" }},
		{name: "zero request limit", change: func(values map[string]string) { values["REQUEST_RATE_LIMIT"] = "0" }},
		{name: "overflow request limit", change: func(values map[string]string) { values["REQUEST_RATE_LIMIT"] = "100001" }},
		{name: "short request window", change: func(values map[string]string) { values["REQUEST_RATE_WINDOW"] = "999ms" }},
		{name: "long request window", change: func(values map[string]string) { values["REQUEST_RATE_WINDOW"] = "25h" }},
		{name: "zero client capacity", change: func(values map[string]string) { values["REQUEST_RATE_CLIENT_CAPACITY"] = "0" }},
		{name: "overflow client capacity", change: func(values map[string]string) { values["REQUEST_RATE_CLIENT_CAPACITY"] = "65537" }},
		{name: "zero publish limit", change: func(values map[string]string) { values["PUBLISH_RATE_LIMIT"] = "0" }},
		{name: "strict limit exceeds established", change: func(values map[string]string) { values["NEW_ACCOUNT_PUBLISH_RATE_LIMIT"] = "11" }},
		{name: "short publish window", change: func(values map[string]string) { values["PUBLISH_RATE_WINDOW"] = "0s" }},
		{name: "short new-account period", change: func(values map[string]string) { values["NEW_ACCOUNT_PERIOD"] = "59s" }},
		{name: "long new-account period", change: func(values map[string]string) { values["NEW_ACCOUNT_PERIOD"] = "721h" }},
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
		"APP_ENV":                         "production",
		"LISTEN_ADDR":                     "127.0.0.1:8080",
		"PUBLIC_BASE_URL":                 "https://alhstudios.com/bb",
		"BASE_PATH":                       "/bb",
		"DATABASE_URL":                    "postgres://gotth:database-password@127.0.0.1/gotth_bb",
		"OIDC_ISSUER_URL":                 "https://auth.example.com/application/o/gotth-bb/",
		"OIDC_CLIENT_ID":                  "gotth-bb",
		"OIDC_CLIENT_SECRET":              "oidc-client-secret",
		"AUTHENTIK_CONTROL_OBJECTS_FILE":  "/run/config/authentik-control-objects.json",
		"AUTHENTIK_CONTROL_SOCKET":        "/run/gotth-bb-control/authentik-control.sock",
		"INVITATION_FINGERPRINT_KEY_FILE": "/run/secrets/invitation-fingerprint-key",
		"BOOTSTRAP_ADMIN_SUBJECT":         "fixed-opaque-subject",
		"REGISTRATION_URL":                "https://auth.example.com/if/flow/gotth-bb-enrollment/",
		"REGISTRATION_ENABLED":            "true",
		"SESSION_COOKIE_NAME":             "",
		"SESSION_MAX_AGE":                 "24h",
		"SESSION_IDLE_TIMEOUT":            "30m",
		"AUTH_REVALIDATE_INTERVAL":        "15m",
		"ACTIVITY_CURSOR_KEYRING_FILE":    "/run/secrets/activity-cursor-keyring",
		"ABUSE_RULES_FILE":                "/run/config/gotth-bb-abuse-rules",
		"REQUEST_RATE_LIMIT":              "300",
		"REQUEST_RATE_WINDOW":             "60s",
		"REQUEST_RATE_CLIENT_CAPACITY":    "4096",
		"PUBLISH_RATE_LIMIT":              "10",
		"NEW_ACCOUNT_PUBLISH_RATE_LIMIT":  "3",
		"PUBLISH_RATE_WINDOW":             "10m",
		"NEW_ACCOUNT_PERIOD":              "24h",
		"LOG_LEVEL":                       "debug",
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
