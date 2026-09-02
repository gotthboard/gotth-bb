package config

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ParseBootstrapAdministratorSubject validates the exact case-sensitive OIDC
// subject that may claim first-run administration. Errors never echo identity
// bytes.
//
// Complexity: for n input bytes, time is tight Theta(n) and auxiliary space is
// tight Theta(1); the returned string retains the caller's immutable bytes.
func ParseBootstrapAdministratorSubject(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("BOOTSTRAP_ADMIN_SUBJECT has an invalid encoding")
	}
	length := utf8.RuneCountInString(raw)
	if length < 1 || length > 512 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("BOOTSTRAP_ADMIN_SUBJECT has an invalid value")
	}
	return raw, nil
}
