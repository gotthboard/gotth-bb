package smtpdelivery

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotthboard/gotth-bb/internal/config"
	"golang.org/x/sys/unix"
)

const maximumPasswordBytes = 4096

type Mailer struct {
	configuration Settings
	password      []byte
	flowBase      url.URL
	dial          func(context.Context, string, string) (net.Conn, error)
}

type Settings struct {
	Host, Username, From, PasswordFile string
	Port                               uint16
	TLSMode                            config.SMTPTLSMode
	Timeout                            time.Duration
}

func New(configuration Settings, issuer url.URL, flowSlug string) (*Mailer, error) {
	if configuration.Host == "" || configuration.Port == 0 || configuration.From == "" || configuration.Timeout < time.Second || configuration.Timeout > 30*time.Second ||
		(configuration.TLSMode != config.SMTPStartTLS && configuration.TLSMode != config.SMTPImplicitTLS && configuration.TLSMode != config.SMTPPlain) ||
		strings.ContainsAny(configuration.Host+configuration.Username+configuration.From, "\r\n\x00") ||
		(configuration.Username == "") != (configuration.PasswordFile == "") ||
		issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || flowSlug == "" || strings.ContainsAny(flowSlug, "/?#") {
		return nil, fmt.Errorf("SMTP invitation delivery is invalid")
	}
	var password []byte
	var err error
	if configuration.Username != "" {
		password, err = loadPassword(configuration.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("SMTP invitation delivery is invalid")
		}
	}
	flowBase := url.URL{Scheme: issuer.Scheme, Host: issuer.Host, Path: "/if/flow/" + flowSlug + "/"}
	dialer := &net.Dialer{Timeout: configuration.Timeout}
	return &Mailer{
		configuration: configuration, password: password, flowBase: flowBase,
		dial: dialer.DialContext,
	}, nil
}

func (mailer *Mailer) Close() {
	if mailer != nil {
		clear(mailer.password)
		mailer.password = nil
	}
}

func (mailer *Mailer) SendInvitation(ctx context.Context, recipient, displayName, tokenUUID string, expires time.Time) (string, error) {
	if mailer == nil || ctx == nil || recipient == "" || strings.ContainsAny(recipient, "\r\n") || strings.ContainsAny(displayName, "\r\n") || tokenUUID == "" || strings.ContainsAny(tokenUUID, "\r\n/?#") || expires.IsZero() {
		return "failed", fmt.Errorf("invitation email input is invalid")
	}
	operationContext, cancel := context.WithTimeout(ctx, mailer.configuration.Timeout)
	defer cancel()
	address := net.JoinHostPort(mailer.configuration.Host, fmt.Sprintf("%d", mailer.configuration.Port))
	connection, err := mailer.dialConnection(operationContext, address)
	if err != nil {
		return "failed", fmt.Errorf("SMTP connection failed")
	}
	defer connection.Close()
	deadline, _ := operationContext.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return "failed", fmt.Errorf("SMTP deadline failed")
	}
	client, err := smtp.NewClient(connection, mailer.configuration.Host)
	if err != nil {
		return "failed", fmt.Errorf("SMTP greeting failed")
	}
	defer client.Close()
	if mailer.configuration.TLSMode == config.SMTPStartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: mailer.configuration.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return "failed", fmt.Errorf("SMTP STARTTLS failed")
		}
	}
	if mailer.configuration.Username != "" {
		auth := smtp.PlainAuth("", mailer.configuration.Username, string(mailer.password), mailer.configuration.Host)
		if err := client.Auth(auth); err != nil {
			return "failed", fmt.Errorf("SMTP authentication failed")
		}
	}
	if err := client.Mail(mailer.configuration.From); err != nil {
		return "failed", fmt.Errorf("SMTP sender rejected")
	}
	if err := client.Rcpt(recipient); err != nil {
		return "failed", fmt.Errorf("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return "failed", fmt.Errorf("SMTP DATA rejected")
	}
	message := mailer.message(recipient, displayName, tokenUUID, expires)
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return "unknown", fmt.Errorf("SMTP DATA write ambiguous")
	}
	if err := writer.Close(); err != nil {
		return "unknown", fmt.Errorf("SMTP DATA acceptance ambiguous")
	}
	_ = client.Quit()
	return "queued", nil
}

func (mailer *Mailer) dialConnection(ctx context.Context, address string) (net.Conn, error) {
	if mailer.configuration.TLSMode != config.SMTPImplicitTLS {
		return mailer.dial(ctx, "tcp", address)
	}
	raw, err := mailer.dial(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || raw.SetDeadline(deadline) != nil {
		raw.Close()
		return nil, fmt.Errorf("SMTP TLS deadline failed")
	}
	connection := tls.Client(raw, &tls.Config{ServerName: mailer.configuration.Host, MinVersion: tls.VersionTLS12})
	if err := connection.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return connection, nil
}

func (mailer *Mailer) message(recipient, displayName, tokenUUID string, expires time.Time) string {
	invitation := mailer.flowBase
	invitation.RawQuery = url.Values{"itoken": {tokenUUID}}.Encode()
	greeting := "Hello,"
	if displayName != "" {
		greeting = "Hello " + displayName + ","
	}
	return "From: " + mailer.configuration.From + "\r\n" +
		"To: " + recipient + "\r\n" +
		"Subject: GOTTH Board invitation\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" +
		greeting + "\r\n\r\nUse this single-use link to join GOTTH Board:\r\n" + invitation.String() +
		"\r\n\r\nThis invitation expires at " + expires.UTC().Format(time.RFC3339) + ".\r\n"
}

func loadPassword(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("invalid path")
	}
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, strings.TrimPrefix(path, "/"), &unix.OpenHow{Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW), Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS)})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "SMTP password")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid descriptor")
	}
	defer file.Close()
	var status unix.Stat_t
	writeErr := unix.Faccessat(fd, "", unix.W_OK, unix.AT_EMPTY_PATH|unix.AT_EACCESS)
	if err := unix.Fstat(fd, &status); err != nil || status.Mode&unix.S_IFMT != unix.S_IFREG || status.Mode&0o022 != 0 || writeErr == nil || !errors.Is(writeErr, unix.EACCES) {
		return nil, errors.New("mutable password file")
	}
	raw, err := io.ReadAll(io.LimitReader(bufio.NewReader(file), maximumPasswordBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maximumPasswordBytes || strings.ContainsAny(string(raw), "\r\n\x00") || strings.TrimSpace(string(raw)) != string(raw) {
		clear(raw)
		return nil, errors.New("invalid password")
	}
	return raw, nil
}
