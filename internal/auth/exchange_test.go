package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestExchangeInitialLoginExchangesOnceAndVerifiesTokenNonceAndClaims(t *testing.T) {
	t.Parallel()

	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	provider := harness.discover(t)
	got, err := provider.exchangeInitialLogin(context.Background(), "successful-code", harness.material)
	if err != nil {
		t.Fatalf("exchangeInitialLogin() returned error: %v", err)
	}
	if got.issuer != harness.issuer || got.subject != "subject-1" || got.displayName != "Danny Hunn" ||
		got.email == nil || *got.email != "danny@example.com" || got.avatarURL == nil || *got.avatarURL != harness.server.URL+"/avatar.png" {
		t.Fatalf("verified identity claims = %+v", got)
	}
	if harness.tokenRequestCount("successful-code") != 1 || harness.jwksRequestCount() != 1 {
		t.Fatalf("token/JWKS requests = %d/%d", harness.tokenRequestCount("successful-code"), harness.jwksRequestCount())
	}
}

func TestExchangeInitialLoginRejectsExchangeAndTokenFailuresWithoutLeaks(t *testing.T) {
	t.Parallel()

	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	provider := harness.discover(t)
	for _, code := range []string{
		"exchange-failure", "oversize-token-response", "missing-id-token", "wrong-id-token-type", "oversize-id-token",
		"invalid-signature", "wrong-issuer", "wrong-audience", "expired-token",
		"nonce-mismatch", "empty-subject", "invalid-profile-claims", "missing-access-token", "access-token-hash-mismatch",
	} {
		code := code
		t.Run(code, func(t *testing.T) {
			got, err := provider.exchangeInitialLogin(context.Background(), code, harness.material)
			if err == nil || got != (verifiedIdentityClaims{}) {
				t.Fatalf("exchangeInitialLogin() = (%+v, %v), want zero/error", got, err)
			}
			if strings.Contains(err.Error(), code) || strings.Contains(err.Error(), exchangeResponseSecret) {
				t.Fatalf("exchange error leaked code or response body: %q", err)
			}
			if harness.tokenRequestCount(code) != 1 {
				t.Fatalf("token request count for %q = %d; error = %v", code, harness.tokenRequestCount(code), err)
			}
		})
	}
}

