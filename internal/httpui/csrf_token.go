package httpui

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const csrfTokenDomain = "gotth-bb/csrf/v1"

// deriveCSRFToken constructs a domain-separated HMAC-SHA-256 synchronizer token
// keyed by the decoded 256-bit opaque session secret. The derived browser/form
// value cannot be used as the session credential and rotates with the session.
//
// Complexity: validation, HMAC, and base64url encoding are tight Theta(1) time
// over fixed 43/32-byte inputs and outputs. Auxiliary space is O(1) excluding
// fixed standard-library hash state and the immutable 43-byte result string;
// decoded key and digest buffers are cleared before return.
func deriveCSRFToken(sessionToken string) (string, error) {
	if len(sessionToken) != sessionCookieEncodedBytes {
		return "", fmt.Errorf("CSRF session credential has an invalid length")
	}
	var encoded [sessionCookieEncodedBytes]byte
	copy(encoded[:], sessionToken)
	defer clear(encoded[:])
	var decoded [sessionCookieTokenBytes]byte
	decodedLength, err := base64.RawURLEncoding.Strict().Decode(decoded[:], encoded[:])
	defer clear(decoded[:])
	if err != nil || decodedLength != sessionCookieTokenBytes {
		return "", fmt.Errorf("CSRF session credential has an invalid encoding")
	}
	mac := hmac.New(sha256.New, decoded[:])
	_, _ = mac.Write([]byte(csrfTokenDomain))
	digest := mac.Sum(nil)
	defer clear(digest)
	return base64.RawURLEncoding.EncodeToString(digest), nil
}
