package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
)

func TestDiscoverOIDCProviderBuildsPinnedConfidentialClient(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/application/o/gotth-bb/.well-known/openid-configuration" || request.URL.RawQuery != "" {
			t.Errorf("discovery request = %s %s", request.Method, request.URL.String())
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{
			"issuer":%q,
			"authorization_endpoint":%q,
			"token_endpoint":%q,
			"jwks_uri":%q,
			"id_token_signing_alg_values_supported":["RS256"],
			"token_endpoint_auth_methods_supported":["client_secret_post","client_secret_basic"]
		}`, serverIssuer(request), serverOrigin(request)+"/authorize", serverOrigin(request)+"/token", serverOrigin(request)+"/jwks")
	}))
	defer server.Close()
	issuer := mustProviderURL(t, server.URL+"/application/o/gotth-bb/")
	redirectURL := "https://forum.example/bb/auth/callback"

	got, err := discoverOIDCProvider(context.Background(), server.Client().Transport, issuer, "gotth-bb", "client-secret", redirectURL)
	if err != nil {
		t.Fatalf("discoverOIDCProvider() returned error: %v", err)
	}
	if got.provider == nil || got.verifier == nil || got.httpClient == nil || requests.Load() != 1 {
		t.Fatalf("provider/verifier nil or discovery requests = %d", requests.Load())
	}
	if got.oauth2Config.ClientID != "gotth-bb" || got.oauth2Config.ClientSecret != "client-secret" ||
		got.oauth2Config.RedirectURL != redirectURL || got.oauth2Config.Endpoint.AuthStyle != oauth2.AuthStyleInHeader ||
		got.oauth2Config.Endpoint.AuthURL != server.URL+"/authorize" || got.oauth2Config.Endpoint.TokenURL != server.URL+"/token" ||
		strings.Join(got.oauth2Config.Scopes, " ") != "openid profile email" {
		t.Fatalf("OAuth2 configuration = %+v", got.oauth2Config)
	}
	formatted := fmt.Sprintf("%v|%+v|%#v", got, got, got)
	if strings.Contains(formatted, "client-secret") {
		t.Fatalf("formatted provider configuration exposed secret: %q", formatted)
	}
}

func TestDiscoverOIDCProviderBuildsPinnedPublicClient(t *testing.T) {
	t.Parallel()

	server := newDiscoveryServer(t, func(request *http.Request) string {
		return discoveryDocument(request, serverIssuer(request), serverOrigin(request)+"/authorize", serverOrigin(request)+"/token", serverOrigin(request)+"/jwks", `["RS256"]`, `["none"]`)
	})
	defer server.Close()
	got, err := discoverOIDCProvider(context.Background(), nil, mustProviderURL(t, server.URL+"/application/o/gotth-bb/"), "gotth-bb", "", "http://127.0.0.1:8080/bb/auth/callback")
	if err != nil || got.oauth2Config.ClientSecret != "" || got.oauth2Config.Endpoint.AuthStyle != oauth2.AuthStyleInParams {
		t.Fatalf("discoverOIDCProvider() = (%+v, %v)", got, err)
	}
}

func TestDiscoverOIDCProviderRejectsTransportFailures(t *testing.T) {
	t.Parallel()

	transportCause := errors.New("transport failed")
	for _, test := range []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "transport error", transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, transportCause })},
		{name: "nil response", transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })},
		{name: "nil body", transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Request: request}, nil
		})},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := discoverOIDCProvider(context.Background(), test.transport, mustProviderURL(t, "https://auth.example/application/o/gotth-bb/"), "gotth-bb", "secret", "https://forum.example/bb/auth/callback")
			if err == nil || got.provider != nil || strings.Contains(err.Error(), transportCause.Error()) {
				t.Fatalf("discoverOIDCProvider() returned unsafe result/error: %+v, %v", got, err)
			}
		})
	}
}

func TestDiscoverOIDCProviderRejectsInvalidCanonicalURLs(t *testing.T) {
	t.Parallel()

	validIssuer := mustProviderURL(t, "https://auth.example/application/o/gotth-bb/")
	for _, test := range []struct {
		name     string
		issuer   url.URL
		redirect string
	}{
		{name: "unsupported issuer scheme", issuer: url.URL{Scheme: "ftp", Host: "auth.example", Path: "/application/o/gotth-bb/"}, redirect: "https://forum.example/bb/auth/callback"},
		{name: "unsupported callback scheme", issuer: validIssuer, redirect: "ftp://forum.example/bb/auth/callback"},
		{name: "noncanonical callback scheme", issuer: validIssuer, redirect: "HTTPS://forum.example/bb/auth/callback"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := discoverOIDCProvider(context.Background(), roundTripperFunc(func(*http.Request) (*http.Response, error) {
				panic("transport must not be called")
			}), test.issuer, "gotth-bb", "secret", test.redirect)
			if err == nil || got.provider != nil {
				t.Fatalf("discoverOIDCProvider() returned an unsafe result: %+v, %v", got, err)
			}
		})
	}
}

func TestDiscoverOIDCProviderPreservesCancellationDuringRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, errors.New("transport stopped")
	})
	got, err := discoverOIDCProvider(ctx, transport, mustProviderURL(t, "https://auth.example/application/o/gotth-bb/"), "gotth-bb", "secret", "https://forum.example/bb/auth/callback")
	if !errors.Is(err, context.Canceled) || got.provider != nil {
		t.Fatalf("discoverOIDCProvider() = (%+v, %v), want context cancellation", got, err)
	}
}

func TestDiscoverOIDCProviderRejectsUnsafeDiscovery(t *testing.T) {
	t.Parallel()

	secretMarker := "must-not-appear"
	for _, test := range []struct {
		name       string
		handler    func(http.ResponseWriter, *http.Request, *atomic.Int32)
		clientID   string
		secret     string
		redirect   string
		wantFollow int32
	}{
		{name: "non-OK body is redacted", handler: func(writer http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
			http.Error(writer, secretMarker, http.StatusBadGateway)
		}, clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback"},
		{name: "malformed JSON", handler: func(writer http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"issuer":`))
		}, clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback"},
		{name: "oversize body", handler: func(writer http.ResponseWriter, request *http.Request, _ *atomic.Int32) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(discoveryDocument(request, serverIssuer(request), serverOrigin(request)+"/authorize", serverOrigin(request)+"/token", serverOrigin(request)+"/jwks", `["RS256"]`, `["client_secret_basic"]`)))
			_, _ = writer.Write([]byte(strings.Repeat(" ", oidcHTTPResponseMaxBytes)))
		}, clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback"},
		{name: "redirect", handler: func(writer http.ResponseWriter, request *http.Request, follows *atomic.Int32) {
			if request.URL.Path == "/followed" {
				follows.Add(1)
				writer.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(writer, request, "/followed", http.StatusFound)
		}, clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var follows atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				test.handler(writer, request, &follows)
			}))
			defer server.Close()
			got, err := discoverOIDCProvider(context.Background(), server.Client().Transport, mustProviderURL(t, server.URL+"/application/o/gotth-bb/"), test.clientID, test.secret, test.redirect)
			if err == nil || got.provider != nil || got.verifier != nil || got.httpClient != nil || got.oauth2Config.ClientID != "" {
				t.Fatalf("discoverOIDCProvider() returned a nonzero result or no error: %v", err)
			}
			if strings.Contains(err.Error(), secretMarker) || follows.Load() != test.wantFollow {
				t.Fatalf("error leaked response or redirect was followed: %q, follows=%d", err, follows.Load())
			}
		})
	}
}