func TestExchangeInitialLoginRejectsInvalidInputBeforeNetwork(t *testing.T) {
	t.Parallel()

	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	provider := harness.discover(t)
	invalidEncoding := strings.Repeat("!", 43)
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		provider discoveredOIDCProvider
		code     string
		material loginMaterial
	}{
		{name: "nil context", provider: provider, code: "not-sent", material: harness.material},
		{name: "canceled context", ctx: canceledContext, provider: provider, code: "not-sent", material: harness.material},
		{name: "zero provider", ctx: context.Background(), code: "not-sent", material: harness.material},
		{name: "missing HTTP client", ctx: context.Background(), provider: discoveredOIDCProvider{provider: provider.provider, verifier: provider.verifier, oauth2Config: provider.oauth2Config}, code: "not-sent", material: harness.material},
		{name: "empty code", ctx: context.Background(), provider: provider, material: harness.material},
		{name: "long code", ctx: context.Background(), provider: provider, code: strings.Repeat("c", maxOIDCAuthorizationCodeBytes+1), material: harness.material},
		{name: "code control", ctx: context.Background(), provider: provider, code: "code\n", material: harness.material},
		{name: "code invalid UTF-8", ctx: context.Background(), provider: provider, code: string([]byte{0xff}), material: harness.material},
		{name: "invalid nonce", ctx: context.Background(), provider: provider, code: "not-sent", material: loginMaterial{state: harness.material.state, nonce: invalidEncoding, pkceVerifier: harness.material.pkceVerifier}},
		{name: "invalid verifier", ctx: context.Background(), provider: provider, code: "not-sent", material: loginMaterial{state: harness.material.state, nonce: harness.material.nonce, pkceVerifier: invalidEncoding}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got, err := test.provider.exchangeInitialLogin(test.ctx, test.code, test.material)
			if err == nil || got != (verifiedIdentityClaims{}) {
				t.Fatalf("exchangeInitialLogin() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
	if count := harness.tokenRequestCount("not-sent"); count != 0 {
		t.Fatalf("invalid inputs caused %d token requests", count)
	}
}

func TestExchangeInitialLoginPreservesCancellationDuringTokenRequest(t *testing.T) {
	t.Parallel()

	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	provider := harness.discover(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := provider.exchangeInitialLogin(ctx, "cancel-during-exchange", harness.material)
		result <- err
	}()
	select {
	case <-harness.exchangeEntered:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("token request did not reach controlled endpoint")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || harness.tokenRequestCount("cancel-during-exchange") != 1 {
			t.Fatalf("exchange result = %v, requests = %d", err, harness.tokenRequestCount("cancel-during-exchange"))
		}
	case <-time.After(time.Second):
		t.Fatal("canceled token exchange did not return")
	}
}

const exchangeResponseSecret = "token-endpoint-secret-body"

type oidcExchangeHarness struct {
	t               *testing.T
	server          *httptest.Server
	issuer          string
	material        loginMaterial
	signer          jose.Signer
	invalidSigner   jose.Signer
	publicJWK       jose.JSONWebKey
	mutex           sync.Mutex
	tokenRequests   map[string]int
	jwksRequests    int
	exchangeEntered chan struct{}
	exchangeOnce    sync.Once
}

func newOIDCExchangeHarness(t *testing.T) *oidcExchangeHarness {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() returned error: %v", err)
	}
	invalidKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() returned error: %v", err)
	}
	newSigner := func(privateKey *ecdsa.PrivateKey) jose.Signer {
		signer, signerErr := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.ES256, Key: privateKey},
			new(jose.SignerOptions).WithType("JWT").WithHeader(jose.HeaderKey("kid"), "exchange-test-key"),
		)
		if signerErr != nil {
			t.Fatalf("jose.NewSigner() returned error: %v", signerErr)
		}
		return signer
	}
	material, err := generateLoginMaterial(bytes.NewReader(sequentialBytes(96)))
	if err != nil {
		t.Fatalf("generateLoginMaterial() returned error: %v", err)
	}
	harness := &oidcExchangeHarness{
		t: t, material: material, signer: newSigner(key), invalidSigner: newSigner(invalidKey),
		publicJWK:     jose.JSONWebKey{Key: &key.PublicKey, KeyID: "exchange-test-key", Algorithm: string(jose.ES256), Use: "sig"},
		tokenRequests: make(map[string]int), exchangeEntered: make(chan struct{}),
	}
	harness.server = httptest.NewServer(http.HandlerFunc(harness.serveHTTP))
	harness.issuer = harness.server.URL + "/application/o/gotth-bb/"
	return harness
}

func (harness *oidcExchangeHarness) discover(t *testing.T) discoveredOIDCProvider {
	t.Helper()
	provider, err := discoverOIDCProvider(
		context.Background(), harness.server.Client().Transport, mustProviderURL(t, harness.issuer),
		"gotth-bb", "client-secret", "https://forum.example/bb/auth/callback",
	)
	if err != nil {
		t.Fatalf("discoverOIDCProvider() returned error: %v", err)
	}
	return provider
}

