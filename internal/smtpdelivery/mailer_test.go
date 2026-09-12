package smtpdelivery

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/config"
)

func TestMailerQueuesFixedInvitationThroughSMTP(t *testing.T) {
	mailer, err := New(Settings{Host: "localhost", Port: 2525, From: "board@example.test", TLSMode: config.SMTPPlain, Timeout: 2 * time.Second}, url.URL{Scheme: "https", Host: "auth.example"}, "gotth-bb-invitation")
	if err != nil {
		t.Fatal(err)
	}
	defer mailer.Close()
	client, server := net.Pipe()
	mailer.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	message := make(chan string, 1)
	go serveSMTPConversation(server, message, false, false)
	state, err := mailer.SendInvitation(context.Background(), "invitee@example.test", "Invited Member", "77777777-7777-4777-8777-777777777777", time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC))
	if err != nil || state != "queued" {
		t.Fatalf("delivery = (%q, %v)", state, err)
	}
	raw := <-message
	for _, want := range []string{"From: board@example.test", "To: invitee@example.test", "Hello Invited Member,", "https://auth.example/if/flow/gotth-bb-invitation/?itoken=77777777-7777-4777-8777-777777777777", "2026-09-10T20:00:00Z"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("message missing %q: %q", want, raw)
		}
	}
}

func TestMailerSubmitsFixedSelfAddressedTestMessage(t *testing.T) {
	mailer, err := New(Settings{Host: "localhost", Port: 2525, From: "board@example.test", TLSMode: config.SMTPPlain, Timeout: 2 * time.Second}, url.URL{Scheme: "https", Host: "auth.example"}, "gotth-bb-invitation")
	if err != nil {
		t.Fatal(err)
	}
	defer mailer.Close()
	client, server := net.Pipe()
	mailer.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	message := make(chan string, 1)
	go serveSMTPConversation(server, message, false, false)
	state, err := mailer.SendTest(context.Background(), "administrator@example.test")
	if err != nil || state != "accepted" {
		t.Fatalf("delivery = (%q, %v)", state, err)
	}
	raw := <-message
	for _, want := range []string{
		"From: board@example.test", "To: administrator@example.test",
		"Subject: GOTTH Board email test",
		"This message confirms that GOTTH Board can submit mail through the configured transport.",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("message missing %q: %q", want, raw)
		}
	}
	for _, forbidden := range []string{"itoken", "auth.example", "Invited Member"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("test message contains invitation material %q: %q", forbidden, raw)
		}
	}
}

func TestMailerRejectsInjectedTestRecipientBeforeDial(t *testing.T) {
	mailer, err := New(Settings{Host: "localhost", Port: 2525, From: "board@example.test", TLSMode: config.SMTPPlain, Timeout: time.Second}, url.URL{Scheme: "https", Host: "auth.example"}, "invitation")
	if err != nil {
		t.Fatal(err)
	}
	defer mailer.Close()
	mailer.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dial called for malformed recipient")
		return nil, nil
	}
	if state, err := mailer.SendTest(context.Background(), "admin@example.test\r\nBcc: attacker@example.test"); err == nil || state != "failed" {
		t.Fatalf("injected recipient = (%q, %v)", state, err)
	}
}

func TestMailerClassifiesDefiniteAndAmbiguousFailure(t *testing.T) {
	for _, test := range []struct {
		name, want                     string
		rejectRecipient, failAfterData bool
	}{
		{name: "recipient", want: "failed", rejectRecipient: true},
		{name: "after DATA", want: "unknown", failAfterData: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mailer, err := New(Settings{Host: "localhost", Port: 2525, From: "board@example.test", TLSMode: config.SMTPPlain, Timeout: time.Second}, url.URL{Scheme: "https", Host: "auth.example"}, "invitation")
			if err != nil {
				t.Fatal(err)
			}
			client, server := net.Pipe()
			mailer.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
			go serveSMTPConversation(server, make(chan string, 1), test.rejectRecipient, test.failAfterData)
			state, err := mailer.SendInvitation(context.Background(), "invitee@example.test", "", "77777777-7777-4777-8777-777777777777", time.Now().Add(time.Hour))
			mailer.Close()
			if err == nil || state != test.want {
				t.Fatalf("failure = (%q, %v), want %q", state, err, test.want)
			}
		})
	}
}

func TestImplicitTLSHandshakeUsesTotalSMTPTimeout(t *testing.T) {
	mailer, err := New(Settings{Host: "localhost", Port: 2465, From: "board@example.test", TLSMode: config.SMTPImplicitTLS, Timeout: time.Second}, url.URL{Scheme: "https", Host: "auth.example"}, "invitation")
	if err != nil {
		t.Fatal(err)
	}
	defer mailer.Close()
	client, server := net.Pipe()
	defer server.Close()
	mailer.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }

	started := time.Now()
	state, err := mailer.SendInvitation(context.Background(), "invitee@example.test", "", "77777777-7777-4777-8777-777777777777", time.Now().Add(time.Hour))
	if err == nil || state != "failed" {
		t.Fatalf("stalled TLS handshake = (%q, %v)", state, err)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("stalled TLS handshake elapsed %s", elapsed)
	}
}

func serveSMTPConversation(connection net.Conn, captured chan<- string, rejectRecipient, failAfterData bool) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	_, _ = io.WriteString(connection, "220 test ESMTP\r\n")
	readSMTPLine(reader)
	_, _ = io.WriteString(connection, "250-test\r\n250 PIPELINING\r\n")
	readSMTPLine(reader)
	_, _ = io.WriteString(connection, "250 sender\r\n")
	readSMTPLine(reader)
	if rejectRecipient {
		_, _ = io.WriteString(connection, "550 rejected\r\n")
		return
	}
	_, _ = io.WriteString(connection, "250 recipient\r\n")
	readSMTPLine(reader)
	_, _ = io.WriteString(connection, "354 send data\r\n")
	var data strings.Builder
	for {
		line := readSMTPLine(reader)
		if line == ".\r\n" || line == "" {
			break
		}
		data.WriteString(line)
	}
	captured <- data.String()
	if failAfterData {
		return
	}
	_, _ = io.WriteString(connection, "250 queued\r\n")
	readSMTPLine(reader)
	_, _ = io.WriteString(connection, "221 bye\r\n")
}

func readSMTPLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return line
}
