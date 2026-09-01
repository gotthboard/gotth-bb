package config

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ParseBasePath validates the immutable browser-facing path prefix. The empty
// string represents a root deployment; every non-empty value is preserved
// exactly so generated URLs and cookie scope share one canonical prefix.
//
// Complexity: for n input bytes, validation is O(n) worst-case, Ω(1) when an
// early structural check fails, and Θ(n) for valid input. Auxiliary space is
// O(n) because strings.Split retains O(n) segment headers and PathUnescape may
// allocate decoded segment bytes; both delegated operations are linear in the
// bytes they inspect.
func ParseBasePath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("BASE_PATH must be valid UTF-8")
	}
	if raw == "/" {
		return "", fmt.Errorf("BASE_PATH must be empty for a root deployment")
	}
	if raw[0] != '/' || strings.HasPrefix(raw, "//") {
		return "", fmt.Errorf("BASE_PATH must begin with exactly one slash")
	}
	if strings.HasSuffix(raw, "/") {
		return "", fmt.Errorf("BASE_PATH must not end with a slash")
	}
	if strings.ContainsAny(raw, "?#\\") {
		return "", fmt.Errorf("BASE_PATH must contain only path segments")
	}

	for _, encoded := range strings.Split(raw[1:], "/") {
		if encoded == "" {
			return "", fmt.Errorf("BASE_PATH must not contain empty segments")
		}

		segment, err := url.PathUnescape(encoded)
		if err != nil {
			return "", fmt.Errorf("BASE_PATH contains invalid escaping: %w", err)
		}
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("BASE_PATH must not contain traversal segments")
		}
		if strings.ContainsAny(segment, "/\\") {
			return "", fmt.Errorf("BASE_PATH must not contain encoded separators")
		}
		for _, character := range segment {
			if unicode.IsControl(character) {
				return "", fmt.Errorf("BASE_PATH must not contain control characters")
			}
		}
	}

	return raw, nil
}
