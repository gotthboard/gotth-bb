package httpui

import (
	"fmt"
	"math"
)

// parseTopicID accepts only the canonical positive base-10 spelling of a
// signed 64-bit topic identifier. It does not normalize signs, leading zeroes,
// escapes, separators, or overflow into a different identifier.
//
// Complexity: for n input bytes, time is O(min(n,19)), Omega(1), and auxiliary
// space is tight Theta(1).
func parseTopicID(raw string) (int64, error) {
	if raw == "" || len(raw) > len("9223372036854775807") || raw[0] == '0' {
		return 0, fmt.Errorf("topic identifier is invalid")
	}
	var identifier int64
	for index := 0; index < len(raw); index++ {
		digit := raw[index]
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("topic identifier is invalid")
		}
		value := int64(digit - '0')
		if identifier > (math.MaxInt64-value)/10 {
			return 0, fmt.Errorf("topic identifier is invalid")
		}
		identifier = identifier*10 + value
	}
	return identifier, nil
}
