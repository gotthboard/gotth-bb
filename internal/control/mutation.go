package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const mutationTimeout = 2 * time.Second

var (
	ErrInput    = errors.New("invalid runtime control settings input")
	ErrDenied   = errors.New("runtime control settings administration denied")
	ErrConflict = errors.New("runtime control settings conflict")
)

type EditableSettings struct {
	Settings Settings
}

type Input struct {
	Registration       RegistrationMode
	MaintenanceEnabled bool
	MaintenanceMessage string
	PublishLimit       uint32
	NewAccountLimit    uint32
	PublishWindow      time.Duration
	NewAccountPeriod   time.Duration
	SessionIdle        time.Duration
	AuthRevalidate     time.Duration
	Revision           int64
	Reason             string
}

type MutationResult struct {
	Revision int64
	AuditID  int64
}

type editableQuerier interface {
	LoadEditableControlSettings(context.Context, db.LoadEditableControlSettingsParams) (db.LoadEditableControlSettingsRow, error)
}

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func LoadEditable(ctx context.Context, querier editableQuerier, actor policy.AccessContext, observedAt time.Time, ceilings Ceilings) (EditableSettings, error) {
	if ctx == nil || querier == nil || !ceilings.Valid() {
		return EditableSettings{}, fmt.Errorf("%w: incomplete boundary", ErrUnavailable)
	}
	if !policy.CanAdminister(actor) {
		return EditableSettings{}, ErrDenied
	}
	if observedAt.IsZero() {
		return EditableSettings{}, fmt.Errorf("%w: invalid time", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return EditableSettings{}, fmt.Errorf("load editable control settings: %w", err)
	}
	row, err := querier.LoadEditableControlSettings(ctx, db.LoadEditableControlSettingsParams{
		ActorUserID: actor.UserID,
		ObservedAt:  finiteTime(observedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EditableSettings{}, ErrDenied
	}
	if err != nil {
		return EditableSettings{}, fmt.Errorf("%w: editable read failed", ErrUnavailable)
	}
	if !row.SettingsPresent {
		return EditableSettings{}, fmt.Errorf("%w: singleton missing", ErrUnavailable)
	}
	settings, err := validateRow(db.LoadRuntimeControlSettingsRow{
		RegistrationMode: row.RegistrationMode, MaintenanceEnabled: row.MaintenanceEnabled,
		MaintenanceMessage: row.MaintenanceMessage, PublishRateLimit: row.PublishRateLimit,
		NewAccountPublishRateLimit: row.NewAccountPublishRateLimit,
		PublishWindowSeconds:       row.PublishWindowSeconds, NewAccountPeriodSeconds: row.NewAccountPeriodSeconds,
		SessionIdleSeconds: row.SessionIdleSeconds, AuthRevalidateSeconds: row.AuthRevalidateSeconds,
		AdministrationRevision: row.AdministrationRevision,
	}, ceilings)
	if err != nil {
		return EditableSettings{}, err
	}
	return EditableSettings{Settings: settings}, nil
}

func Update(ctx context.Context, beginner transactionBeginner, clock func() time.Time, actor policy.AccessContext, input Input, ceilings Ceilings, smtpConfigured bool, requestID pgtype.UUID) (MutationResult, error) {
	if ctx == nil || beginner == nil || clock == nil || !ceilings.Valid() {
		return MutationResult{}, fmt.Errorf("runtime control settings mutation boundary is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return MutationResult{}, ErrDenied
	}
	requested, err := validateInput(input, ceilings, smtpConfigured)
	if err != nil || !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return MutationResult{}, ErrInput
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, fmt.Errorf("update runtime control settings: %w", err)
	}
	observedAt := clock()
	if observedAt.IsZero() {
		return MutationResult{}, fmt.Errorf("runtime control settings clock returned zero time")
	}
	observedAt = observedAt.UTC().Truncate(time.Microsecond)
	mutationContext, cancel := context.WithTimeout(ctx, mutationTimeout)
	defer cancel()

	result := MutationResult{}
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if _, configureErr := queries.ConfigureAdministrationTransaction(mutationContext); configureErr != nil {
			return fmt.Errorf("configure control settings transaction: %w", configureErr)
		}
		lockedActor, lockErr := queries.LockControlSettingsAdministrator(mutationContext, db.LockControlSettingsAdministratorParams{
			ActorUserID: actor.UserID, ObservedAt: finiteTime(observedAt),
		})
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ErrDenied
		}
		if lockErr != nil {
			return fmt.Errorf("lock control settings administrator: %w", lockErr)
		}
		if lockedActor != actor.UserID {
			return fmt.Errorf("control settings administrator lock returned invalid state")
		}
		current, lockErr := queries.LockRuntimeControlSettings(mutationContext)
		if lockErr != nil {
			return fmt.Errorf("%w: lock singleton", ErrUnavailable)
		}
		currentSettings, validateErr := validateRow(db.LoadRuntimeControlSettingsRow{
			RegistrationMode: current.RegistrationMode, MaintenanceEnabled: current.MaintenanceEnabled,
			MaintenanceMessage: current.MaintenanceMessage, PublishRateLimit: current.PublishRateLimit,
			NewAccountPublishRateLimit: current.NewAccountPublishRateLimit,
			PublishWindowSeconds:       current.PublishWindowSeconds, NewAccountPeriodSeconds: current.NewAccountPeriodSeconds,
			SessionIdleSeconds: current.SessionIdleSeconds, AuthRevalidateSeconds: current.AuthRevalidateSeconds,
			AdministrationRevision: current.AdministrationRevision,
		}, ceilings)
		if validateErr != nil {
			return validateErr
		}
		if currentSettings.Revision != input.Revision || currentSettings.Revision == int64(^uint64(0)>>1) {
			return ErrConflict
		}
		if sameSettings(currentSettings, requested) {
			return ErrConflict
		}
		changed, updateErr := queries.UpdateControlSettingsAndAudit(mutationContext, db.UpdateControlSettingsAndAuditParams{
			RegistrationMode: string(input.Registration), MaintenanceEnabled: input.MaintenanceEnabled,
			MaintenanceMessage: input.MaintenanceMessage, PublishRateLimit: int32(input.PublishLimit),
			NewAccountPublishRateLimit: int32(input.NewAccountLimit), PublishWindowSeconds: int32(input.PublishWindow / time.Second),
			NewAccountPeriodSeconds: int32(input.NewAccountPeriod / time.Second), SessionIdleSeconds: int32(input.SessionIdle / time.Second),
			AuthRevalidateSeconds: int32(input.AuthRevalidate / time.Second), ObservedAt: finiteTime(observedAt),
			ExpectedRevision: input.Revision, ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true},
			Reason:                   pgtype.Text{String: input.Reason, Valid: true},
			PreviousRegistrationMode: current.RegistrationMode, PreviousMaintenanceEnabled: current.MaintenanceEnabled,
			PreviousMaintenanceMessageSha256: digest(current.MaintenanceMessage), PreviousPublishRateLimit: current.PublishRateLimit,
			PreviousNewAccountPublishRateLimit: current.NewAccountPublishRateLimit, PreviousPublishWindowSeconds: current.PublishWindowSeconds,
			PreviousNewAccountPeriodSeconds: current.NewAccountPeriodSeconds, PreviousSessionIdleSeconds: current.SessionIdleSeconds,
			PreviousAuthRevalidateSeconds: current.AuthRevalidateSeconds, MaintenanceMessageSha256: digest(input.MaintenanceMessage),
			RequestID: requestID,
		})
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if updateErr != nil {
			return fmt.Errorf("update control settings and audit failed")
		}
		if changed.AdministrationRevision != input.Revision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("control settings update returned invalid state")
		}
		result = MutationResult{Revision: changed.AdministrationRevision, AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return MutationResult{}, fmt.Errorf("runtime control settings transaction: %w", err)
	}
	return result, nil
}

