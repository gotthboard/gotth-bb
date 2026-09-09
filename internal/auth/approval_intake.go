package auth

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gotthboard/gotth-bb/internal/registration"
)

const maximumApprovalAssertionBytes = 8 << 10

var approvalSubject = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// VerifyApprovalIntake verifies one short-lived assertion with the same pinned
// provider and bounded HTTP client used for login. Signature, issuer,
// audience, expiry, purpose, flow, and the closed identity projection must all
// agree before any database work is possible.
func (service *Service) VerifyApprovalIntake(ctx context.Context, raw, expectedFlow string) (registration.Intake, error) {
	if service == nil || service.provider.verifier == nil || service.provider.httpClient == nil || service.clock == nil ||
		ctx == nil || !approvalSubject.MatchString(expectedFlow) || len(raw) == 0 || len(raw) > maximumApprovalAssertionBytes ||
		!utf8.ValidString(raw) || strings.IndexFunc(raw, func(character rune) bool { return unicode.IsSpace(character) || unicode.IsControl(character) }) >= 0 {
		return registration.Intake{}, fmt.Errorf("approval intake boundary is invalid")
	}
	if err := ctx.Err(); err != nil {
		return registration.Intake{}, fmt.Errorf("verify approval intake: %w", err)
	}
	now := service.clock().UTC().Truncate(time.Second)
	if now.IsZero() {
		return registration.Intake{}, fmt.Errorf("approval intake clock returned zero time")
	}
	token, err := service.provider.verifier.Verify(oidc.ClientContext(ctx, service.provider.httpClient), raw)
	if err != nil {
		return registration.Intake{}, fmt.Errorf("approval assertion verification failed")
	}
	if !token.Expiry.After(now) || token.Expiry.After(now.Add(60*time.Second)) || token.IssuedAt.After(now.Add(time.Second)) ||
		!approvalSubject.MatchString(token.Subject) {
		return registration.Intake{}, fmt.Errorf("approval assertion lifetime or subject is invalid")
	}
	var claims struct {
		Purpose           string `json:"purpose"`
		FlowUUID          string `json:"flow_uuid"`
		UserID            int64  `json:"user_id"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
	}
	if err := token.Claims(&claims); err != nil || claims.Purpose != "gotth-bb-approval-intake" ||
		claims.FlowUUID != expectedFlow || claims.UserID <= 0 || !claims.EmailVerified ||
		!validApprovalText(claims.PreferredUsername, 1, 150, 150) ||
		!validApprovalText(claims.Name, 1, 320, 80) || !validApprovalText(claims.Email, 3, 320, 320) ||
		!addressOnly(claims.Email) {
		return registration.Intake{}, fmt.Errorf("approval assertion claims are invalid")
	}
	return registration.Intake{
		AuthentikUserID: claims.UserID, Subject: token.Subject,
		Username: claims.PreferredUsername, DisplayName: claims.Name, VerifiedEmail: claims.Email,
	}, nil
}

func validApprovalText(value string, minimumBytes, maximumBytes, maximumRunes int) bool {
	if !utf8.ValidString(value) || len(value) < minimumBytes || len(value) > maximumBytes || utf8.RuneCountInString(value) > maximumRunes || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func addressOnly(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Name == "" && address.Address == value
}
