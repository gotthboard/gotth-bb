package config

import (
	"fmt"
	"io"
)

// secret holds startup material that must not be exposed by ordinary
// formatting or serialization paths.
type secret struct {
	value string
}

// Format redacts the value for every fmt verb, including Go-syntax output.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary space O(1),
// Omega(1), and tight Theta(1). The fixed redaction marker has constant length;
// io.WriteString delegates the bounded write to the formatter state.
func (secret secret) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED]")
}
