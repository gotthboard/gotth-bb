package auth

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"slices"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// initialAuthorizationURL validates one complete discovered provider and one
// fixed independent login-material set, then builds the Authorization Code URL
// with state, nonce, and an S256 PKCE challenge. Neither the verifier nor the
// client secret is placed in browser state.
//
// Complexity: time and auxiliary space are tight Theta(1): three fixed 43-byte
// decodes, three fixed comparisons, one SHA-256 challenge, and bounded URL
// construction from immutable provider fields.
func (provider discoveredOIDCProvider) initialAuthorizationURL(material loginMaterial) (string, error) {
	if provider.provider == nil || provider.verifier == nil || provider.httpClient == nil || provider.oauth2Config.ClientID == "" ||
		provider.oauth2Config.Endpoint.AuthURL == "" || provider.oauth2Config.RedirectURL == "" ||
		!slices.Equal(provider.oauth2Config.Scopes, []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail}) {
		return "", fmt.Errorf("OIDC provider is not initialized for initial authorization")
	}
	decode := func(value string) ([loginSecretBytes]byte, error) {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
		defer clear(decoded)
		if err != nil || len(decoded) != loginSecretBytes {
			return [loginSecretBytes]byte{}, fmt.Errorf("login authorization material has an invalid encoding or length")
		}
		var result [loginSecretBytes]byte
		copy(result[:], decoded)
		return result, nil
	}
	state, err := decode(material.state)
	if err != nil {
		return "", err
	}
	defer clear(state[:])
	nonce, err := decode(material.nonce)
	if err != nil {
		return "", err
	}
	defer clear(nonce[:])
	verifier, err := decode(material.pkceVerifier)
	if err != nil {
		return "", err
	}
	defer clear(verifier[:])
	if bytes.Equal(state[:], nonce[:]) || bytes.Equal(state[:], verifier[:]) || bytes.Equal(nonce[:], verifier[:]) {
		return "", fmt.Errorf("login authorization material repeats a value")
	}
	return provider.oauth2Config.AuthCodeURL(
		material.state,
		oauth2.SetAuthURLParam("nonce", material.nonce),
		oauth2.S256ChallengeOption(material.pkceVerifier),
	), nil
}
