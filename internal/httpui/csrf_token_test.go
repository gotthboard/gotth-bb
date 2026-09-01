package httpui

import (
	"crypto/sha256"
	"encoding/base64"
	"reflect"
	"testing"
)

func TestDeriveCSRFTokenMatchesIndependentHMACVector(t *testing.T) {
	t.Parallel()

	raw := make([]byte, sessionCookieTokenBytes)
	for index := range raw {
		raw[index] = byte(index)
	}
	sessionToken := base64.RawURLEncoding.EncodeToString(raw)
	const want = "5AuYkU2gcdIavXk_b7wteYUat25eNfRSoad3APyb8E4"
	got, err := deriveCSRFToken(sessionToken)
	if err != nil || got != want || got == sessionToken {
		t.Fatalf("deriveCSRFToken() = (%q, %v), want independent vector %q", got, err, want)
	}
	decoded, decodeErr := base64.RawURLEncoding.Strict().DecodeString(got)
	if decodeErr != nil || len(decoded) != sha256.Size {
		t.Fatalf("derived token decoding = (%x, %v)", decoded, decodeErr)
	}
}

func TestDeriveCSRFTokenRejectsInvalidSessionCredentials(t *testing.T) {
	t.Parallel()

	valid := base64.RawURLEncoding.EncodeToString(make([]byte, sessionCookieTokenBytes))
	for _, token := range []string{"", valid[:len(valid)-1], valid[:len(valid)-1] + "*"} {
		token := token
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			if got, err := deriveCSRFToken(token); err == nil || !reflect.DeepEqual(got, "") {
				t.Fatalf("deriveCSRFToken(%q) = (%q, %v), want empty/error", token, got, err)
			}
		})
	}
}