func validateInput(input Input, ceilings Ceilings, smtpConfigured bool) (Settings, error) {
	if !validReason(input.Reason) || input.Revision <= 0 ||
		input.PublishLimit > uint32(^uint32(0)>>1) || input.NewAccountLimit > uint32(^uint32(0)>>1) ||
		input.PublishWindow < time.Second || input.PublishWindow > ceilings.PublishWindow ||
		input.NewAccountPeriod < time.Minute || input.NewAccountPeriod > ceilings.NewAccountPeriod ||
		input.SessionIdle < time.Second || input.SessionIdle > ceilings.SessionIdle ||
		input.AuthRevalidate < time.Second || input.AuthRevalidate > ceilings.AuthRevalidate ||
		input.PublishWindow%time.Second != 0 || input.NewAccountPeriod%time.Second != 0 ||
		input.SessionIdle%time.Second != 0 || input.AuthRevalidate%time.Second != 0 {
		return Settings{}, ErrInput
	}
	row := db.LoadRuntimeControlSettingsRow{
		RegistrationMode: string(input.Registration), MaintenanceEnabled: input.MaintenanceEnabled,
		MaintenanceMessage: input.MaintenanceMessage, PublishRateLimit: int32(input.PublishLimit),
		NewAccountPublishRateLimit: int32(input.NewAccountLimit), PublishWindowSeconds: int32(input.PublishWindow / time.Second),
		NewAccountPeriodSeconds: int32(input.NewAccountPeriod / time.Second), SessionIdleSeconds: int32(input.SessionIdle / time.Second),
		AuthRevalidateSeconds: int32(input.AuthRevalidate / time.Second), AdministrationRevision: input.Revision,
	}
	settings, err := validateRow(row, ceilings)
	if err != nil {
		return Settings{}, ErrInput
	}
	if settings.Registration != RegistrationClosed && !smtpConfigured {
		return Settings{}, ErrInput
	}
	return settings, nil
}

func sameSettings(left, right Settings) bool {
	return left.Registration == right.Registration &&
		left.MaintenanceEnabled == right.MaintenanceEnabled &&
		left.MaintenanceMessage == right.MaintenanceMessage &&
		left.Publication == right.Publication && left.SessionIdle == right.SessionIdle &&
		left.AuthRevalidate == right.AuthRevalidate
}

func validReason(value string) bool {
	return len(value) >= 1 && len(value) <= 2000 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func finiteTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: true}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
