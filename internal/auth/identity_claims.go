package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

type verifiedIdentityClaims struct {
	issuer      string
	subject     string
	displayName string
	email       *string
	avatarURL   *string
}

// validateIdentityClaims accepts only immutable verified identity coordinates
// and the three approved profile claims. Authorization-shaped claims are never
// decoded into the result, so they cannot become local roles or memberships.
//
// Complexity: for i issuer bytes, s subject bytes, and p approved-claim bytes,
// time is O(i+s+p), Omega(1), and auxiliary space is O(p), Omega(1). Accepted
// fields are individually bounded to the database contract (2,048, 512, 80,
// 320, and 2,048 Unicode code points respectively).
func validateIdentityClaims(issuer, subject string, claims map[string]json.RawMessage) (verifiedIdentityClaims, error) {
	validBoundedText := func(value string, minimum, maximum int) bool {
		if !utf8.ValidString(value) {
			return false
		}
		length := utf8.RuneCountInString(value)
		return length >= minimum && length <= maximum && strings.IndexFunc(value, unicode.IsControl) < 0
	}
	if !validBoundedText(issuer, 1, 2048) || !validBoundedText(subject, 1, 512) {
		return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC issuer or subject is invalid")
	}
	decodeString := func(name string, required bool) (string, bool, error) {
		raw, present := claims[name]
		if !present {
			if required {
				return "", false, fmt.Errorf("verified OIDC %s claim is required", name)
			}
			return "", false, nil
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) < 2 || trimmed[0] != '"' {
			return "", false, fmt.Errorf("verified OIDC %s claim must be a string", name)
		}
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", false, fmt.Errorf("verified OIDC %s claim must be a string", name)
		}
		return value, true, nil
	}
	displayName, _, err := decodeString("name", true)
	if err != nil || !validBoundedText(displayName, 1, 80) {
		return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC name claim is invalid")
	}
	email, emailPresent, err := decodeString("email", false)
	if err != nil {
		return verifiedIdentityClaims{}, err
	}
	emailVerified := false
	if rawVerification, verificationPresent := claims["email_verified"]; verificationPresent {
		trimmed := bytes.TrimSpace(rawVerification)
		if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
			return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC email_verified claim must be a boolean")
		}
		emailVerified = bytes.Equal(trimmed, []byte("true"))
	} else if emailPresent {
		return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC email claim requires email_verified")
	}
	var acceptedEmail *string
	if emailPresent {
		if !validBoundedText(email, 3, 320) {
			return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC email claim is invalid")
		}
		if emailVerified {
			acceptedEmail = &email
		}
	}
	picture, picturePresent, err := decodeString("picture", false)
	if err != nil {
		return verifiedIdentityClaims{}, err
	}
	var acceptedPicture *string
	if picturePresent {
		if !validBoundedText(picture, 1, 2048) {
			return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC picture claim is invalid")
		}
		parsed, err := url.Parse(picture)
		if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Path == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.ForceQuery || parsed.Fragment != "" ||
			strings.IndexFunc(parsed.Path, unicode.IsControl) >= 0 || strings.IndexFunc(parsed.RawQuery, unicode.IsControl) >= 0 || parsed.String() != picture {
			return verifiedIdentityClaims{}, fmt.Errorf("verified OIDC picture claim is not a safe canonical URL")
		}
		acceptedPicture = &picture
	}
	return verifiedIdentityClaims{
		issuer: issuer, subject: subject, displayName: displayName, email: acceptedEmail, avatarURL: acceptedPicture,
	}, nil
}
