package config

import (
	"fmt"
	"time"
)

// parsePositiveDuration parses one required duration without echoing its raw
// value into diagnostics.
//
// Complexity: for n input bytes, time O(n), Omega(1) on an early parse failure,
// and tight Theta(n) for valid input under time.ParseDuration's decimal scan;
// auxiliary space O(n) in the delegated parser's error path, Omega(1), and
// tight Theta(1) for valid input. The returned duration is one fixed-width
// value and the raw input is never copied into our error.
func parsePositiveDuration(name, raw string) (time.Duration, error) {
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return value, nil
}
