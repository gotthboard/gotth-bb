package config

import (
	"testing"
	"time"
)

func TestLoadSMTPConfigAcceptsExactDisabledAndEnabledStates(t *testing.T) {
	disabled := validConfigEnvironment()
	got, err := loadSMTPConfig(mapLookup(disabled), EnvironmentProduction)
	if err != nil || got.Configured() {
		t.Fatalf("disabled SMTP = (%+v, %v)", got, err)
	}
	enabled := validConfigEnvironment()
	enabled["SMTP_HOST"] = "smtp.example.test"
	enabled["SMTP_PORT"] = "587"
	enabled["SMTP_USERNAME"] = "board"
	enabled["SMTP_FROM"] = "board@example.test"
	enabled["SMTP_TLS_MODE"] = "starttls"
	enabled["SMTP_TIMEOUT"] = "5s"
	enabled["SMTP_PASSWORD_FILE"] = "/run/secrets/smtp-password"
	got, err = loadSMTPConfig(mapLookup(enabled), EnvironmentProduction)
	if err != nil || !got.Configured() || got.Host != "smtp.example.test" || got.Port != 587 || got.Username != "board" || got.From != "board@example.test" || got.TLSMode != SMTPStartTLS || got.Timeout != 5*time.Second || got.PasswordFile != "/run/secrets/smtp-password" {
		t.Fatalf("enabled SMTP = (%+v, %v)", got, err)
	}
}

func TestLoadSMTPConfigRejectsPartialOrUnsafeStates(t *testing.T) {
	tests := []func(map[string]string){
		func(values map[string]string) { values["SMTP_HOST"] = "smtp.example.test" },
		func(values map[string]string) { values["SMTP_HOST"] = "SMTP.EXAMPLE.TEST" },
		func(values map[string]string) {
			values["SMTP_HOST"] = "smtp.example.test"
			values["SMTP_PORT"] = "0587"
		},
		func(values map[string]string) {
			values["SMTP_HOST"], values["SMTP_PORT"], values["SMTP_FROM"], values["SMTP_TLS_MODE"], values["SMTP_TIMEOUT"] = "smtp.example.test", "25", "board@example.test", "plain", "5s"
		},
		func(values map[string]string) {
			values["SMTP_HOST"], values["SMTP_PORT"], values["SMTP_USERNAME"], values["SMTP_FROM"], values["SMTP_TLS_MODE"], values["SMTP_TIMEOUT"] = "smtp.example.test", "587", "board", "board@example.test", "starttls", "5s"
		},
		func(values map[string]string) { values["SMTP_PASSWORD_FILE"] = "/run/secrets/smtp-password" },
	}
	for index, mutate := range tests {
		values := validConfigEnvironment()
		mutate(values)
		if got, err := loadSMTPConfig(mapLookup(values), EnvironmentProduction); err == nil || got.Configured() {
			t.Fatalf("unsafe SMTP case %d = (%+v, %v)", index, got, err)
		}
	}
}
