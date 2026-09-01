package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// recoverLoginMaterial validates the canonical browser state and exact stored
// envelope format, compares the lookup hash in constant time, authenticates
// both field-separated AES-256-GCM values, and returns no partial material on
// any mismatch or tampering.
//
// Complexity: time and auxiliary space are tight Theta(1): all accepted input
// sizes are fixed, with one hash and two AES-GCM opens over 43-byte plaintexts.
func recoverLoginMaterial(stateValue string, stateHash, nonceEnvelope, verifierEnvelope []byte) (loginMaterial, error) {
	state, err := base64.RawURLEncoding.Strict().DecodeString(stateValue)
	defer clear(state)
	if err != nil || len(state) != loginSecretBytes {
		return loginMaterial{}, fmt.Errorf("login state has an invalid encoding or length")
	}
	stateValueBytes := []byte(stateValue)
	computedHash := sha256.Sum256(stateValueBytes)
	clear(stateValueBytes)
	if len(stateHash) != sha256.Size || subtle.ConstantTimeCompare(computedHash[:], stateHash) != 1 {
		return loginMaterial{}, fmt.Errorf("login state does not match the stored attempt")
	}

	type openedSecret struct {
		encoded []byte
		decoded [loginSecretBytes]byte
	}
	open := func(label string, envelope []byte) (openedSecret, error) {
		if len(envelope) != protectedSecretBytes || envelope[0] != loginProtectionVersion {
			return openedSecret{}, fmt.Errorf("login protection envelope has an invalid format")
		}
		var key [sha256.Size]byte
		mac := hmac.New(sha256.New, state)
		_, _ = mac.Write([]byte(label))
		mac.Sum(key[:0])
		defer clear(key[:])
		block, err := aes.NewCipher(key[:])
		if err != nil {
			return openedSecret{}, fmt.Errorf("construct login recovery cipher: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return openedSecret{}, fmt.Errorf("construct login recovery AEAD: %w", err)
		}
		plaintext, err := aead.Open(
			nil,
			envelope[1:1+loginGCMNonceBytes],
			envelope[1+loginGCMNonceBytes:],
			computedHash[:],
		)
		if err != nil {
			clear(plaintext)
			return openedSecret{}, fmt.Errorf("login protection authentication failed")
		}
		var decoded [loginSecretBytes]byte
		decodedLength, err := base64.RawURLEncoding.Strict().Decode(decoded[:], plaintext)
		if err != nil || decodedLength != loginSecretBytes {
			clear(plaintext)
			clear(decoded[:])
			return openedSecret{}, fmt.Errorf("login protection plaintext has an invalid encoding or length")
		}
		return openedSecret{encoded: plaintext, decoded: decoded}, nil
	}

	nonce, err := open(loginNonceKeyLabel, nonceEnvelope)
	if err != nil {
		return loginMaterial{}, err
	}
	defer clear(nonce.encoded)
	defer clear(nonce.decoded[:])
	verifier, err := open(loginVerifierKeyLabel, verifierEnvelope)
	if err != nil {
		return loginMaterial{}, err
	}
	defer clear(verifier.encoded)
	defer clear(verifier.decoded[:])
	if bytes.Equal(state, nonce.decoded[:]) || bytes.Equal(state, verifier.decoded[:]) || bytes.Equal(nonce.decoded[:], verifier.decoded[:]) {
		return loginMaterial{}, fmt.Errorf("recovered login material repeats a value")
	}
	return loginMaterial{
		state:        stateValue,
		nonce:        string(nonce.encoded),
		pkceVerifier: string(verifier.encoded),
	}, nil
}
