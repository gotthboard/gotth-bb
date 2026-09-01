package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateIdentityClaimsAcceptsOnlyApprovedProfileFields(t *testing.T) {
	t.Parallel()

	claims := rawIdentityClaims(t, `{
		"name":"Danny Hunn",
		"email":"danny@example.com",
		"email_verified":true,
		"picture":"https://auth.example/media/avatar.png?size=128",
		"role":"administrator",
		"groups":["staff","private-area"]
	}`)
	got, err := validateIdentityClaims("https://auth.example/application/o/gotth-bb/", "authentik-subject-1", claims)
	if err != nil {
		t.Fatalf("validateIdentityClaims() returned error: %v", err)
	}
	if got.issuer != "https://auth.example/application/o/gotth-bb/" || got.subject != "authentik-subject-1" || got.displayName != "Danny Hunn" ||
		got.email == nil || *got.email != "danny@example.com" || got.avatarURL == nil || *got.avatarURL != "https://auth.example/media/avatar.png?size=128" {
		t.Fatalf("validated claims = %+v", got)
	}
}

func TestValidateIdentityClaimsOmitsAbsentOrUnverifiedOptionalFields(t *testing.T) {
	t.Parallel()

	withoutOptional, err := validateIdentityClaims("issuer", "subject", rawIdentityClaims(t, `{"name":"Member"}`))
	if err != nil || withoutOptional.email != nil || withoutOptional.avatarURL != nil {
		t.Fatalf("absent optional claims = (%+v, %v)", withoutOptional, err)
	}
	unverified, err := validateIdentityClaims("issuer", "subject", rawIdentityClaims(t, `{"name":"Member","email":"member@example.com","email_verified":false}`))
	if err != nil || unverified.email != nil {
		t.Fatalf("unverified email = (%+v, %v)", unverified, err)
	}
}

func TestValidateIdentityClaimsAllowsHTTPAvatarOnlyForDevelopmentHTTPIssuer(t *testing.T) {
	t.Parallel()

	got, err := validateIdentityClaims("http://127.0.0.1/application/o/gotth-bb/", "subject", rawIdentityClaims(t, `{"name":"Member","picture":"http://127.0.0.1/avatar.png"}`))
	if err != nil || got.avatarURL == nil || *got.avatarURL != "http://127.0.0.1/avatar.png" {
		t.Fatalf("validateIdentityClaims() = (%+v, %v)", got, err)
	}
}

func TestValidateIdentityClaimsRejectsInvalidCoordinatesAndApprovedClaims(t *testing.T) {
	t.Parallel()

	valid := `{"name":"Member"}`
	for _, test := range []struct {
		name    string
		issuer  string
		subject string
		claims  string
	}{
		{name: "empty issuer", subject: "subject", claims: valid},
		{name: "invalid UTF-8 issuer", issuer: string([]byte{0xff}), subject: "subject", claims: valid},
		{name: "long issuer", issuer: strings.Repeat("i", 2049), subject: "subject", claims: valid},
		{name: "issuer control", issuer: "issuer\n", subject: "subject", claims: valid},
		{name: "empty subject", issuer: "issuer", claims: valid},
		{name: "long subject", issuer: "issuer", subject: strings.Repeat("s", 513), claims: valid},
		{name: "subject control", issuer: "issuer", subject: "subject\t", claims: valid},
		{name: "missing name", issuer: "issuer", subject: "subject", claims: `{}`},
		{name: "null name", issuer: "issuer", subject: "subject", claims: `{"name":null}`},
		{name: "non-string name", issuer: "issuer", subject: "subject", claims: `{"name":42}`},
		{name: "empty name", issuer: "issuer", subject: "subject", claims: `{"name":""}`},
		{name: "long name", issuer: "issuer", subject: "subject", claims: `{"name":"` + strings.Repeat("n", 81) + `"}`},
		{name: "name control", issuer: "issuer", subject: "subject", claims: `{"name":"Member\nName"}`},
		{name: "non-string email", issuer: "issuer", subject: "subject", claims: `{"name":"Member","email":42,"email_verified":true}`},
		{name: "short email", issuer: "issuer", subject: "subject", claims: `{"name":"Member","email":"x","email_verified":true}`},
		{name: "long email", issuer: "issuer", subject: "subject", claims: `{"name":"Member","email":"` + strings.Repeat("e", 321) + `","email_verified":true}`},
		{name: "email control", issuer: "issuer", subject: "subject", claims: `{"name":"Member","email":"member@example.com\n","email_verified":true}`},
		{name: "missing email verification", issuer: "issuer", subject: "subject", claims: `{"name":"Member","email":"member@example.com"}`},
		{name: "invalid email verification", issuer: "issuer", subject: "subject", claims: `{"name":"Member","email":"member@example.com","email_verified":"yes"}`},
		{name: "verification without email has invalid type", issuer: "issuer", subject: "subject", claims: `{"name":"Member","email_verified":"yes"}`},
		{name: "non-string picture", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":42}`},
		{name: "empty picture", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":""}`},
		{name: "long picture", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":"https://example.com/` + strings.Repeat("p", 2040) + `"}`},
		{name: "relative picture", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":"/avatar.png"}`},
		{name: "picture credentials", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":"https://user:pass@example.com/avatar.png"}`},
		{name: "picture fragment", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":"https://example.com/avatar.png#secret"}`},
		{name: "picture empty query", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":"https://example.com/avatar.png?"}`},
		{name: "picture scheme", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":"ftp://example.com/avatar.png"}`},
		{name: "HTTP picture for HTTPS issuer", issuer: "https://auth.example/application/o/gotth-bb/", subject: "subject", claims: `{"name":"Member","picture":"http://example.com/avatar.png"}`},
		{name: "picture control", issuer: "issuer", subject: "subject", claims: `{"name":"Member","picture":"https://example.com/avatar%0A.png"}`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			claims := rawIdentityClaims(t, test.claims)
			if got, err := validateIdentityClaims(test.issuer, test.subject, claims); err == nil || got != (verifiedIdentityClaims{}) {
				t.Fatalf("validateIdentityClaims() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
}

func TestValidateIdentityClaimsRejectsMalformedRawClaim(t *testing.T) {
	t.Parallel()

	claims := map[string]json.RawMessage{"name": json.RawMessage(`"unterminated`)}
	if got, err := validateIdentityClaims("issuer", "subject", claims); err == nil || got != (verifiedIdentityClaims{}) {
		t.Fatalf("validateIdentityClaims() = (%+v, %v), want zero/error", got, err)
	}
}

func rawIdentityClaims(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var claims map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &claims); err != nil {
		t.Fatalf("json.Unmarshal() returned error: %v", err)
	}
	return claims
}
