package administration

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/authentikcontrol"
	"github.com/gotthboard/gotth-bb/internal/config"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/registration"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const smtpEnvelopeVersion byte = 1

var smtpHostName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)

type SMTPSettings struct {
	Host, Username, FromAddress, TLSMode string
	Port, TimeoutSeconds                 int
	PasswordPresent, Verified            bool
	Revision                             int64
}

type SMTPRuntimeSettings struct {
	SMTPSettings
	Password []byte
}

type SMTPSettingsInput struct {
	Host, Port, Username, FromAddress, TLSMode, TimeoutSeconds string
	PasswordAction, Reason                                     string
	Password                                                   []byte
	ExpectedRevision                                           int64
}

type SMTPSettingsResult struct {
	Revision int64
	AuditID  int64
}

type smtpSettingsQuerier interface {
	LoadEditableSMTPSettings(context.Context, db.LoadEditableSMTPSettingsParams) (db.LoadEditableSMTPSettingsRow, error)
}

type smtpSettingsGateway interface {
	ConfigureEmail(context.Context, authentikcontrol.EmailSettings) (authentikcontrol.EmailStage, error)
}

// LoadSMTPCredentialKey loads the distinct installation key used only for the
// administrator-managed SMTP password envelope.
func LoadSMTPCredentialKey(path string) ([32]byte, error) {
	key, err := registration.LoadFingerprintKey(path)
	if err != nil {
		return [32]byte{}, fmt.Errorf("SMTP credential key is invalid")
	}
	return key, nil
}

func LoadSMTPSettings(ctx context.Context, querier smtpSettingsQuerier, actor policy.AccessContext, observedAt time.Time) (SMTPSettings, error) {
	if ctx == nil || querier == nil || observedAt.IsZero() {
		return SMTPSettings{}, ErrAccountAdministrationInput
	}
	if !policy.CanAdminister(actor) {
		return SMTPSettings{}, ErrAccountAdministrationDenied
	}
	row, err := querier.LoadEditableSMTPSettings(ctx, db.LoadEditableSMTPSettingsParams{ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt)})
	if err != nil {
		return SMTPSettings{}, fmt.Errorf("%w: load SMTP settings", ErrAccountAdministrationUnavailable)
	}
	if !row.ActorPresent {
		return SMTPSettings{}, ErrAccountAdministrationDenied
	}
	if !row.SettingsPresent || row.AdministrationRevision < 1 {
		return SMTPSettings{}, malformedAdministrationRows("SMTP settings")
	}
	settings := SMTPSettings{
		Host: row.Host, Port: int(row.Port), Username: row.Username, FromAddress: row.FromAddress,
		TLSMode: row.TlsMode, TimeoutSeconds: int(row.TimeoutSeconds), PasswordPresent: row.PasswordPresent,
		Revision: row.AdministrationRevision,
	}
	settings.Verified = row.VerifiedRevision.Valid && row.VerifiedRevision.Int64 == settings.Revision && settings.Host != ""
	if err := validateStoredSMTP(settings); err != nil {
		return SMTPSettings{}, malformedAdministrationRows("SMTP settings")
	}
	return settings, nil
}

func LoadRuntimeSMTPSettings(ctx context.Context, queries *db.Queries, key [32]byte) (SMTPRuntimeSettings, error) {
	if ctx == nil || queries == nil || key == ([32]byte{}) {
		return SMTPRuntimeSettings{}, fmt.Errorf("SMTP runtime boundary is incomplete")
	}
	row, err := queries.LoadRuntimeSMTPSettings(ctx)
	if err != nil {
		return SMTPRuntimeSettings{}, fmt.Errorf("load SMTP runtime settings: %w", err)
	}
	return runtimeSMTPSettings(row.Host, row.Username, row.FromAddress, row.TlsMode, int(row.Port), int(row.TimeoutSeconds), row.PasswordEnvelope, row.AdministrationRevision, row.VerifiedRevision, key)
}

