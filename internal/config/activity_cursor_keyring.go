package config

import (
	"fmt"
	"path/filepath"
)

// ParseActivityCursorKeyringFile accepts one absolute, clean, non-root path.
// The discovery package owns descriptor-based traversal and file-policy checks
// when startup opens the configured secret.
//
// Complexity: for n path bytes, time is O(n), Omega(1), and tight Theta(n) for
// valid input; auxiliary space is O(n), Omega(1), and tight Theta(n) when
// filepath.Clean allocates a normalized candidate.
func ParseActivityCursorKeyringFile(raw string) (string, error) {
	if raw == "" || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw || raw == string(filepath.Separator) {
		return "", fmt.Errorf("ACTIVITY_CURSOR_KEYRING_FILE must be an absolute clean non-root path")
	}
	return raw, nil
}
