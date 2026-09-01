package config

import (
	"fmt"
	"log/slog"
)

// ParseLogLevel selects the closed structured-log threshold. Empty input uses
// the documented info default.
//
// Complexity: time O(1), Omega(1), and tight Theta(1); auxiliary space O(1),
// Omega(1), and tight Theta(1). The accepted set and maximum compared byte
// count are fixed constants independent of hostile input length.
func ParseLogLevel(raw string) (slog.Level, error) {
	switch raw {
	case "":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
}