func SMTPReady(ctx context.Context, queries *db.Queries, key [32]byte) (bool, error) {
	if ctx == nil || queries == nil || key == ([32]byte{}) {
		return false, fmt.Errorf("SMTP readiness boundary is incomplete")
	}
	settings, err := LoadRuntimeSMTPSettings(ctx, queries, key)
	if err != nil {
		return false, fmt.Errorf("load SMTP readiness: %w", err)
	}
	defer clear(settings.Password)
	return settings.Host != "" && settings.Verified, nil
}

func UpdateSMTPSettings(ctx context.Context, beginner accountTransactionBeginner, gateway smtpSettingsGateway, clock func() time.Time, randomSource io.Reader, actor policy.AccessContext, input SMTPSettingsInput, key [32]byte, requestID pgtype.UUID) (SMTPSettingsResult, error) {
	defer clear(input.Password)
	if gateway == nil || randomSource == nil || key == ([32]byte{}) {
		return SMTPSettingsResult{}, fmt.Errorf("SMTP settings boundary is incomplete")
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, input.Reason, requestID); err != nil {
		return SMTPSettingsResult{}, err
	}
	validated, err := validateSMTPInput(input)
	if err != nil {
		return SMTPSettingsResult{}, err
	}
	observedAt, err := accountMutationTime(clock)
	if err != nil {
		return SMTPSettingsResult{}, err
	}
	mutationContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result SMTPSettingsResult
	err = store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(mutationContext, queries); err != nil {
			return err
		}
		lockedActor, actorErr := queries.LockControlSettingsAdministrator(mutationContext, db.LockControlSettingsAdministratorParams{ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt)})
		if errors.Is(actorErr, pgx.ErrNoRows) {
			return ErrAccountAdministrationDenied
		}
		if actorErr != nil {
			return fmt.Errorf("lock SMTP administrator: %w", actorErr)
		}
		if lockedActor != actor.UserID {
			return fmt.Errorf("lock SMTP administrator returned invalid state")
		}
		controlState, controlErr := queries.LockRuntimeControlSettings(mutationContext)
		if controlErr != nil || controlState.RegistrationMode != "closed" {
			return fmt.Errorf("%w: registration must be closed while SMTP settings change", ErrAccountAdministrationConflict)
		}
		row, lockErr := queries.LockRuntimeSMTPSettings(mutationContext)
		if lockErr != nil {
			return fmt.Errorf("lock SMTP settings: %w", lockErr)
		}
		if row.AdministrationRevision != input.ExpectedRevision {
			return ErrAccountAdministrationConflict
		}
		current, runtimeErr := runtimeSMTPSettings(row.Host, row.Username, row.FromAddress, row.TlsMode, int(row.Port), int(row.TimeoutSeconds), row.PasswordEnvelope, row.AdministrationRevision, row.VerifiedRevision, key)
		if runtimeErr != nil {
			return fmt.Errorf("load current SMTP settings: %w", runtimeErr)
		}
		defer clear(current.Password)
		password, passwordErr := selectSMTPPassword(input, current)
		if passwordErr != nil {
			return passwordErr
		}
		defer clear(password)
		if validated.Username == "" && len(password) != 0 || validated.Username != "" && len(password) == 0 {
			return fmt.Errorf("%w: password is required when username is set", ErrAccountAdministrationInput)
		}
		if current.Host == validated.Host && current.Port == validated.Port && current.Username == validated.Username &&
			current.FromAddress == validated.FromAddress && current.TLSMode == validated.TLSMode && current.TimeoutSeconds == validated.TimeoutSeconds &&
			bytesEqual(current.Password, password) {
			return ErrAccountAdministrationConflict
		}
		nextRevision := row.AdministrationRevision + 1
		envelope, envelopeErr := encryptSMTPPassword(password, nextRevision, key, randomSource)
		if envelopeErr != nil {
			return fmt.Errorf("encrypt SMTP password: %w", envelopeErr)
		}
		defer clear(envelope)
		stage, remoteErr := gateway.ConfigureEmail(mutationContext, authentikcontrol.EmailSettings{
			Host: validated.Host, Port: validated.Port, Username: validated.Username, Password: string(password),
			FromAddress: validated.FromAddress, Timeout: validated.TimeoutSeconds,
			UseTLS: validated.TLSMode == string(config.SMTPStartTLS), UseSSL: validated.TLSMode == string(config.SMTPImplicitTLS),
		})
		if remoteErr != nil || stage.Host != validated.Host || stage.Port != validated.Port || stage.Username != validated.Username || stage.FromAddress != validated.FromAddress || stage.Timeout != validated.TimeoutSeconds {
			return fmt.Errorf("%w: Authentik rejected SMTP settings", ErrAccountAdministrationUnavailable)
		}
		updated, updateErr := queries.UpdateSMTPSettingsAndAudit(mutationContext, db.UpdateSMTPSettingsAndAuditParams{
			ActorUserID: actor.UserID, ObservedAt: administrationTime(observedAt), Host: validated.Host, Port: int32(validated.Port),
			Username: validated.Username, FromAddress: validated.FromAddress, TlsMode: validated.TLSMode, TimeoutSeconds: int32(validated.TimeoutSeconds),
			PasswordEnvelope: envelope, ExpectedRevision: input.ExpectedRevision, Reason: pgtype.Text{String: input.Reason, Valid: true}, RequestID: requestID,
		})
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return ErrAccountAdministrationConflict
		}
		if updateErr != nil {
			return fmt.Errorf("update SMTP settings: %w", updateErr)
		}
		if updated.AdministrationRevision != nextRevision || updated.AuditID <= 0 {
			return fmt.Errorf("SMTP update returned invalid state")
		}
		result = SMTPSettingsResult{Revision: updated.AdministrationRevision, AuditID: updated.AuditID}
		return nil
	})
	if err != nil {
		return SMTPSettingsResult{}, err
	}
	return result, nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func validateSMTPInput(input SMTPSettingsInput) (SMTPSettings, error) {
	port, portErr := strconv.ParseUint(input.Port, 10, 16)
	timeout, timeoutErr := strconv.ParseUint(input.TimeoutSeconds, 10, 8)
	address, addressErr := mail.ParseAddress(input.FromAddress)
	if !validSMTPHost(input.Host) {
		return SMTPSettings{}, fmt.Errorf("%w: SMTP host is invalid", ErrAccountAdministrationInput)
	}
	if portErr != nil || port == 0 || strconv.FormatUint(port, 10) != input.Port {
		return SMTPSettings{}, fmt.Errorf("%w: SMTP port must be 1 through 65535", ErrAccountAdministrationInput)
	}
	if !validSMTPText(input.Username, 320) {
		return SMTPSettings{}, fmt.Errorf("%w: SMTP username is invalid", ErrAccountAdministrationInput)
	}
	if addressErr != nil || address.Name != "" || address.Address != input.FromAddress || !validSMTPText(input.FromAddress, 320) {
		return SMTPSettings{}, fmt.Errorf("%w: sender must be one email address", ErrAccountAdministrationInput)
	}
	if input.TLSMode != string(config.SMTPStartTLS) && input.TLSMode != string(config.SMTPImplicitTLS) {
		return SMTPSettings{}, fmt.Errorf("%w: TLS mode is invalid", ErrAccountAdministrationInput)
	}
	if timeoutErr != nil || timeout < 1 || timeout > 30 || strconv.FormatUint(timeout, 10) != input.TimeoutSeconds {
		return SMTPSettings{}, fmt.Errorf("%w: timeout must be 1 through 30 seconds", ErrAccountAdministrationInput)
	}
	if input.ExpectedRevision < 1 || input.PasswordAction != "preserve" && input.PasswordAction != "replace" && input.PasswordAction != "clear" {
		return SMTPSettings{}, fmt.Errorf("%w: SMTP revision or password action is invalid", ErrAccountAdministrationInput)
	}
	if input.PasswordAction == "replace" && (len(input.Password) == 0 || len(input.Password) > 4096 || strings.ContainsAny(string(input.Password), "\r\n\x00")) || input.PasswordAction != "replace" && len(input.Password) != 0 {
		return SMTPSettings{}, fmt.Errorf("%w: SMTP password is invalid", ErrAccountAdministrationInput)
	}
	return SMTPSettings{Host: input.Host, Port: int(port), Username: input.Username, FromAddress: input.FromAddress, TLSMode: input.TLSMode, TimeoutSeconds: int(timeout)}, nil
}

