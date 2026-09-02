package config

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"time"
)

// LookupEnv returns one exact environment value and whether it was present.
type LookupEnv func(string) (string, bool)

// Config is the loaded immutable startup contract. The PostgreSQL driver must
// still validate the opaque database connection string before serving.
type Config struct {
	Environment            Environment
	ListenAddr             netip.AddrPort
	PublicBaseURL          url.URL
	BasePath               string
	databaseURL            secret
	OIDCIssuerURL          url.URL
	OIDCClientID           string
	oidcClientSecret       secret
	BootstrapAdminSubject  string
	RegistrationURL        url.URL
	RegistrationEnabled    bool
	SessionCookieName      string
	SessionMaxAge          time.Duration
	SessionIdleTimeout     time.Duration
	AuthRevalidateInterval time.Duration
	LogLevel               slog.Level
}

// Format prevents recursive fmt traversal from exposing unexported secret
// fields when an entire configuration is logged or diagnosed accidentally.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary space O(1),
// Omega(1), and tight Theta(1). The fixed marker has constant length and the
// bounded write is delegated directly to the formatter state.
func (configured Config) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED CONFIG]")
}

// Load reads every startup setting once, validates non-database values and
// cross-field invariants, and returns no partial configuration on failure.
//
// Complexity: for p bytes passed to scanning parsers and s bytes in opaque
// database/client values, valid-input time is tight Theta(p+1), with O(p+1)
// worst-case and Omega(1) on an early missing-value failure; opaque values are
// only length-checked. Auxiliary allocated space is O(p), Omega(1). The fixed
// result headers retain O(p+s) caller-owned bytes by reference without copying;
// delegated URL/path parsers may additionally retain or allocate O(p) bytes.
func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("configuration lookup is required")
	}
	required := func(name string) (string, error) {
		value, ok := lookup(name)
		if !ok {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}

	environmentRaw, err := required("APP_ENV")
	if err != nil {
		return Config{}, err
	}
	environment, err := ParseEnvironment(environmentRaw)
	if err != nil {
		return Config{}, err
	}

	basePathRaw, err := required("BASE_PATH")
	if err != nil {
		return Config{}, err
	}
	basePath, err := ParseBasePath(basePathRaw)
	if err != nil {
		return Config{}, err
	}

	listenRaw, err := required("LISTEN_ADDR")
	if err != nil {
		return Config{}, err
	}
	listenAddr, err := ParseListenAddr(listenRaw, environment)
	if err != nil {
		return Config{}, err
	}

	publicBaseRaw, err := required("PUBLIC_BASE_URL")
	if err != nil {
		return Config{}, err
	}
	publicBaseURL, err := ParsePublicBaseURL(publicBaseRaw, basePath, environment == EnvironmentProduction)
	if err != nil {
		return Config{}, err
	}

	databaseURL, err := required("DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must not be empty")
	}

	oidcIssuerRaw, err := required("OIDC_ISSUER_URL")
	if err != nil {
		return Config{}, err
	}
	oidcIssuerURL, err := ParseOIDCIssuerURL(oidcIssuerRaw, environment == EnvironmentProduction)
	if err != nil {
		return Config{}, err
	}

	oidcClientID, err := required("OIDC_CLIENT_ID")
	if err != nil {
		return Config{}, err
	}
	if oidcClientID == "" {
		return Config{}, fmt.Errorf("OIDC_CLIENT_ID must not be empty")
	}
	oidcClientSecret, secretPresent := lookup("OIDC_CLIENT_SECRET")
	if environment == EnvironmentProduction && (!secretPresent || oidcClientSecret == "") {
		return Config{}, fmt.Errorf("OIDC_CLIENT_SECRET is required in production")
	}
	bootstrapAdminSubjectRaw, err := required("BOOTSTRAP_ADMIN_SUBJECT")
	if err != nil {
		return Config{}, err
	}
	bootstrapAdminSubject, err := ParseBootstrapAdministratorSubject(bootstrapAdminSubjectRaw)
	if err != nil {
		return Config{}, err
	}
	registrationRaw, err := required("REGISTRATION_URL")
	if err != nil {
		return Config{}, err
	}
	registrationURL, err := ParseRegistrationURL(registrationRaw, oidcIssuerURL, environment == EnvironmentProduction)
	if err != nil {
		return Config{}, err
	}
	registrationEnabledRaw, err := required("REGISTRATION_ENABLED")
	if err != nil {
		return Config{}, err
	}
	registrationEnabled := false
	switch registrationEnabledRaw {
	case "true":
		registrationEnabled = true
	case "false":
	default:
		return Config{}, fmt.Errorf("REGISTRATION_ENABLED must be exactly true or false")
	}

	sessionMaxAgeRaw, err := required("SESSION_MAX_AGE")
	if err != nil {
		return Config{}, err
	}
	sessionMaxAge, err := parsePositiveDuration("SESSION_MAX_AGE", sessionMaxAgeRaw)
	if err != nil {
		return Config{}, err
	}
	sessionIdleRaw, err := required("SESSION_IDLE_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	sessionIdleTimeout, err := parsePositiveDuration("SESSION_IDLE_TIMEOUT", sessionIdleRaw)
	if err != nil {
		return Config{}, err
	}
	authRevalidateRaw, err := required("AUTH_REVALIDATE_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	authRevalidateInterval, err := parsePositiveDuration("AUTH_REVALIDATE_INTERVAL", authRevalidateRaw)
	if err != nil {
		return Config{}, err
	}
	if sessionIdleTimeout > sessionMaxAge {
		return Config{}, fmt.Errorf("SESSION_IDLE_TIMEOUT must not exceed SESSION_MAX_AGE")
	}
	if authRevalidateInterval > sessionMaxAge {
		return Config{}, fmt.Errorf("AUTH_REVALIDATE_INTERVAL must not exceed SESSION_MAX_AGE")
	}

	sessionCookieRaw, _ := lookup("SESSION_COOKIE_NAME")
	sessionCookieName, err := ParseSessionCookieName(sessionCookieRaw)
	if err != nil {
		return Config{}, err
	}
	logLevelRaw, _ := lookup("LOG_LEVEL")
	logLevel, err := ParseLogLevel(logLevelRaw)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Environment:            environment,
		ListenAddr:             listenAddr,
		PublicBaseURL:          publicBaseURL,
		BasePath:               basePath,
		databaseURL:            secret{value: databaseURL},
		OIDCIssuerURL:          oidcIssuerURL,
		OIDCClientID:           oidcClientID,
		oidcClientSecret:       secret{value: oidcClientSecret},
		BootstrapAdminSubject:  bootstrapAdminSubject,
		RegistrationURL:        registrationURL,
		RegistrationEnabled:    registrationEnabled,
		SessionCookieName:      sessionCookieName,
		SessionMaxAge:          sessionMaxAge,
		SessionIdleTimeout:     sessionIdleTimeout,
		AuthRevalidateInterval: authRevalidateInterval,
		LogLevel:               logLevel,
	}, nil
}
