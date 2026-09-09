package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
)

const approvalFlowTestUUID = "8d29e230-3485-4ee6-a741-ec089e510002"
const approvalUserTestUUID = "77777777-7777-4777-8777-777777777777"

func TestVerifyApprovalIntakePinsSignedClaims(t *testing.T) {
	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	now := time.Now().UTC().Truncate(time.Second)
	service := &Service{provider: harness.discover(t), clock: func() time.Time { return now }}
	raw := signedApprovalAssertion(t, harness, now, map[string]any{})
	got, err := service.VerifyApprovalIntake(context.Background(), raw, approvalFlowTestUUID)
	if err != nil || got.AuthentikUserID != 17 || got.Subject != approvalUserTestUUID ||
		got.Username != "member" || got.DisplayName != "Member" || got.VerifiedEmail != "member@example.test" {
		t.Fatalf("VerifyApprovalIntake() = (%+v, %v)", got, err)
	}
}

func TestVerifyApprovalIntakeRejectsUntrustedOrMalformedClaims(t *testing.T) {
	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	now := time.Now().UTC().Truncate(time.Second)
	service := &Service{provider: harness.discover(t), clock: func() time.Time { return now }}
	tests := []struct {
		name   string
		claims map[string]any
	}{
		{"purpose", map[string]any{"purpose": "other"}},
		{"flow", map[string]any{"flow_uuid": "11111111-1111-4111-8111-111111111111"}},
		{"user ID", map[string]any{"user_id": 0}},
		{"subject", map[string]any{"sub": "not-a-uuid"}},
		{"unverified email", map[string]any{"email_verified": false}},
		{"named email", map[string]any{"email": "Member <member@example.test>"}},
		{"long expiry", map[string]any{"exp": now.Add(61 * time.Second).Unix()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := signedApprovalAssertion(t, harness, now, test.claims)
			if got, err := service.VerifyApprovalIntake(context.Background(), raw, approvalFlowTestUUID); err == nil || got.AuthentikUserID != 0 {
				t.Fatalf("VerifyApprovalIntake() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
	if got, err := service.VerifyApprovalIntake(context.Background(), strings.Repeat("x", maximumApprovalAssertionBytes+1), approvalFlowTestUUID); err == nil || got.AuthentikUserID != 0 {
		t.Fatalf("oversized assertion = (%+v, %v)", got, err)
	}
}

func signedApprovalAssertion(t *testing.T, harness *oidcExchangeHarness, now time.Time, overrides map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": harness.issuer, "aud": "gotth-bb", "iat": now.Unix(), "exp": now.Add(60 * time.Second).Unix(),
		"sub": approvalUserTestUUID, "purpose": "gotth-bb-approval-intake", "flow_uuid": approvalFlowTestUUID,
		"user_id": 17, "preferred_username": "member", "name": "Member",
		"email": "member@example.test", "email_verified": true,
	}
	for name, value := range overrides {
		claims[name] = value
	}
	raw, err := jwt.Signed(harness.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