func selectSMTPPassword(input SMTPSettingsInput, current SMTPRuntimeSettings) ([]byte, error) {
	switch input.PasswordAction {
	case "preserve":
		return append([]byte(nil), current.Password...), nil
	case "replace":
		return append([]byte(nil), input.Password...), nil
	case "clear":
		return nil, nil
	default:
		return nil, ErrAccountAdministrationInput
	}
}

func runtimeSMTPSettings(host, username, from, tlsMode string, port, timeout int, envelope []byte, revision int64, verified pgtype.Int8, key [32]byte) (SMTPRuntimeSettings, error) {
	settings := SMTPSettings{Host: host, Port: port, Username: username, FromAddress: from, TLSMode: tlsMode, TimeoutSeconds: timeout, PasswordPresent: len(envelope) != 0, Revision: revision}
	settings.Verified = verified.Valid && verified.Int64 == revision && host != ""
	if err := validateStoredSMTP(settings); err != nil {
		return SMTPRuntimeSettings{}, err
	}
	password, err := decryptSMTPPassword(envelope, revision, key)
	if err != nil || (username == "") != (len(password) == 0) {
		clear(password)
		return SMTPRuntimeSettings{}, errors.New("invalid SMTP credential envelope")
	}
	return SMTPRuntimeSettings{SMTPSettings: settings, Password: password}, nil
}

