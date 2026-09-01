package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

const (
	loginProtectionVersion = byte(1)
	loginGCMNonceBytes     = 12
	loginGCMTagBytes       = 16
	protectedSecretBytes   = 1 + loginGCMNonceBytes + 43 + loginGCMTagBytes
)

type protectedLoginMaterial struct {
	stateHash              [sha256.Size]byte
	nonceCiphertext        [protectedSecretBytes]byte
	pkceVerifierCiphertext [protectedSecretBytes]byte
}

// protectLoginMaterial hashes the browser state lookup key and seals the nonce
// and PKCE verifier into fixed versioned AES-256-GCM envelopes. Field-separated
// HMAC-SHA-256 keys are derived from the 256-bit state, so a database-only
// disclosure cannot recover either secret. The state hash is authenticated as
// additional data, binding each envelope to its login-attempt row.
//
// Complexity: time and auxiliary space are tight Theta(1): input and output
// sizes are fixed, with two AES-GCM seals over 43-byte plaintexts.
func protectLoginMaterial(material loginMaterial, entropy io.Reader) (protectedLoginMaterial, error) {
	decode := func(value string) ([loginSecretBytes]byte, error) {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
		defer clear(decoded)
		if err != nil || len(decoded) != loginSecretBytes {
			return [loginSecretBytes]byte{}, fmt.Errorf("login material has an invalid encoding or length")
		}
		var result [loginSecretBytes]byte
		copy(result[:], decoded)
		return result, nil
	}
	state, err := decode(material.state)
	if err != nil {
		return protectedLoginMaterial{}, err
	}
	defer clear(state[:])
	nonce, err := decode(material.nonce)
	if err != nil {
		return protectedLoginMaterial{}, err
	}
	defer clear(nonce[:])
	verifier, err := decode(material.pkceVerifier)
	if err != nil {
		return protectedLoginMaterial{}, err
	}
	defer clear(verifier[:])
	if bytes.Equal(state[:], nonce[:]) || bytes.Equal(state[:], verifier[:]) || bytes.Equal(nonce[:], verifier[:]) {
		return protectedLoginMaterial{}, fmt.Errorf("login material repeats a value")
	}
	if entropy == nil {
		return protectedLoginMaterial{}, fmt.Errorf("login protection entropy source is required")
	}
	var randomNonces [2 * loginGCMNonceBytes]byte
	defer clear(randomNonces[:])
	if _, err := io.ReadFull(entropy, randomNonces[:]); err != nil {
		return protectedLoginMaterial{}, fmt.Errorf("read login protection entropy: %w", err)
	}

	stateBytes := []byte(material.state)
	stateHash := sha256.Sum256(stateBytes)
	clear(stateBytes)
	seal := func(label, plaintext string, randomNonce []byte) ([protectedSecretBytes]byte, error) {
		var key [sha256.Size]byte
		mac := hmac.New(sha256.New, state[:])
		_, _ = mac.Write([]byte(label))
		mac.Sum(key[:0])
		defer clear(key[:])
		block, err := aes.NewCipher(key[:])
		if err != nil {
			return [protectedSecretBytes]byte{}, fmt.Errorf("construct login protection cipher: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return [protectedSecretBytes]byte{}, fmt.Errorf("construct login protection AEAD: %w", err)
		}
		plaintextBytes := []byte(plaintext)
		defer clear(plaintextBytes)
		var envelope [protectedSecretBytes]byte
		envelope[0] = loginProtectionVersion
		copy(envelope[1:1+loginGCMNonceBytes], randomNonce)
		sealed := aead.Seal(envelope[:1+loginGCMNonceBytes], randomNonce, plaintextBytes, stateHash[:])
		if len(sealed) != len(envelope) {
			return [protectedSecretBytes]byte{}, fmt.Errorf("login protection envelope has an invalid length")
		}
		return envelope, nil
	}
	nonceCiphertext, err := seal("gotth-bb/oidc-login-attempt/nonce/aes-256-gcm/v1", material.nonce, randomNonces[:loginGCMNonceBytes])
	if err != nil {
		return protectedLoginMaterial{}, err
	}
	verifierCiphertext, err := seal("gotth-bb/oidc-login-attempt/pkce-verifier/aes-256-gcm/v1", material.pkceVerifier, randomNonces[loginGCMNonceBytes:])
	if err != nil {
		return protectedLoginMaterial{}, err
	}
	return protectedLoginMaterial{
		stateHash:              stateHash,
		nonceCiphertext:        nonceCiphertext,
		pkceVerifierCiphertext: verifierCiphertext,
	}, nil
}
