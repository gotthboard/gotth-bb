package config

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gotthboard/gotth-bb/internal/auth"
)

// NewAuthenticationService crosses the retained OIDC client-secret boundary
// only into the concrete authentication service. It reconstructs and validates
// the exact callback from immutable public configuration; callers receive no
// general secret accessor.
//
// Complexity: local validation and callback construction scan bounded
// configuration strings in O(n), Omega(1) time and O(n), Omega(1) auxiliary
// space. Delegated service construction performs one bounded OIDC discovery
// request with the complexity documented by auth.NewService. PostgreSQL is
// retained but not queried, and no work is retried or detached.
func (configured Config) NewAuthenticationService(
	ctx context.Context,
	transport http.RoundTripper,
	database auth.SessionDatabase,
	entropy io.Reader,
	clock func() time.Time,
	validateReturnPath func(string) (string, error),
) (*auth.Service, error) {
	validatedEnvironment, err := ParseEnvironment(string(configured.Environment))
	if err != nil {
		return nil, fmt.Errorf("authentication environment is invalid")
	}
	production := validatedEnvironment == EnvironmentProduction
	publicBaseURL, err := ParsePublicBaseURL(configured.PublicBaseURL.String(), configured.BasePath, production)
	if err != nil || publicBaseURL.RawPath != "" {
		return nil, fmt.Errorf("authentication public URL is invalid")
	}
	issuerURL, err := ParseOIDCIssuerURL(configured.OIDCIssuerURL.String(), production)
	if err != nil {
		return nil, fmt.Errorf("authentication issuer URL is invalid")
	}
	if configured.OIDCClientID == "" {
		return nil, fmt.Errorf("authentication client ID is required")
	}
	if production && configured.oidcClientSecret.value == "" {
		return nil, fmt.Errorf("authentication client secret is required in production")
	}
	callbackURL := publicBaseURL
	callbackURL.Path += "/auth/callback"
	callbackURL.RawPath = ""
	return auth.NewService(
		ctx,
		transport,
		issuerURL,
		configured.OIDCClientID,
		configured.oidcClientSecret.value,
		callbackURL.String(),
		database,
		entropy,
		clock,
		configured.SessionMaxAge,
		configured.SessionIdleTimeout,
		configured.AuthRevalidateInterval,
		validateReturnPath,
	)
}