func validateStoredSMTP(settings SMTPSettings) error {
	if settings.Revision < 1 {
		return errors.New("invalid SMTP revision")
	}
	if settings.Host == "" {
		if settings.Port != 0 || settings.Username != "" || settings.FromAddress != "" || settings.TLSMode != "" || settings.TimeoutSeconds != 0 || settings.PasswordPresent || settings.Verified {
			return errors.New("invalid disabled SMTP state")
		}
		return nil
	}
	address, addressErr := mail.ParseAddress(settings.FromAddress)
	if !validSMTPHost(settings.Host) || settings.Port < 1 || settings.Port > 65535 || !validSMTPText(settings.Username, 320) || addressErr != nil || address.Name != "" || address.Address != settings.FromAddress || !validSMTPText(settings.FromAddress, 320) ||
		(settings.TLSMode != string(config.SMTPStartTLS) && settings.TLSMode != string(config.SMTPImplicitTLS)) || settings.TimeoutSeconds < 1 || settings.TimeoutSeconds > 30 || (settings.Username == "") != !settings.PasswordPresent {
		return errors.New("invalid configured SMTP state")
	}
	return nil
}

func validSMTPHost(value string) bool {
	if value == "" || len(value) > 253 || strings.ToLower(value) != value || strings.ContainsAny(value, "[]%") {
		return false
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.String() == value
	}
	return smtpHostName.MatchString(value)
}

func validSMTPText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func encryptSMTPPassword(password []byte, revision int64, key [32]byte, source io.Reader) ([]byte, error) {
	if len(password) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(source, nonce); err != nil {
		clear(nonce)
		return nil, err
	}
	envelope := make([]byte, 1+len(nonce))
	envelope[0] = smtpEnvelopeVersion
	copy(envelope[1:], nonce)
	envelope = gcm.Seal(envelope, nonce, password, smtpEnvelopeAAD(revision))
	clear(nonce)
	return envelope, nil
}

func decryptSMTPPassword(envelope []byte, revision int64, key [32]byte) ([]byte, error) {
	if len(envelope) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(envelope) < 1+gcm.NonceSize()+gcm.Overhead() || envelope[0] != smtpEnvelopeVersion {
		return nil, errors.New("invalid SMTP credential envelope")
	}
	nonce := envelope[1 : 1+gcm.NonceSize()]
	plaintext, err := gcm.Open(nil, nonce, envelope[1+gcm.NonceSize():], smtpEnvelopeAAD(revision))
	if err != nil {
		clear(plaintext)
		return nil, errors.New("invalid SMTP credential envelope")
	}
	return plaintext, nil
}

func smtpEnvelopeAAD(revision int64) []byte {
	aad := make([]byte, len("gotth-bb/smtp/v1/")+8)
	copy(aad, "gotth-bb/smtp/v1/")
	binary.BigEndian.PutUint64(aad[len(aad)-8:], uint64(revision))
	return aad
}
