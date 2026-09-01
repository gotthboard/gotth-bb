package auth

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func TestInitialAuthorizationURLContainsExactOIDCAndPKCEParameters(t *testing.T) {
	t.Parallel()

	material, err := generateLoginMaterial(bytes.NewReader(sequentialBytes(96)))
	if err != nil {
		t.Fatalf("generateLoginMaterial() returned error: %v", err)
	}
	provider := discoveredOIDCProvider{
		provider:   new(oidc.Provider),
		verifier:   new(oidc.IDTokenVerifier),
		httpClient: new(http.Client),
		oauth2Config: oauth2.Config{
			ClientID:    "gotth-bb",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://auth.example/application/o/authorize/"},
			RedirectURL: "https://forum.example/bb/auth/callback",
			Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
		},
	}

	raw, err := provider.initialAuthorizationURL(material)
	if err != nil {
		t.Fatalf("initialAuthorizationURL() returned error: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse() returned error: %v", err)
	}
	wantChallenge := oauth2.S256ChallengeFromVerifier(material.pkceVerifier)
	want := url.Values{
		"response_type":         {"code"},
		"client_id":             {"gotth-bb"},
		"redirect_uri":          {"https://forum.example/bb/auth/callback"},
		"scope":                 {"openid profile email"},
		"state":                 {material.state},
		"nonce":                 {material.nonce},
		"code_challenge":        {wantChallenge},
		"code_challenge_method": {"S256"},
	}
	if parsed.Scheme != "https" || parsed.Host != "auth.example" || parsed.Path != "/application/o/authorize/" || parsed.Fragment != "" || parsed.Query().Encode() != want.Encode() {
		t.Fatalf("authorization URL = %q", raw)
	}
	if strings.Contains(raw, material.pkceVerifier) || strings.Contains(raw, "client_secret") {
		t.Fatalf("authorization URL exposed verifier or client secret: %q", raw)
	}
}

func TestInitialAuthorizationURLRejectsInvalidProviderOrMaterial(t *testing.T) {
	t.Parallel()

	validMaterial, err := generateLoginMaterial(bytes.NewReader(sequentialBytes(96)))
	if err != nil {
		t.Fatalf("generateLoginMaterial() returned error: %v", err)
	}
	validProvider := discoveredOIDCProvider{
		provider:   new(oidc.Provider),
		verifier:   new(oidc.IDTokenVerifier),
		httpClient: new(http.Client),
		oauth2Config: oauth2.Config{
			ClientID:    "gotth-bb",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://auth.example/authorize"},
			RedirectURL: "https://forum.example/bb/auth/callback",
			Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
		},
	}
	invalidEncoding := strings.Repeat("!", 43)
	short := base64.RawURLEncoding.EncodeToString(make([]byte, 31))
	for _, test := range []struct {
		name     string
		provider discoveredOIDCProvider
		material loginMaterial
	}{
		{name: "zero provider", material: validMaterial},
		{name: "missing verifier", provider: discoveredOIDCProvider{provider: new(oidc.Provider), oauth2Config: validProvider.oauth2Config}, material: validMaterial},
		{name: "missing OAuth client", provider: discoveredOIDCProvider{provider: new(oidc.Provider), verifier: new(oidc.IDTokenVerifier)}, material: validMaterial},
		{name: "invalid state encoding", provider: validProvider, material: loginMaterial{state: invalidEncoding, nonce: validMaterial.nonce, pkceVerifier: validMaterial.pkceVerifier}},
		{name: "short nonce", provider: validProvider, material: loginMaterial{state: validMaterial.state, nonce: short, pkceVerifier: validMaterial.pkceVerifier}},
		{name: "invalid verifier encoding", provider: validProvider, material: loginMaterial{state: validMaterial.state, nonce: validMaterial.nonce, pkceVerifier: invalidEncoding}},
		{name: "repeated state and nonce", provider: validProvider, material: loginMaterial{state: validMaterial.state, nonce: validMaterial.state, pkceVerifier: validMaterial.pkceVerifier}},
		{name: "repeated state and verifier", provider: validProvider, material: loginMaterial{state: validMaterial.state, nonce: validMaterial.nonce, pkceVerifier: validMaterial.state}},
		{name: "repeated nonce and verifier", provider: validProvider, material: loginMaterial{state: validMaterial.state, nonce: validMaterial.nonce, pkceVerifier: validMaterial.nonce}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := test.provider.initialAuthorizationURL(test.material); err == nil || got != "" {
				t.Fatalf("initialAuthorizationURL() = (%q, %v), want empty/error", got, err)
			}
		})
	}
}
