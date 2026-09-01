package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestRecoverLoginMaterialAuthenticatesAndOpensProtectedValues(t *testing.T) {
	t.Parallel()

	material := testLoginMaterial(t)
	protected, err := protectLoginMaterial(material, strings.NewReader(strings.Repeat("n", 24)))
	if err != nil {
		t.Fatalf("protectLoginMaterial() returned error: %v", err)
	}
	recovered, err := recoverLoginMaterial(
		material.state,
		protected.stateHash[:],
		protected.nonceCiphertext[:],
		protected.pkceVerifierCiphertext[:],
	)
	if err != nil || recovered != material {
		t.Fatalf("recoverLoginMaterial() returned original material = %v, error = %v", recovered == material, err)
	}
}

func TestRecoverLoginMaterialRejectsAuthenticatedInvalidOrRepeatedPlaintext(t *testing.T) {
	t.Parallel()

	material := testLoginMaterial(t)
	protected, err := protectLoginMaterial(material, strings.NewReader(strings.Repeat("n", 24)))
	if err != nil {
		t.Fatalf("protectLoginMaterial() returned error: %v", err)
	}
	noncanonicalNonce := noncanonicalRawURL(t, material.nonce)
	for _, test := range []struct {
		name     string
		label    string
		plain    string
		replaces string
	}{
		{name: "noncanonical nonce", label: loginNonceKeyLabel, plain: noncanonicalNonce, replaces: "nonce"},
		{name: "repeated state and nonce", label: loginNonceKeyLabel, plain: material.state, replaces: "nonce"},
		{name: "repeated nonce and verifier", label: loginVerifierKeyLabel, plain: material.nonce, replaces: "verifier"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			forged := sealTestLoginEnvelope(t, material.state, test.label, test.plain)
			nonceEnvelope := protected.nonceCiphertext[:]
			verifierEnvelope := protected.pkceVerifierCiphertext[:]
			if test.replaces == "nonce" {
				nonceEnvelope = forged
			} else {
				verifierEnvelope = forged
			}
			recovered, err := recoverLoginMaterial(material.state, protected.stateHash[:], nonceEnvelope, verifierEnvelope)
			if err == nil || recovered != (loginMaterial{}) {
				t.Fatalf("recoverLoginMaterial() returned zero material = %v, error = %v", recovered == (loginMaterial{}), err)
			}
		})
	}
}

func TestRecoverLoginMaterialRejectsStateAndEnvelopeTampering(t *testing.T) {
	t.Parallel()

	material := testLoginMaterial(t)
	protected, err := protectLoginMaterial(material, strings.NewReader(strings.Repeat("n", 24)))
	if err != nil {
		t.Fatalf("protectLoginMaterial() returned error: %v", err)
	}
	otherRaw := make([]byte, 96)
	for index := range otherRaw {
		otherRaw[index] = byte(255 - index)
	}
	other, err := generateLoginMaterial(bytes.NewReader(otherRaw))
	if err != nil {
		t.Fatalf("generateLoginMaterial(other) returned error: %v", err)
	}
	noncanonicalState := material.state[:len(material.state)-1] + "9"
	shortState := base64.RawURLEncoding.EncodeToString(make([]byte, 31))

	tests := []struct {
		name       string
		state      string
		stateHash  []byte
		nonce      []byte
		verifier   []byte
		mutate     func([]byte)
		mutatePart string
	}{
		{name: "empty state", stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:]},
		{name: "malformed state", state: "%%%", stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:]},
		{name: "short state", state: shortState, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:]},
		{name: "noncanonical state", state: noncanonicalState, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:]},
		{name: "wrong state", state: other.state, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:]},
		{name: "short state hash", state: material.state, stateHash: protected.stateHash[:31], nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:]},
		{name: "changed state hash", state: material.state, stateHash: append([]byte(nil), protected.stateHash[:]...), nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:], mutatePart: "hash", mutate: flipLastByte},
		{name: "short nonce envelope", state: material.state, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:71], verifier: protected.pkceVerifierCiphertext[:]},
		{name: "long nonce envelope", state: material.state, stateHash: protected.stateHash[:], nonce: append(append([]byte(nil), protected.nonceCiphertext[:]...), 0), verifier: protected.pkceVerifierCiphertext[:]},
		{name: "wrong nonce version", state: material.state, stateHash: protected.stateHash[:], nonce: append([]byte(nil), protected.nonceCiphertext[:]...), verifier: protected.pkceVerifierCiphertext[:], mutatePart: "nonce-version", mutate: flipFirstByte},
		{name: "changed nonce ciphertext", state: material.state, stateHash: protected.stateHash[:], nonce: append([]byte(nil), protected.nonceCiphertext[:]...), verifier: protected.pkceVerifierCiphertext[:], mutatePart: "nonce", mutate: flipLastByte},
		{name: "short verifier envelope", state: material.state, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: protected.pkceVerifierCiphertext[:71]},
		{name: "long verifier envelope", state: material.state, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: append(append([]byte(nil), protected.pkceVerifierCiphertext[:]...), 0)},
		{name: "wrong verifier version", state: material.state, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: append([]byte(nil), protected.pkceVerifierCiphertext[:]...), mutatePart: "verifier-version", mutate: flipFirstByte},
		{name: "changed verifier ciphertext", state: material.state, stateHash: protected.stateHash[:], nonce: protected.nonceCiphertext[:], verifier: append([]byte(nil), protected.pkceVerifierCiphertext[:]...), mutatePart: "verifier", mutate: flipLastByte},
		{name: "swapped field envelopes", state: material.state, stateHash: protected.stateHash[:], nonce: protected.pkceVerifierCiphertext[:], verifier: protected.nonceCiphertext[:]},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.mutate != nil {
				switch test.mutatePart {
				case "hash":
					test.mutate(test.stateHash)
				case "nonce-version", "nonce":
					test.mutate(test.nonce)
				case "verifier-version", "verifier":
					test.mutate(test.verifier)
				}
			}
			recovered, err := recoverLoginMaterial(test.state, test.stateHash, test.nonce, test.verifier)
			if err == nil || recovered != (loginMaterial{}) {
				t.Fatalf("recoverLoginMaterial() returned zero material = %v, error = %v", recovered == (loginMaterial{}), err)
			}
		})
	}
}

func flipFirstByte(value []byte) {
	value[0] ^= 0xff
}

func flipLastByte(value []byte) {
	value[len(value)-1] ^= 0xff
}

func sealTestLoginEnvelope(t *testing.T, stateValue, label, plaintext string) []byte {
	t.Helper()
	state, err := base64.RawURLEncoding.Strict().DecodeString(stateValue)
	if err != nil {
		t.Fatalf("decode test state: %v", err)
	}
	var key [sha256.Size]byte
	mac := hmac.New(sha256.New, state)
	_, _ = mac.Write([]byte(label))
	mac.Sum(key[:0])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("aes.NewCipher() returned error: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM() returned error: %v", err)
	}
	stateHash := sha256.Sum256([]byte(stateValue))
	envelope := make([]byte, 1+loginGCMNonceBytes, protectedSecretBytes)
	envelope[0] = loginProtectionVersion
	return aead.Seal(envelope, envelope[1:1+loginGCMNonceBytes], []byte(plaintext), stateHash[:])
}