func TestDiscoverOIDCProviderRejectsInvalidMetadataAndInputs(t *testing.T) {
	t.Parallel()

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name      string
		ctx       context.Context
		clientID  string
		secret    string
		redirect  string
		mutateDoc func(*http.Request, *discoveryFields)
	}{
		{name: "nil context", clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback"},
		{name: "canceled context", ctx: canceledContext, clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback"},
		{name: "empty client ID", ctx: context.Background(), secret: "secret", redirect: "https://forum.example/bb/auth/callback"},
		{name: "invalid redirect", ctx: context.Background(), clientID: "gotth-bb", secret: "secret", redirect: "/bb/auth/callback"},
		{name: "issuer mismatch", ctx: context.Background(), clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback", mutateDoc: func(_ *http.Request, fields *discoveryFields) { fields.issuer += "wrong" }},
		{name: "off-origin authorization endpoint", ctx: context.Background(), clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback", mutateDoc: func(_ *http.Request, fields *discoveryFields) { fields.authURL = "https://attacker.invalid/authorize" }},
		{name: "token endpoint query", ctx: context.Background(), clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback", mutateDoc: func(_ *http.Request, fields *discoveryFields) { fields.tokenURL += "?secret=value" }},
		{name: "JWKS credentials", ctx: context.Background(), clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback", mutateDoc: func(request *http.Request, fields *discoveryFields) {
			fields.jwksURL = request.URL.Scheme + "://user:pass@" + request.Host + "/jwks"
		}},
		{name: "unsupported signing algorithm", ctx: context.Background(), clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback", mutateDoc: func(_ *http.Request, fields *discoveryFields) { fields.algorithms = `["HS256","none"]` }},
		{name: "confidential method unavailable", ctx: context.Background(), clientID: "gotth-bb", secret: "secret", redirect: "https://forum.example/bb/auth/callback", mutateDoc: func(_ *http.Request, fields *discoveryFields) { fields.authMethods = `["client_secret_post"]` }},
		{name: "public method unavailable", ctx: context.Background(), clientID: "gotth-bb", redirect: "https://forum.example/bb/auth/callback", mutateDoc: func(_ *http.Request, fields *discoveryFields) { fields.authMethods = `["client_secret_basic"]` }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newDiscoveryServer(t, func(request *http.Request) string {
				fields := discoveryFields{
					issuer: serverIssuer(request), authURL: serverOrigin(request) + "/authorize", tokenURL: serverOrigin(request) + "/token", jwksURL: serverOrigin(request) + "/jwks",
					algorithms: `["RS256"]`, authMethods: `["client_secret_basic","none"]`,
				}
				if test.mutateDoc != nil {
					test.mutateDoc(request, &fields)
				}
				return discoveryDocument(request, fields.issuer, fields.authURL, fields.tokenURL, fields.jwksURL, fields.algorithms, fields.authMethods)
			})
			defer server.Close()
			got, err := discoverOIDCProvider(test.ctx, server.Client().Transport, mustProviderURL(t, server.URL+"/application/o/gotth-bb/"), test.clientID, test.secret, test.redirect)
			if err == nil || got.provider != nil || got.verifier != nil || got.httpClient != nil || got.oauth2Config.ClientID != "" {
				t.Fatalf("discoverOIDCProvider() returned a nonzero result or no error: %v", err)
			}
		})
	}
}

type discoveryFields struct {
	issuer, authURL, tokenURL, jwksURL, algorithms, authMethods string
}

func newDiscoveryServer(t *testing.T, document func(*http.Request) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(document(request)))
	}))
}

func discoveryDocument(_ *http.Request, issuer, authURL, tokenURL, jwksURL, algorithms, authMethods string) string {
	return fmt.Sprintf(`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"id_token_signing_alg_values_supported":%s,"token_endpoint_auth_methods_supported":%s}`, issuer, authURL, tokenURL, jwksURL, algorithms, authMethods)
}

func serverIssuer(request *http.Request) string {
	return serverOrigin(request) + "/application/o/gotth-bb/"
}

func serverOrigin(request *http.Request) string {
	return "http://" + request.Host
}

func mustProviderURL(t *testing.T, raw string) url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return *parsed
}
