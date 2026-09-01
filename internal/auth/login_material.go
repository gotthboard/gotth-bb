package auth

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
)

const loginSecretBytes = 32

type loginMaterial struct {
	state        string
	nonce        string
	pkceVerifier string
}

// generateLoginMaterial reads independent 256-bit state, nonce, and PKCE
// verifier values. It returns no partial material on entropy failure and clears
// the mutable source buffer before return.
//
// Complexity: time and auxiliary space are tight Theta(1): one fixed 96-byte
// read, one fixed mutable buffer, and three 43-byte base64url strings.
func generateLoginMaterial(entropy io.Reader) (loginMaterial, error) {
	if entropy == nil {
		return loginMaterial{}, fmt.Errorf("login material entropy source is required")
	}
	var raw [3 * loginSecretBytes]byte
	defer clear(raw[:])
	if _, err := io.ReadFull(entropy, raw[:]); err != nil {
		return loginMaterial{}, fmt.Errorf("read login material entropy: %w", err)
	}
	state := raw[:loginSecretBytes]
	nonce := raw[loginSecretBytes : 2*loginSecretBytes]
	verifier := raw[2*loginSecretBytes:]
	if bytes.Equal(state, nonce) || bytes.Equal(state, verifier) || bytes.Equal(nonce, verifier) {
		return loginMaterial{}, fmt.Errorf("login material entropy repeated a value")
	}
	return loginMaterial{
		state:        base64.RawURLEncoding.EncodeToString(state),
		nonce:        base64.RawURLEncoding.EncodeToString(nonce),
		pkceVerifier: base64.RawURLEncoding.EncodeToString(verifier),
	}, nil
}
