package administration

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestSMTPCredentialEnvelopeRoundTripIsRevisionBound(t *testing.T) {
	t.Parallel()
	key := [32]byte{1, 2, 3}
	password := []byte("one private password")
	envelope, err := encryptSMTPPassword(password, 7, key, bytes.NewReader(bytes.Repeat([]byte{9}, 12)))
	if err != nil || bytes.Contains(envelope, password) {
		t.Fatalf("encryptSMTPPassword() = (%x, %v)", envelope, err)
	}
	decrypted, err := decryptSMTPPassword(envelope, 7, key)
	if err != nil || !bytes.Equal(decrypted, password) {
		t.Fatalf("decryptSMTPPassword() = (%q, %v)", decrypted, err)
	}
	clear(decrypted)
	if plaintext, err := decryptSMTPPassword(envelope, 8, key); err == nil || plaintext != nil {
		t.Fatal("credential envelope accepted for another settings revision")
	}
	tampered := append([]byte(nil), envelope...)
	tampered[len(tampered)-1] ^= 1
	if plaintext, err := decryptSMTPPassword(tampered, 7, key); err == nil || plaintext != nil {
		t.Fatal("tampered credential envelope accepted")
	}
}

func TestSMTPInputValidationReturnsSpecificFailures(t *testing.T) {
	t.Parallel()
	valid := SMTPSettingsInput{
		Host: "smtp.example.test", Port: "587", Username: "board", FromAddress: "board@example.test",
		TLSMode: "starttls", TimeoutSeconds: "10", PasswordAction: "replace", Password: []byte("secret"), ExpectedRevision: 1,
	}
	if settings, err := validateSMTPInput(valid); err != nil || settings.Port != 587 || settings.TimeoutSeconds != 10 {
		t.Fatalf("validateSMTPInput(valid) = (%+v, %v)", settings, err)
	}
	tests := []struct {
		name string
		edit func(*SMTPSettingsInput)
	}{
		{"uppercase host", func(input *SMTPSettingsInput) { input.Host = "SMTP.example.test" }},
		{"bad port", func(input *SMTPSettingsInput) { input.Port = "0587" }},
		{"named sender", func(input *SMTPSettingsInput) { input.FromAddress = "Board <board@example.test>" }},
		{"plain transport", func(input *SMTPSettingsInput) { input.TLSMode = "plain" }},
		{"long timeout", func(input *SMTPSettingsInput) { input.TimeoutSeconds = "31" }},
		{"missing replacement", func(input *SMTPSettingsInput) { input.Password = nil }},
		{"password on preserve", func(input *SMTPSettingsInput) { input.PasswordAction = "preserve" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.Password = append([]byte(nil), valid.Password...)
			test.edit(&input)
			if _, err := validateSMTPInput(input); err == nil {
				t.Fatal("invalid SMTP input accepted")
			}
		})
	}
}

func TestRuntimeSMTPSettingsRejectsMalformedAndDecryptsConfigured(t *testing.T) {
	t.Parallel()
	key := [32]byte{4}
	envelope, err := encryptSMTPPassword([]byte("secret"), 3, key, bytes.NewReader(bytes.Repeat([]byte{7}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := runtimeSMTPSettings("smtp.example.test", "board", "board@example.test", "starttls", 587, 10, envelope, 3, pgtype.Int8{Int64: 3, Valid: true}, key)
	if err != nil || !settings.Verified || string(settings.Password) != "secret" {
		t.Fatalf("runtimeSMTPSettings() = (%+v, %v)", settings.SMTPSettings, err)
	}
	clear(settings.Password)
	if _, err := runtimeSMTPSettings("", "board", "", "", 0, 0, nil, 3, pgtype.Int8{}, key); err == nil {
		t.Fatal("malformed disabled settings accepted")
	}
}

func TestLoadSMTPCredentialKeyAcceptsOnlyDistinctRawKeyShape(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "smtp-key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{5}, 32), 0o400); err != nil {
		t.Fatal(err)
	}
	key, err := LoadSMTPCredentialKey(path)
	if err != nil || key == ([32]byte{}) {
		t.Fatalf("LoadSMTPCredentialKey() = (%x, %v)", key, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadSMTPCredentialKey(path); err == nil || key != ([32]byte{}) {
		t.Fatal("short SMTP key accepted")
	}
}
