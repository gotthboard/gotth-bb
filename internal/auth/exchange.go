package auth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	maxOIDCAuthorizationCodeBytes = 4096
	maxOIDCIDTokenBytes           = 64 << 10
)

// exchangeInitialLogin sends one authorization code with the recovered PKCE
// verifier, verifies the returned ID-token signature/issuer/audience/expiry,
// validates nonce and any access-token hash, and admits only approved identity
// claims. OAuth tokens are not returned or retained.
//
// Complexity: local code and nonce validation are bounded by 4 KiB and fixed
// 32-byte values. The ID token is bounded to 64 KiB, so local verification and
// claim decoding are O(n), Omega(1), with O(n) auxiliary space. Network work is
// exactly one token exchange plus at most the verifier's cached-key refresh,
// each governed by the discovered provider's ten-second/512 KiB HTTP boundary.
func (provider discoveredOIDCProvider) exchangeInitialLogin(ctx context.Context, code string, material loginMaterial) (verifiedIdentityClaims, error) {
	if ctx == nil {
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC exchange context is required")
	}
	if err := ctx.Err(); err != nil {
		return verifiedIdentityClaims{}, fmt.Errorf("exchange OIDC code: %w", err)
	}
	if provider.provider == nil || provider.verifier == nil || provider.httpClient == nil || provider.oauth2Config.ClientID == "" ||
		provider.oauth2Config.Endpoint.TokenURL == "" || provider.oauth2Config.RedirectURL == "" ||
		!slices.Equal(provider.oauth2Config.Scopes, []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail}) {
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC provider is not initialized for code exchange")
	}
	if len(code) == 0 || len(code) > maxOIDCAuthorizationCodeBytes || !utf8.ValidString(code) ||
		strings.IndexFunc(code, func(character rune) bool { return unicode.IsControl(character) || unicode.IsSpace(character) }) >= 0 {
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC authorization code is invalid")
	}
	decodeSecret := func(value string) ([loginSecretBytes]byte, error) {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
		defer clear(decoded)
		if err != nil || len(decoded) != loginSecretBytes {
			return [loginSecretBytes]byte{}, fmt.Errorf("OIDC login material has an invalid encoding or length")
		}
		var result [loginSecretBytes]byte
		copy(result[:], decoded)
		return result, nil
	}
	expectedNonce, err := decodeSecret(material.nonce)
	if err != nil {
		return verifiedIdentityClaims{}, err
	}
	defer clear(expectedNonce[:])
	verifier, err := decodeSecret(material.pkceVerifier)
	if err != nil {
		return verifiedIdentityClaims{}, err
	}
	defer clear(verifier[:])
	exchangeContext := oidc.ClientContext(ctx, provider.httpClient)
	token, err := provider.oauth2Config.Exchange(exchangeContext, code, oauth2.VerifierOption(material.pkceVerifier))
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return verifiedIdentityClaims{}, fmt.Errorf("exchange OIDC code: %w", contextError)
		}
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC code exchange failed")
	}
	if token == nil {
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC code exchange returned no token")
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || len(rawIDToken) == 0 || len(rawIDToken) > maxOIDCIDTokenBytes {
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC code exchange returned an invalid ID token")
	}
	idToken, err := provider.verifier.Verify(exchangeContext, rawIDToken)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return verifiedIdentityClaims{}, fmt.Errorf("verify OIDC ID token: %w", contextError)
		}
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC ID token verification failed")
	}
	actualNonce, err := decodeSecret(idToken.Nonce)
	if err != nil || subtle.ConstantTimeCompare(expectedNonce[:], actualNonce[:]) != 1 {
		clear(actualNonce[:])
		return verifiedIdentityClaims{}, fmt.Errorf("OIDC ID token nonce is invalid")
	}
	clear(actualNonce[:])
	if idToken.AccessTokenHash != "" {
		if token.AccessToken == "" || idToken.VerifyAccessToken(token.AccessToken) != nil {
			return verifiedIdentityClaims{}, fmt.Errorf("OIDC access-token hash verification failed")
		}
	}
	var claims map[string]json.RawMessage
	if err := idToken.Claims(&claims); err != nil {
		return verifiedIdentityClaims{}, fmt.Errorf("decode verified OIDC claims failed")
	}
	if claims == nil {
		return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC claims are missing")
	}
	accepted, err := validateIdentityClaims(idToken.Issuer, idToken.Subject, claims)
	if err != nil {
		return verifiedIdentityClaims{}, fmt.Errorf("validate verified OIDC claims: %w", err)
	}
	return accepted, nil
}
