package config

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"testing"
)

func TestSecretFormattingRedactsValue(t *testing.T) {
	t.Parallel()

	const raw = "do-not-format-this-secret"
	secret := secret{value: raw}
	for _, format := range []string{"%s", "%q", "%v", "%+v", "%#v"} {
		format := format
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			got := fmt.Sprintf(format, secret)
			if strings.Contains(got, raw) {
				t.Fatalf("format %q exposed secret: %q", format, got)
			}
			if got != "[REDACTED]" {
				t.Fatalf("format %q = %q, want redaction marker", format, got)
			}
		})
	}
}

func TestConfigFormattingRedactsSecrets(t *testing.T) {
	t.Parallel()

	const raw = "do-not-format-config-secret"
	configured := Config{
		databaseURL:      secret{value: raw},
		oidcClientSecret: secret{value: raw},
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		format := format
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			if got := fmt.Sprintf(format, configured); strings.Contains(got, raw) {
				t.Fatalf("nested format %q exposed secret: %q", format, got)
			}
		})
	}
}

func TestConfigTemplateCannotRevealSecrets(t *testing.T) {
	t.Parallel()

	const raw = "do-not-render-config-secret"
	configured := Config{
		databaseURL:      secret{value: raw},
		oidcClientSecret: secret{value: raw},
	}
	templateUnderTest := template.Must(template.New("secret-probe").Parse(
		`{{.DatabaseURL.Reveal}}|{{.OIDCClientSecret.Reveal}}`,
	))
	var rendered bytes.Buffer
	err := templateUnderTest.Execute(&rendered, configured)
	if err == nil {
		t.Fatal("template unexpectedly reached secret reveal methods")
	}
	if strings.Contains(rendered.String(), raw) {
		t.Fatalf("template exposed secret before failing: %q", rendered.String())
	}
}
