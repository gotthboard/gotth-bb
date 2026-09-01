package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

const sessionTokenBytes = 32

type sessionMaterial struct {
	token     string
	tokenHash [sha256.Size]byte
}

// generateSessionMaterial reads one 256-bit opaque token, encodes the browser
// value without padding, hashes the exact encoded cookie bytes for database
// lookup, and clears both mutable source and encoded copies before return.
//
// Complexity: time and auxiliary space are tight Theta(1): one fixed 32-byte
// entropy read, one 43-byte base64url encoding, and one SHA-256 operation.
func generateSessionMaterial(entropy io.Reader) (sessionMaterial, error) {
	if entropy == nil {
		return sessionMaterial{}, fmt.Errorf("session entropy source is required")
	}
	var raw [sessionTokenBytes]byte
	defer clear(raw[:])
	if _, err := io.ReadFull(entropy, raw[:]); err != nil {
		return sessionMaterial{}, fmt.Errorf("read session entropy: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	tokenBytes := []byte(token)
	tokenHash := sha256.Sum256(tokenBytes)
	clear(tokenBytes)
	return sessionMaterial{token: token, tokenHash: tokenHash}, nil
}
