package authentikcontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadClosedDescriptorAndToken(t *testing.T) {
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "token")
	objectsPath := filepath.Join(directory, "objects.json")
	if err := os.WriteFile(tokenPath, []byte("one-private-token"), 0o400); err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"issuer_origin":"https://auth.example.test","flows":{"open":{"slug":"gotth-bb-open","uuid":"` + testOpenFlow + `"},"approval":{"slug":"gotth-bb-approval","uuid":"` + testApprovalFlow + `"},"invitation":{"slug":"gotth-bb-invitation","uuid":"` + testInvitationFlow + `"}},"groups":{"accepted":"` + testAcceptedGroup + `","pending":"` + testPendingGroup + `","suspended":"` + testSuspendedGroup + `"}}`
	if err := os.WriteFile(objectsPath, []byte(raw), 0o400); err != nil {
		t.Fatal(err)
	}
	secret, objects, err := Load(tokenPath, objectsPath, "https://auth.example.test/application/o/gotth-bb/")
	if err != nil || string(secret.bytes) != "one-private-token" || objects.Flows.Invitation.UUID != testInvitationFlow {
		t.Fatalf("unexpected load: %#v %#v %v", secret, objects, err)
	}
	secret.destroy()
	if len(secret.bytes) != 0 {
		t.Fatal("secret not destroyed")
	}
}

func TestLoadRejectsUnsafeInputs(t *testing.T) {
	directory := t.TempDir()
	validObjects := `{"version":1,"issuer_origin":"https://auth.example.test","flows":{"open":{"slug":"gotth-bb-open","uuid":"` + testOpenFlow + `"},"approval":{"slug":"gotth-bb-approval","uuid":"` + testApprovalFlow + `"},"invitation":{"slug":"gotth-bb-invitation","uuid":"` + testInvitationFlow + `"}},"groups":{"accepted":"` + testAcceptedGroup + `","pending":"` + testPendingGroup + `","suspended":"` + testSuspendedGroup + `"}}`
	tests := []struct {
		name, token, objects string
		mutate               func(string, string)
	}{
		{"token newline", "token\n", validObjects, nil},
		{"unknown field", "token", strings.Replace(validObjects, `"version":1`, `"version":1,"extra":true`, 1), nil},
		{"duplicate field", "token", strings.Replace(validObjects, `"version":1`, `"version":1,"version":1`, 1), nil},
		{"duplicate identity", "token", strings.Replace(validObjects, testSuspendedGroup, testPendingGroup, 1), nil},
		{"issuer mismatch", "token", validObjects, func(_, _ string) {}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tokenPath := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+"-token")
			objectsPath := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+"-objects")
			if err := os.WriteFile(tokenPath, []byte(test.token), 0o400); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(objectsPath, []byte(test.objects), 0o400); err != nil {
				t.Fatal(err)
			}
			issuer := "https://auth.example.test/application/o/gotth-bb/"
			if test.name == "issuer mismatch" {
				issuer = "https://other.example.test/application/o/gotth-bb/"
			}
			if _, _, err := Load(tokenPath, objectsPath, issuer); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}
