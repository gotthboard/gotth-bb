package observability

import (
	"encoding/hex"
	"fmt"
	"io"
)

// GenerateRequestID reads exactly 128 bits from the caller's cryptographic
// source and encodes them as a fixed lowercase hexadecimal identifier.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary and result
// space O(1), Omega(1), and tight Theta(1). Exactly 16 bytes are read and 32
// bytes encoded; source I/O may block according to its documented behavior.
func GenerateRequestID(source io.Reader) (string, error) {
	if source == nil {
		return "", fmt.Errorf("request ID entropy source is required")
	}

	var entropy [16]byte
	if _, err := io.ReadFull(source, entropy[:]); err != nil {
		return "", fmt.Errorf("request ID entropy is unavailable")
	}
	var encoded [32]byte
	hex.Encode(encoded[:], entropy[:])
	return string(encoded[:]), nil
}
