package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestProtectLoginMaterialHashesStateAndSealsDatabaseSecrets(t *testing.T) {
	t.Parallel()

	material := testLoginMaterial(t)
	protected, err := protectLoginMaterial(material, strings.NewReader(strings.Repeat("n", 24)))
	if err != nil {
		t.Fatalf("protectLoginMaterial() returned error: %v", err)
	}
	wantHash := sha256.Sum256([]byte(material.state))
	if protected.stateHash != wantHash {
		t.Fatal("state hash does not match the browser state lookup key")
	}
	for name, value := range map[string][]byte{
		"nonce":         protected.nonceCiphertext[:],
		"PKCE verifier": protected.pkceVerifierCiphertext[:],
	} {
		if len(value) != 72 || value[0] != 1 {
			t.Errorf("%s protected envelope has length %d and version %d", name, len(value), value[0])
		}
	}
	if bytes.Contains(protected.nonceCiphertext[:], []byte(material.nonce)) || bytes.Contains(protected.pkceVerifierCiphertext[:], []byte(material.pkceVerifier)) {
		t.Fatal("protected database envelope contains plaintext login material")
	}
	if bytes.Equal(protected.nonceCiphertext[:], protected.pkceVerifierCiphertext[:]) {
		t.Fatal("field-separated keys produced identical envelopes")
	}
	for name, test := range map[string]struct {
		value []byte
		want  string
	}{
		"nonce":         {value: protected.nonceCiphertext[:], want: "a8d1851083edec7931a6c7e67de23c15a65488509488002d4165ef24098b528c"},
		"PKCE verifier": {value: protected.pkceVerifierCiphertext[:], want: "2dc32c7d3857d78c772e9a969cb2cd45aae892b14bb43ddc8833356c70992565"},
	} {
		digest := sha256.Sum256(test.value)
		if got := hex.EncodeToString(digest[:]); got != test.want {
			t.Errorf("%s envelope SHA-256 = %s", name, got)
		}
	}
}

func TestProtectLoginMaterialRejectsInvalidMaterialBeforeEntropy(t *testing.T) {
	t.Parallel()

	valid := testLoginMaterial(t)
	short := base64.RawURLEncoding.EncodeToString(make([]byte, 31))
	noncanonicalState := noncanonicalRawURL(t, valid.state)
	for _, material := range []loginMaterial{
		{},
		{state: "%%%", nonce: valid.nonce, pkceVerifier: valid.pkceVerifier},
		{state: short, nonce: valid.nonce, pkceVerifier: valid.pkceVerifier},
		{state: noncanonicalState, nonce: valid.nonce, pkceVerifier: valid.pkceVerifier},
		{state: valid.state, nonce: "%%%", pkceVerifier: valid.pkceVerifier},
		{state: valid.state, nonce: short, pkceVerifier: valid.pkceVerifier},
		{state: valid.state, nonce: valid.nonce, pkceVerifier: "%%%"},
		{state: valid.state, nonce: valid.nonce, pkceVerifier: short},
		{state: valid.state, nonce: valid.state, pkceVerifier: valid.pkceVerifier},
	} {
		if protected, err := protectLoginMaterial(material, panicReader{}); err == nil || protected != (protectedLoginMaterial{}) {
			t.Fatalf("invalid login material returned zero envelope = %v, error = %v", protected == (protectedLoginMaterial{}), err)
		}
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("entropy reader must not be called")
}

func TestProtectLoginMaterialRejectsMissingShortOrFailedNonceEntropy(t *testing.T) {
	t.Parallel()

	material := testLoginMaterial(t)
	cause := errors.New("nonce entropy failed")
	for _, test := range []struct {
		name    string
		reader  io.Reader
		wantErr error
	}{
		{name: "nil reader"},
		{name: "short reader", reader: strings.NewReader(strings.Repeat("n", 23)), wantErr: io.ErrUnexpectedEOF},
		{name: "reader failure", reader: errReader{cause: cause}, wantErr: cause},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			protected, err := protectLoginMaterial(material, test.reader)
			if err == nil || protected != (protectedLoginMaterial{}) {
				t.Fatalf("protectLoginMaterial() returned zero envelope = %v, error = %v", protected == (protectedLoginMaterial{}), err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want cause %v", err, test.wantErr)
			}
		})
	}
}

func testLoginMaterial(t *testing.T) loginMaterial {
	t.Helper()
	raw := make([]byte, 96)
	for index := range raw {
		raw[index] = byte(index)
	}
	material, err := generateLoginMaterial(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("generateLoginMaterial() returned error: %v", err)
	}
	return material
}

func noncanonicalRawURL(t *testing.T, canonical string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	index := strings.IndexByte(alphabet, canonical[len(canonical)-1])
	if index < 0 || index%4 != 0 {
		t.Fatal("canonical test fixture has an unexpected trailing base64url symbol")
	}
	noncanonical := canonical[:len(canonical)-1] + string(alphabet[index+1])
	canonicalBytes, canonicalErr := base64.RawURLEncoding.DecodeString(canonical)
	noncanonicalBytes, noncanonicalErr := base64.RawURLEncoding.DecodeString(noncanonical)
	if canonicalErr != nil || noncanonicalErr != nil || !bytes.Equal(canonicalBytes, noncanonicalBytes) {
		t.Fatal("test fixture does not exercise alternate base64url trailing bits")
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(noncanonical); err == nil {
		t.Fatal("strict base64url decoder accepted the noncanonical test fixture")
	}
	return noncanonical
}
