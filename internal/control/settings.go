// Package control owns the typed, database-backed B1-09 runtime control
// settings and their immutable startup ceilings.
package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

var ErrUnavailable = errors.New("runtime control settings unavailable")

type RegistrationMode string

const (
	RegistrationClosed                RegistrationMode = "closed"
	RegistrationVerifiedEmailOpen     RegistrationMode = "verified_email_open"
	RegistrationAdministratorApproval RegistrationMode = "administrator_approval"
	RegistrationInvitationOnly        RegistrationMode = "invitation_only"
)

type Ceilings struct {
	PublishLimit      uint32
	NewAccountLimit   uint32
	PublishWindow     time.Duration
	NewAccountPeriod  time.Duration
	SessionIdle       time.Duration
	AuthRevalidate    time.Duration
	SessionMaximumAge time.Duration
}

type Settings struct {
	Registration       RegistrationMode
	MaintenanceEnabled bool
	MaintenanceMessage string
	Publication        abuse.PublicationPolicy
	SessionIdle        time.Duration
	AuthRevalidate     time.Duration
	Revision           int64
}

type settingsQuerier interface {
	LoadRuntimeControlSettings(context.Context) (db.LoadRuntimeControlSettingsRow, error)
}

func (mode RegistrationMode) Valid() bool {
	switch mode {
	case RegistrationClosed, RegistrationVerifiedEmailOpen,
		RegistrationAdministratorApproval, RegistrationInvitationOnly:
		return true
	default:
		return false
	}
}

func (ceilings Ceilings) Valid() bool {
	_, err := abuse.NewPublicationPolicy(
		ceilings.PublishLimit, ceilings.NewAccountLimit,
		ceilings.PublishWindow, ceilings.NewAccountPeriod,
	)
	return err == nil && ceilings.SessionMaximumAge >= time.Second &&
		ceilings.SessionIdle >= time.Second && ceilings.SessionIdle <= ceilings.SessionMaximumAge &&
		ceilings.AuthRevalidate >= time.Second && ceilings.AuthRevalidate <= ceilings.SessionMaximumAge
}

func Load(ctx context.Context, querier settingsQuerier, ceilings Ceilings) (Settings, error) {
	if ctx == nil || querier == nil || !ceilings.Valid() {
		return Settings{}, fmt.Errorf("%w: incomplete boundary", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return Settings{}, fmt.Errorf("load runtime control settings: %w", err)
	}
	row, err := querier.LoadRuntimeControlSettings(ctx)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return Settings{}, fmt.Errorf("load runtime control settings: %w", contextError)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return Settings{}, fmt.Errorf("%w: singleton missing", ErrUnavailable)
		}
		return Settings{}, fmt.Errorf("%w: database read failed", ErrUnavailable)
	}
	return validateRow(row, ceilings)
}

func validateRow(row db.LoadRuntimeControlSettingsRow, ceilings Ceilings) (Settings, error) {
	mode := RegistrationMode(row.RegistrationMode)
	if !mode.Valid() || !validMessage(row.MaintenanceMessage) || row.AdministrationRevision <= 0 ||
		row.PublishRateLimit <= 0 || row.NewAccountPublishRateLimit <= 0 ||
		row.PublishWindowSeconds <= 0 || row.NewAccountPeriodSeconds <= 0 ||
		row.SessionIdleSeconds <= 0 || row.AuthRevalidateSeconds <= 0 {
		return Settings{}, fmt.Errorf("%w: malformed value", ErrUnavailable)
	}
	publishLimit := uint32(row.PublishRateLimit)
	newAccountLimit := uint32(row.NewAccountPublishRateLimit)
	publishWindow := time.Duration(row.PublishWindowSeconds) * time.Second
	newAccountPeriod := time.Duration(row.NewAccountPeriodSeconds) * time.Second
	sessionIdle := time.Duration(row.SessionIdleSeconds) * time.Second
	authRevalidate := time.Duration(row.AuthRevalidateSeconds) * time.Second
	if publishLimit > ceilings.PublishLimit || newAccountLimit > ceilings.NewAccountLimit ||
		publishWindow > ceilings.PublishWindow || newAccountPeriod > ceilings.NewAccountPeriod ||
		sessionIdle > ceilings.SessionIdle || authRevalidate > ceilings.AuthRevalidate ||
		sessionIdle > ceilings.SessionMaximumAge || authRevalidate > ceilings.SessionMaximumAge {
		return Settings{}, fmt.Errorf("%w: startup ceiling exceeded", ErrUnavailable)
	}
	publication, err := abuse.NewPublicationPolicy(publishLimit, newAccountLimit, publishWindow, newAccountPeriod)
	if err != nil {
		return Settings{}, fmt.Errorf("%w: malformed publication policy", ErrUnavailable)
	}
	return Settings{
		Registration: mode, MaintenanceEnabled: row.MaintenanceEnabled,
		MaintenanceMessage: row.MaintenanceMessage, Publication: publication,
		SessionIdle: sessionIdle, AuthRevalidate: authRevalidate,
		Revision: row.AdministrationRevision,
	}, nil
}

func validMessage(value string) bool {
	return len(value) <= 1120 && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 280 &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}
