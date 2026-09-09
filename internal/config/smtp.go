package config

import (
	"fmt"
	"net/mail"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type SMTPTLSMode string

const (
	SMTPStartTLS    SMTPTLSMode = "starttls"
	SMTPImplicitTLS SMTPTLSMode = "implicit_tls"
	SMTPPlain       SMTPTLSMode = "plain"
)

type SMTPConfig struct {
	Host         string
	Port         uint16
	Username     string
	From         string
	TLSMode      SMTPTLSMode
	Timeout      time.Duration
	PasswordFile string
	configured   bool
}

func (configured SMTPConfig) Configured() bool { return configured.configured }

var smtpDNSName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)

func loadSMTPConfig(lookup LookupEnv, environment Environment) (SMTPConfig, error) {
	names := []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_FROM", "SMTP_TLS_MODE", "SMTP_TIMEOUT"}
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, present := lookup(name)
		if !present {
			return SMTPConfig{}, fmt.Errorf("%s is required", name)
		}
		values[name] = value
	}
	passwordFile, passwordPresent := lookup("SMTP_PASSWORD_FILE")
	disabled := true
	for _, value := range values {
		disabled = disabled && value == ""
	}
	if disabled {
		if passwordPresent && passwordFile != "" {
			return SMTPConfig{}, fmt.Errorf("SMTP_PASSWORD_FILE must be absent when SMTP is disabled")
		}
		return SMTPConfig{}, nil
	}
	host := values["SMTP_HOST"]
	if !validSMTPHost(host) {
		return SMTPConfig{}, fmt.Errorf("SMTP_HOST is invalid")
	}
	portValue, err := strconv.ParseUint(values["SMTP_PORT"], 10, 16)
	if err != nil || portValue == 0 || strconv.FormatUint(portValue, 10) != values["SMTP_PORT"] {
		return SMTPConfig{}, fmt.Errorf("SMTP_PORT must be canonical decimal from 1 through 65535")
	}
	username := values["SMTP_USERNAME"]
	if !validSMTPText(username, 320) {
		return SMTPConfig{}, fmt.Errorf("SMTP_USERNAME is invalid")
	}
	from := values["SMTP_FROM"]
	address, err := mail.ParseAddress(from)
	if err != nil || address.Name != "" || address.Address != from || !validSMTPText(from, 320) {
		return SMTPConfig{}, fmt.Errorf("SMTP_FROM must be one address-only mailbox")
	}
	tlsMode := SMTPTLSMode(values["SMTP_TLS_MODE"])
	if tlsMode != SMTPStartTLS && tlsMode != SMTPImplicitTLS && tlsMode != SMTPPlain || environment == EnvironmentProduction && tlsMode == SMTPPlain {
		return SMTPConfig{}, fmt.Errorf("SMTP_TLS_MODE is invalid")
	}
	timeout, err := parsePositiveDuration("SMTP_TIMEOUT", values["SMTP_TIMEOUT"])
	if err != nil || timeout < time.Second || timeout > 30*time.Second {
		return SMTPConfig{}, fmt.Errorf("SMTP_TIMEOUT must be from 1s through 30s")
	}
	if username == "" {
		if passwordPresent && passwordFile != "" {
			return SMTPConfig{}, fmt.Errorf("SMTP_PASSWORD_FILE requires SMTP_USERNAME")
		}
		passwordFile = ""
	} else {
		if !passwordPresent || passwordFile == "" {
			return SMTPConfig{}, fmt.Errorf("SMTP_PASSWORD_FILE is required with SMTP_USERNAME")
		}
		passwordFile, err = ParseAuthentikControlFile("SMTP_PASSWORD_FILE", passwordFile)
		if err != nil {
			return SMTPConfig{}, err
		}
	}
	return SMTPConfig{
		Host: host, Port: uint16(portValue), Username: username, From: from,
		TLSMode: tlsMode, Timeout: timeout, PasswordFile: passwordFile, configured: true,
	}, nil
}

func validSMTPHost(value string) bool {
	if value == "" || len(value) > 253 || strings.ToLower(value) != value || strings.ContainsAny(value, "[]%") {
		return false
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.String() == value
	}
	return smtpDNSName.MatchString(value)
}

func validSMTPText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}