func (harness *oidcExchangeHarness) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/application/o/gotth-bb/.well-known/openid-configuration":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"id_token_signing_alg_values_supported":["ES256"],"token_endpoint_auth_methods_supported":["client_secret_basic"]}`,
			harness.issuer, harness.server.URL+"/authorize", harness.server.URL+"/token", harness.server.URL+"/jwks")
	case "/jwks":
		harness.mutex.Lock()
		harness.jwksRequests++
		harness.mutex.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []jose.JSONWebKey{harness.publicJWK}})
	case "/token":
		harness.serveToken(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (harness *oidcExchangeHarness) serveToken(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		harness.t.Errorf("ParseForm() returned error: %v", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	code := request.Form.Get("code")
	harness.mutex.Lock()
	harness.tokenRequests[code]++
	harness.mutex.Unlock()
	if code == "cancel-during-exchange" {
		harness.exchangeOnce.Do(func() { close(harness.exchangeEntered) })
		<-request.Context().Done()
		return
	}
	clientID, clientSecret, basic := request.BasicAuth()
	if request.Method != http.MethodPost || !basic || clientID != "gotth-bb" || clientSecret != "client-secret" ||
		request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("redirect_uri") != "https://forum.example/bb/auth/callback" ||
		request.Form.Get("code_verifier") != harness.material.pkceVerifier {
		harness.t.Errorf("invalid token request method/auth/form for code %q", code)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if code == "exchange-failure" {
		http.Error(writer, exchangeResponseSecret, http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if code == "oversize-token-response" {
		_, _ = writer.Write([]byte(`{"access_token":"access-token","token_type":"Bearer"}`))
		_, _ = writer.Write([]byte(strings.Repeat(" ", oidcHTTPResponseMaxBytes)))
		return
	}
	if code == "missing-id-token" {
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "access-token", "token_type": "Bearer", "expires_in": 300})
		return
	}
	if code == "wrong-id-token-type" {
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "access-token", "token_type": "Bearer", "id_token": 42})
		return
	}
	if code == "oversize-id-token" {
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "access-token", "token_type": "Bearer", "id_token": strings.Repeat("x", maxOIDCIDTokenBytes+1)})
		return
	}
	now := time.Now()
	issuer := harness.issuer
	audience := "gotth-bb"
	expiry := now.Add(5 * time.Minute)
	nonce := harness.material.nonce
	subject := "subject-1"
	profile := map[string]any{
		"name": "Danny Hunn", "email": "danny@example.com", "email_verified": true,
		"picture": harness.server.URL + "/avatar.png", "role": "administrator", "groups": []string{"staff"},
	}
	signer := harness.signer
	switch code {
	case "invalid-signature":
		signer = harness.invalidSigner
	case "wrong-issuer":
		issuer += "wrong"
	case "wrong-audience":
		audience = "other-client"
	case "expired-token":
		expiry = now.Add(-time.Hour)
	case "nonce-mismatch":
		nonce = validEncodedLoginState
	case "empty-subject":
		subject = ""
	case "invalid-profile-claims":
		profile["name"] = 42
	}
	accessToken := "access-token"
	atHash := accessTokenHash(accessToken)
	if code == "access-token-hash-mismatch" {
		atHash = accessTokenHash("different-access-token")
	}
	if code == "missing-access-token" {
		accessToken = ""
	}
	claims := map[string]any{
		"iss": issuer, "sub": subject, "aud": audience, "exp": expiry.Unix(), "iat": now.Unix(), "nonce": nonce, "at_hash": atHash,
	}
	for key, value := range profile {
		claims[key] = value
	}
	rawIDToken, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		harness.t.Errorf("sign ID token: %v", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"access_token": accessToken, "token_type": "Bearer", "expires_in": 300, "id_token": rawIDToken,
	})
}

func (harness *oidcExchangeHarness) tokenRequestCount(code string) int {
	harness.mutex.Lock()
	defer harness.mutex.Unlock()
	return harness.tokenRequests[code]
}

func (harness *oidcExchangeHarness) jwksRequestCount() int {
	harness.mutex.Lock()
	defer harness.mutex.Unlock()
	return harness.jwksRequests
}

func accessTokenHash(accessToken string) string {
	hash := sha256.Sum256([]byte(accessToken))
	return base64.RawURLEncoding.EncodeToString(hash[:len(hash)/2])
}
