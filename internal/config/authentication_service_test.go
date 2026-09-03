package config

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewAuthenticationServicePassesValidatedConfigurationWithoutExposingSecrets(t *testing.T) {
	t.Parallel()

	configured, err := Load(mapLookup(validConfigEnvironment()))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	issuer := configured.OIDCIssuerURL.String()
	requests := 0
	transport := authenticationRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != issuer+".well-known/openid-configuration" {
			t.Fatalf("discovery URL = %q", request.URL.String())
		}
		metadata := fmt.Sprintf(`{
			"issuer":%q,
			"authorization_endpoint":%q,
			"token_endpoint":%q,
			"jwks_uri":%q,
			"response_types_supported":["code"],
			"subject_types_supported":["public"],
			"id_token_signing_alg_values_supported":["RS256"],
			"token_endpoint_auth_methods_supported":["client_secret_basic"]
		}`, issuer, issuer+"authorize", issuer+"token", issuer+"jwks")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(metadata)),
			Request:    request,
		}, nil
	})
	service, err := configured.NewAuthenticationService(
		context.Background(),
		transport,
		configAuthenticationDatabase{},
		strings.NewReader("unused-construction-entropy"),
		time.Now,
		func(returnPath string) (string, error) { return returnPath, nil },
	)
	if err != nil || service == nil || requests != 1 {
		t.Fatalf("NewAuthenticationService() = (%v, %v), discovery requests %d", service, err, requests)
	}
	formatted := fmt.Sprintf("%v %+v %#v", service, service, service)
	for _, secret := range []string{validConfigEnvironment()["OIDC_CLIENT_SECRET"], validConfigEnvironment()["DATABASE_URL"]} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("formatted service exposed secret: %q", formatted)
		}
	}
}

func TestNewAuthenticationServiceRejectsInvalidDependenciesBeforeDiscovery(t *testing.T) {
	t.Parallel()

	transport := authenticationRoundTripper(func(*http.Request) (*http.Response, error) {
		panic("discovery must not run")
	})
	validConfigured, err := Load(mapLookup(validConfigEnvironment()))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	validDatabase := configAuthenticationDatabase{}
	validEntropy := strings.NewReader("unused-construction-entropy")
	validClock := time.Now
	validReturnPath := func(returnPath string) (string, error) { return returnPath, nil }
	invalidPublicURL := validConfigured
	invalidPublicURL.PublicBaseURL.Path = "/wrong"
	encodedPublicURL := validConfigured
	encodedPublicURL.PublicBaseURL.RawPath = "/%62b"
	invalidIssuer := validConfigured
	invalidIssuer.OIDCIssuerURL.Path = "/wrong"
	missingClientID := validConfigured
	missingClientID.OIDCClientID = ""
	missingProductionSecret := validConfigured
	missingProductionSecret.oidcClientSecret = secret{}
	for _, test := range []struct {
		name       string
		configured Config
		ctx        context.Context
		database   auth.SessionDatabase
		entropy    io.Reader
		clock      func() time.Time
		validator  func(string) (string, error)
	}{
		{name: "configuration", ctx: context.Background(), database: validDatabase, entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "public URL", configured: invalidPublicURL, ctx: context.Background(), database: validDatabase, entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "encoded public URL", configured: encodedPublicURL, ctx: context.Background(), database: validDatabase, entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "issuer", configured: invalidIssuer, ctx: context.Background(), database: validDatabase, entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "client ID", configured: missingClientID, ctx: context.Background(), database: validDatabase, entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "production secret", configured: missingProductionSecret, ctx: context.Background(), database: validDatabase, entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "context", configured: validConfigured, database: validDatabase, entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "database", configured: validConfigured, ctx: context.Background(), entropy: validEntropy, clock: validClock, validator: validReturnPath},
		{name: "entropy", configured: validConfigured, ctx: context.Background(), database: validDatabase, clock: validClock, validator: validReturnPath},
		{name: "clock", configured: validConfigured, ctx: context.Background(), database: validDatabase, entropy: validEntropy, validator: validReturnPath},
		{name: "validator", configured: validConfigured, ctx: context.Background(), database: validDatabase, entropy: validEntropy, clock: validClock},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if service, err := test.configured.NewAuthenticationService(test.ctx, transport, test.database, test.entropy, test.clock, test.validator); err == nil || service != nil {
				t.Fatalf("NewAuthenticationService() = (%v, %v), want nil/error", service, err)
			}
		})
	}
}

type authenticationRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip authenticationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type configAuthenticationDatabase struct{}

func (configAuthenticationDatabase) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("database must not execute during construction")
}

func (configAuthenticationDatabase) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("database must not query during construction")
}

func (configAuthenticationDatabase) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("database must not query during construction")
}

func (configAuthenticationDatabase) Begin(context.Context) (pgx.Tx, error) {
	panic("database must not begin during construction")
}
