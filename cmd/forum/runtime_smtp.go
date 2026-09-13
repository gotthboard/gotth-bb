package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	administrationservice "github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/config"
	"github.com/gotthboard/gotth-bb/internal/smtpdelivery"
	"github.com/gotthboard/gotth-bb/internal/store/db"
)

type runtimeSMTPMailer struct {
	queries  *db.Queries
	key      [32]byte
	issuer   url.URL
	flowSlug string
}

func (runtime *runtimeSMTPMailer) SMTPRevision(ctx context.Context) (int64, error) {
	settings, err := runtime.load(ctx, false)
	if err != nil {
		return 0, err
	}
	defer clear(settings.Password)
	return settings.Revision, nil
}

func (runtime *runtimeSMTPMailer) SendTest(ctx context.Context, recipient string, revision int64) (string, error) {
	settings, err := runtime.load(ctx, false)
	if err != nil || settings.Revision != revision {
		clear(settings.Password)
		return "failed", fmt.Errorf("SMTP settings changed or are unavailable")
	}
	defer clear(settings.Password)
	mailer, err := runtime.mailer(settings)
	if err != nil {
		return "failed", err
	}
	defer mailer.Close()
	return mailer.SendTest(ctx, recipient)
}

func (runtime *runtimeSMTPMailer) SendInvitation(ctx context.Context, recipient, displayName, tokenUUID string, expires time.Time) (string, error) {
	settings, err := runtime.load(ctx, true)
	if err != nil {
		clear(settings.Password)
		return "failed", fmt.Errorf("verified SMTP settings are unavailable")
	}
	defer clear(settings.Password)
	mailer, err := runtime.mailer(settings)
	if err != nil {
		return "failed", err
	}
	defer mailer.Close()
	return mailer.SendInvitation(ctx, recipient, displayName, tokenUUID, expires)
}

func (runtime *runtimeSMTPMailer) load(ctx context.Context, requireVerified bool) (administrationservice.SMTPRuntimeSettings, error) {
	if runtime == nil || runtime.queries == nil || runtime.key == ([32]byte{}) {
		return administrationservice.SMTPRuntimeSettings{}, fmt.Errorf("SMTP runtime is incomplete")
	}
	settings, err := administrationservice.LoadRuntimeSMTPSettings(ctx, runtime.queries, runtime.key)
	if err != nil || settings.Host == "" || requireVerified && !settings.Verified {
		clear(settings.Password)
		return administrationservice.SMTPRuntimeSettings{}, fmt.Errorf("SMTP runtime is unavailable")
	}
	return settings, nil
}

func (runtime *runtimeSMTPMailer) mailer(settings administrationservice.SMTPRuntimeSettings) (*smtpdelivery.Mailer, error) {
	return smtpdelivery.NewWithPassword(smtpdelivery.Settings{
		Host: settings.Host, Port: uint16(settings.Port), Username: settings.Username, From: settings.FromAddress,
		TLSMode: config.SMTPTLSMode(settings.TLSMode), Timeout: time.Duration(settings.TimeoutSeconds) * time.Second,
	}, settings.Password, runtime.issuer, runtime.flowSlug)
}
