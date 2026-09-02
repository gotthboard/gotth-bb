package config

import (
	"strings"
	"testing"
)

func TestParseBootstrapAdministratorSubject(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"opaque-subject", "用户-01", strings.Repeat("x", 512)} {
		value := value
		t.Run(value[:min(len(value), 16)], func(t *testing.T) {
			t.Parallel()
			if got, err := ParseBootstrapAdministratorSubject(value); err != nil || got != value {
				t.Fatalf("ParseBootstrapAdministratorSubject() = (%q, %v)", got, err)
			}
		})
	}
}

func TestParseBootstrapAdministratorSubjectRejectsInvalidValuesWithoutEcho(t *testing.T) {
	t.Parallel()

	secret := "do-not-echo-bootstrap-subject"
	for _, value := range []string{"", secret + "\n", strings.Repeat("x", 513), string([]byte{0xff, 0xfe})} {
		got, err := ParseBootstrapAdministratorSubject(value)
		if err == nil || got != "" {
			t.Fatalf("ParseBootstrapAdministratorSubject() = (%q, %v), want zero/error", got, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed configured subject: %q", err)
		}
	}
}
