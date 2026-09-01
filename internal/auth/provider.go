package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	oidcHTTPTimeout          = 10 * time.Second
	oidcHTTPResponseMaxBytes = 512 << 10
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type discoveredOIDCProvider struct {
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	httpClient   *http.Client
	oauth2Config oauth2.Config
}

// Format prevents diagnostic formatting from traversing the OAuth2 client
// secret retained in the provider configuration.
func (discoveredOIDCProvider) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED OIDC PROVIDER]")
}

// discoverOIDCProvider performs exact-issuer discovery through a bounded,
// redirect-refusing client, validates every network endpoint against the
// configured Authentik origin, pins supported signature algorithms and a token
// authentication style, and constructs the OAuth2 client and ID-token verifier.
//
// Complexity: for a discovery document of n bytes (n <= 512 KiB), time and
// auxiliary space are O(n), Omega(1). Network work is one bounded discovery
// request with a ten-second client timeout; later token/JWKS requests retain the
// same response bound, timeout, redirect refusal, and origin restriction.
func discoverOIDCProvider(
	ctx context.Context,
	baseTransport http.RoundTripper,
	issuerURL url.URL,
	clientID string,
	clientSecret string,
	redirectURL string,
) (discoveredOIDCProvider, error) {
	if ctx == nil {
		return discoveredOIDCProvider{}, fmt.Errorf("OIDC discovery context is required")
	}
	if err := ctx.Err(); err != nil {
		return discoveredOIDCProvider{}, fmt.Errorf("discover OIDC provider: %w", err)
	}
	if clientID == "" {
		return discoveredOIDCProvider{}, fmt.Errorf("OIDC client ID is required")
	}
	validateURL := func(name, raw string, sameOrigin bool) (url.URL, error) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Path == "" {
			return url.URL{}, fmt.Errorf("%s is not an absolute hierarchical URL", name)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return url.URL{}, fmt.Errorf("%s must use HTTP or HTTPS", name)
		}
		if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			return url.URL{}, fmt.Errorf("%s contains credentials, a query, or a fragment", name)
		}
		if parsed.String() != raw {
			return url.URL{}, fmt.Errorf("%s is not canonically encoded", name)
		}
		if sameOrigin && (parsed.Scheme != issuerURL.Scheme || parsed.Host != issuerURL.Host) {
			return url.URL{}, fmt.Errorf("%s escapes the configured issuer origin", name)
		}
		return *parsed, nil
	}
	issuer := issuerURL.String()
	if _, err := validateURL("OIDC issuer", issuer, false); err != nil {
		return discoveredOIDCProvider{}, err
	}
	if _, err := validateURL("OIDC callback", redirectURL, false); err != nil {
		return discoveredOIDCProvider{}, err
	}
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	hardenedTransport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request == nil || request.URL == nil || request.URL.Scheme != issuerURL.Scheme || request.URL.Host != issuerURL.Host {
			return nil, fmt.Errorf("OIDC request escaped the configured issuer origin")
		}
		response, err := baseTransport.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		if response == nil || response.Body == nil {
			return nil, fmt.Errorf("OIDC transport returned an invalid response")
		}
		response.Body = http.MaxBytesReader(nil, response.Body, oidcHTTPResponseMaxBytes)
		return response, nil
	})
	client := &http.Client{
		Transport: hardenedTransport,
		Timeout:   oidcHTTPTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("OIDC redirects are not allowed")
		},
	}
	providerContext := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(providerContext, issuer)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return discoveredOIDCProvider{}, fmt.Errorf("discover OIDC provider: %w", contextError)
		}
		return discoveredOIDCProvider{}, fmt.Errorf("OIDC provider discovery failed")
	}
	var metadata struct {
		JWKSURL          string   `json:"jwks_uri"`
		Algorithms       []string `json:"id_token_signing_alg_values_supported"`
		TokenAuthMethods []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return discoveredOIDCProvider{}, fmt.Errorf("OIDC provider metadata decoding failed")
	}
	endpoint := provider.Endpoint()
	if _, err := validateURL("OIDC authorization endpoint", endpoint.AuthURL, true); err != nil {
		return discoveredOIDCProvider{}, err
	}
	if _, err := validateURL("OIDC token endpoint", endpoint.TokenURL, true); err != nil {
		return discoveredOIDCProvider{}, err
	}
	if _, err := validateURL("OIDC JWKS endpoint", metadata.JWKSURL, true); err != nil {
		return discoveredOIDCProvider{}, err
	}
	supportedAlgorithm := false
	for _, algorithm := range metadata.Algorithms {
		switch algorithm {
		case oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512,
			oidc.PS256, oidc.PS384, oidc.PS512, oidc.EdDSA:
			supportedAlgorithm = true
		}
	}
	if !supportedAlgorithm {
		return discoveredOIDCProvider{}, fmt.Errorf("OIDC provider advertises no supported ID-token signing algorithm")
	}
	authStyle := oauth2.AuthStyleInParams
	if clientSecret != "" {
		authStyle = oauth2.AuthStyleInHeader
		if !slices.Contains(metadata.TokenAuthMethods, "client_secret_basic") {
			return discoveredOIDCProvider{}, fmt.Errorf("OIDC provider does not advertise client_secret_basic")
		}
	} else if !slices.Contains(metadata.TokenAuthMethods, "none") {
		return discoveredOIDCProvider{}, fmt.Errorf("OIDC provider does not advertise public-client token authentication")
	}
	endpoint.AuthStyle = authStyle
	oauthConfig := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     endpoint,
		RedirectURL:  redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})
	return discoveredOIDCProvider{provider: provider, verifier: verifier, httpClient: client, oauth2Config: oauthConfig}, nil
}
