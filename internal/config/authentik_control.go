package config

import (
	"fmt"
	"path/filepath"
)

// ParseAuthentikControlFile accepts one absolute clean non-root path. The
// authentikcontrol package owns descriptor-safe opening and content validation.
func ParseAuthentikControlFile(name, raw string) (string, error) {
	invalidControl := false
	for _, character := range []byte(raw) {
		if character < 0x20 || character == 0x7f {
			invalidControl = true
			break
		}
	}
	if raw == "" || len(raw) > 4096 || invalidControl || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw || raw == string(filepath.Separator) {
		return "", fmt.Errorf("%s must be an absolute clean non-root path of at most 4096 bytes without control characters", name)
	}
	return raw, nil
}

// ParseAuthentikControlSocket accepts only the exact absolute gateway socket
// basename. The gateway separately verifies its parent ownership and mode.
func ParseAuthentikControlSocket(raw string) (string, error) {
	path, err := ParseAuthentikControlFile("AUTHENTIK_CONTROL_SOCKET", raw)
	if err != nil {
		return "", err
	}
	if filepath.Base(path) != "authentik-control.sock" {
		return "", fmt.Errorf("AUTHENTIK_CONTROL_SOCKET must end in authentik-control.sock")
	}
	if len(path) > 107 {
		return "", fmt.Errorf("AUTHENTIK_CONTROL_SOCKET exceeds the Unix socket path limit")
	}
	return path, nil
}
