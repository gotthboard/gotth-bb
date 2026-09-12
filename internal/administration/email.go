package administration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrEmailTestRateLimited = errors.New("email test rate limited")

type EmailTestState struct {
	Status        string
	RequestedAt   time.Time
	CompletedAt   time.Time
	NextAllowedAt time.Time
}

type EmailTestResult struct {
	Status      string
	RequestedAt time.Time
	CompletedAt time.Time
	AuditID     int64
}

type EmailTestMailer interface {
	SendTest(context.Context, string) (string, error)
}

type emailAdministrationQuerier interface {
	LoadEmailTestStateForAdministration(context.Context, db.LoadEmailTestStateForAdministrationParams) (db.LoadEmailTestStateForAdministrationRow, error)
}

func LoadEmailTestState(ctx context.Context, querier emailAdministrationQuerier, actor policy.AccessContext, observedAt time.Time) (EmailTestState, error) {
	if ctx == nil || querier == nil {
		return EmailTestState{}, fmt.Errorf("email administration loader is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return EmailTestState{}, ErrAccountAdministrationDenied
	}
	if observedAt.IsZero() {
		return EmailTestState{}, ErrAccountAdministrationInput
	}
	if err := ctx.Err(); err != nil {
		return EmailTestState{}, fmt.Errorf("load email test state: %w", err)
	}
	observed := observedAt.UTC().Truncate(time.Microsecond)
	row, err := querier.LoadEmailTestStateForAdministration(ctx, db.LoadEmailTestStateForAdministrationParams{
		ActorUserID: actor.UserID, ObservedAt: administrationTime(observed),
	})
	if err != nil {
		return EmailTestState{}, fmt.Errorf("%w: load email test state", ErrAccountAdministrationUnavailable)
	}
	if !row.ActorPresent {
		return EmailTestState{}, ErrAccountAdministrationDenied
	}
	if !row.StatePresent {
		if row.Status != "" || row.RequestedAt.Valid || row.CompletedAt.Valid || row.NextAllowedAt.Valid {
			return EmailTestState{}, malformedAdministrationRows("email test state")
		}
		return EmailTestState{}, nil
	}
	state := EmailTestState{Status: row.Status}
	if finiteAdministrationTime(row.RequestedAt) {
		state.RequestedAt = row.RequestedAt.Time.UTC().Truncate(time.Microsecond)
	}
	if finiteAdministrationTime(row.CompletedAt) {
		state.CompletedAt = row.CompletedAt.Time.UTC().Truncate(time.Microsecond)
	}
	if finiteAdministrationTime(row.NextAllowedAt) {
		state.NextAllowedAt = row.NextAllowedAt.Time.UTC().Truncate(time.Microsecond)
	}
	if !validEmailTestStatus(state.Status) || state.RequestedAt.IsZero() || state.NextAllowedAt != state.RequestedAt.Add(5*time.Minute) ||
		(state.Status == "requested") != state.CompletedAt.IsZero() ||
		state.RequestedAt.After(observed) || !state.CompletedAt.IsZero() && (state.CompletedAt.Before(state.RequestedAt) || state.CompletedAt.After(observed)) {
		return EmailTestState{}, malformedAdministrationRows("email test state")
	}
	return state, nil
}

func TestEmail(ctx context.Context, beginner accountTransactionBeginner, mailer EmailTestMailer, clock func() time.Time, random io.Reader, actor policy.AccessContext, reason string, requestID pgtype.UUID) (EmailTestResult, error) {
	if mailer == nil || random == nil {
		return EmailTestResult{}, fmt.Errorf("email test boundary is incomplete")
	}
	if err := validateAccountMutationBoundary(ctx, beginner, clock, actor, reason, requestID); err != nil {
		return EmailTestResult{}, err
	}
	requestedAt, err := accountMutationTime(clock)
	if err != nil {
		return EmailTestResult{}, err
	}
	idempotencyKey, err := randomUUID(random)
	if err != nil {
		return EmailTestResult{}, fmt.Errorf("%w: generate email test identity", ErrAccountAdministrationUnavailable)
	}
	idempotencyText := fmt.Sprintf("%x", idempotencyKey.Bytes)
	digest := sha256.Sum256([]byte(idempotencyText))
	idempotencyDigest := hex.EncodeToString(digest[:])

	var recipient string
	var requestedAudit int64
	reservationContext, cancelReservation := context.WithTimeout(ctx, accountMutationTimeout)
	defer cancelReservation()
	err = store.WithinTxOptions(reservationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(reservationContext, queries); err != nil {
			return err
		}
		locked, lockErr := queries.LockEmailTestAdministrator(reservationContext, db.LockEmailTestAdministratorParams{
			ActorUserID: actor.UserID, ObservedAt: administrationTime(requestedAt),
		})
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ErrAccountAdministrationDenied
		}
		if lockErr != nil {
			return fmt.Errorf("lock email test administrator: %w", lockErr)
		}
		if locked.ID != actor.UserID || !locked.Email.Valid || !validAddressOnly(locked.Email.String) {
			return ErrAccountAdministrationDenied
		}
		recipient = locked.Email.String
		reserved, reserveErr := queries.ReserveEmailTestAndAudit(reservationContext, db.ReserveEmailTestAndAuditParams{
			ActorUserID: actor.UserID, IdempotencyKey: idempotencyKey,
			ObservedAt: administrationTime(requestedAt), Reason: pgtype.Text{String: reason, Valid: true},
			IdempotencySha256: idempotencyDigest, RequestID: requestID,
		})
		if errors.Is(reserveErr, pgx.ErrNoRows) {
			return ErrEmailTestRateLimited
		}
		if reserveErr != nil {
			return fmt.Errorf("reserve email test: %w", reserveErr)
		}
		if !finiteAdministrationTime(reserved.RequestedAt) || !finiteAdministrationTime(reserved.NextAllowedAt) ||
			!reserved.RequestedAt.Time.Equal(requestedAt) || !reserved.NextAllowedAt.Time.Equal(requestedAt.Add(5*time.Minute)) || reserved.AuditID <= 0 {
			return fmt.Errorf("email test reservation returned invalid state")
		}
		requestedAudit = reserved.AuditID
		return nil
	})
	if err != nil {
		return EmailTestResult{}, fmt.Errorf("email test reservation transaction: %w", err)
	}

	status, sendErr := mailer.SendTest(ctx, recipient)
	if !validCompletedEmailTestStatus(status) || status == "accepted" && sendErr != nil || status != "accepted" && sendErr == nil {
		status = "unknown"
	}
	completedAt, clockErr := accountMutationTime(clock)
	if clockErr != nil || completedAt.Before(requestedAt) {
		return EmailTestResult{}, fmt.Errorf("%w: complete email test clock", ErrAccountAdministrationUnavailable)
	}
	// SMTP submission may finish after the browser has disconnected. Once the
	// reservation exists, persist its terminal classification with a fresh,
	// bounded context so a canceled request does not strand "requested" state.
	completionContext, cancelCompletion := context.WithTimeout(context.WithoutCancel(ctx), accountMutationTimeout)
	defer cancelCompletion()
	var result EmailTestResult
	err = store.WithinTxOptions(completionContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := configureAccountTransaction(completionContext, queries); err != nil {
			return err
		}
		completed, completeErr := queries.CompleteEmailTestAndAudit(completionContext, db.CompleteEmailTestAndAuditParams{
			Status: status, ObservedAt: administrationTime(completedAt), ActorUserID: actor.UserID,
			IdempotencyKey: idempotencyKey, Reason: pgtype.Text{String: reason, Valid: true},
			IdempotencySha256: idempotencyDigest, RequestID: requestID,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return ErrAccountAdministrationConflict
		}
		if completeErr != nil {
			return fmt.Errorf("complete email test: %w", completeErr)
		}
		if completed.Status != status || !finiteAdministrationTime(completed.CompletedAt) ||
			!completed.CompletedAt.Time.Equal(completedAt) || completed.AuditID <= requestedAudit {
			return fmt.Errorf("email test completion returned invalid state")
		}
		result = EmailTestResult{Status: status, RequestedAt: requestedAt, CompletedAt: completedAt, AuditID: completed.AuditID}
		return nil
	})
	if err != nil {
		return EmailTestResult{}, fmt.Errorf("email test completion transaction: %w", err)
	}
	return result, nil
}

func validEmailTestStatus(status string) bool {
	return status == "requested" || validCompletedEmailTestStatus(status)
}

func validCompletedEmailTestStatus(status string) bool {
	return status == "accepted" || status == "failed" || status == "unknown"
}

func validAddressOnly(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Name == "" && address.Address == value
}

func randomUUID(source io.Reader) (pgtype.UUID, error) {
	if source == nil {
		return pgtype.UUID{}, errors.New("UUID source is required")
	}
	var raw [16]byte
	if _, err := io.ReadFull(source, raw[:]); err != nil {
		return pgtype.UUID{}, err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	if raw == ([16]byte{}) {
		return pgtype.UUID{}, errors.New("zero UUID")
	}
	return pgtype.UUID{Bytes: raw, Valid: true}, nil
}
