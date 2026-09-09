package registration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const decisionTransactionTimeout = 2 * time.Second

var (
	ErrInput       = errors.New("invalid registration decision input")
	ErrDenied      = errors.New("registration decision denied")
	ErrConflict    = errors.New("registration decision conflict")
	ErrRemote      = errors.New("registration decision remote failure")
	ErrUnavailable = errors.New("registration decision unavailable")
)

type Decision uint8

const (
	Approve Decision = iota + 1
	Reject
)

type DecisionInput struct {
	RegistrationID int64
	Revision       int64
	Decision       Decision
	Reason         string
	RequestID      pgtype.UUID
}

type DecisionResult struct {
	Status   string
	Revision int64
	AuditID  int64
}

type Gateway interface {
	AddUser(context.Context, string, string) error
	RemoveUser(context.Context, string, string) error
}

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type decisionNames struct {
	required string
	terminal string
	request  string
	complete string
}

// Decide commits restrictive local intent before changing Authentik, then
// commits a terminal local result only after the gateway has verified every
// requested membership. No PostgreSQL transaction spans remote I/O.
func Decide(ctx context.Context, beginner transactionBeginner, remote Gateway, clock func() time.Time, actor policy.AccessContext, input DecisionInput, referenceKey [32]byte) (DecisionResult, error) {
	names, err := validateDecisionBoundary(ctx, beginner, remote, clock, actor, input, referenceKey)
	if err != nil {
		return DecisionResult{}, err
	}
	reference := registrationReference(referenceKey, input.RegistrationID)
	intentAt, err := observedAt(clock)
	if err != nil {
		return DecisionResult{}, err
	}

	var subject string
	var phaseRevision int64
	var alreadyComplete bool
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, intentAt); err != nil {
			return err
		}
		row, err := queries.LockPendingRegistration(txctx, input.RegistrationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return fmt.Errorf("lock pending registration: %w", err)
		}
		subject, err = canonicalUUID(row.AuthentikSubject)
		if err != nil || row.AuthentikUserID <= 0 || row.AdministrationRevision <= 0 {
			return fmt.Errorf("%w: malformed pending registration", ErrUnavailable)
		}
		switch {
		case row.Status == names.terminal && sameUUID(row.TransitionRequestID, input.RequestID):
			alreadyComplete = true
			phaseRevision = row.AdministrationRevision
			return nil
		case row.Status == names.required && sameUUID(row.TransitionRequestID, input.RequestID):
			phaseRevision = row.AdministrationRevision
			return nil
		case row.Status != "pending" || row.AdministrationRevision != input.Revision:
			return ErrConflict
		}
		changed, err := queries.BeginPendingRegistrationDecision(txctx, db.BeginPendingRegistrationDecisionParams{
			RequiredStatus: names.required, RequestID: input.RequestID, RegistrationID: input.RegistrationID,
			ExpectedRevision: input.Revision, ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true},
			ActionType: names.request, Reason: pgtype.Text{String: input.Reason, Valid: true},
			RegistrationRef: reference, ObservedAt: finiteTime(intentAt),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil || changed.Status != names.required || changed.AdministrationRevision != input.Revision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("%w: persist registration intent", ErrUnavailable)
		}
		phaseRevision = changed.AdministrationRevision
		return nil
	})
	if err != nil {
		return DecisionResult{}, fmt.Errorf("begin registration decision: %w", err)
	}
	if alreadyComplete {
		return DecisionResult{Status: names.terminal, Revision: phaseRevision}, nil
	}

	remoteErr := applyRemoteDecision(ctx, remote, input.Decision, subject)
	resultAt, clockErr := observedAt(clock)
	if clockErr != nil {
		return DecisionResult{}, clockErr
	}
	if remoteErr != nil {
		failure := remoteFailureClass(remoteErr)
		recordErr := recordDecisionFailure(ctx, beginner, actor.UserID, input, names, reference, phaseRevision, failure, resultAt)
		if recordErr != nil {
			return DecisionResult{}, fmt.Errorf("%w: %s; record failure: %v", ErrRemote, failure, recordErr)
		}
		return DecisionResult{}, fmt.Errorf("%w: %s", ErrRemote, failure)
	}

	var result DecisionResult
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, resultAt); err != nil {
			return err
		}
		row, err := queries.LockPendingRegistration(txctx, input.RegistrationID)
		if err != nil {
			return ErrConflict
		}
		if row.Status == names.terminal && sameUUID(row.TransitionRequestID, input.RequestID) {
			result = DecisionResult{Status: row.Status, Revision: row.AdministrationRevision}
			return nil
		}
		if row.Status != names.required || !sameUUID(row.TransitionRequestID, input.RequestID) || row.AdministrationRevision != phaseRevision {
			return ErrConflict
		}
		changed, err := queries.CompletePendingRegistrationDecision(txctx, db.CompletePendingRegistrationDecisionParams{
			ResultingStatus: names.terminal, ObservedAt: finiteTime(resultAt),
			ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, RegistrationID: input.RegistrationID,
			RequiredStatus: names.required, RequestID: input.RequestID, ExpectedRevision: phaseRevision,
			ActionType: names.complete, Reason: pgtype.Text{String: input.Reason, Valid: true}, RegistrationRef: reference,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil || changed.Status != names.terminal || changed.AdministrationRevision != phaseRevision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("%w: complete registration decision", ErrUnavailable)
		}
		result = DecisionResult{Status: changed.Status, Revision: changed.AdministrationRevision, AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return DecisionResult{}, fmt.Errorf("complete registration decision: %w", err)
	}
	return result, nil
}

func validateDecisionBoundary(ctx context.Context, beginner transactionBeginner, remote Gateway, clock func() time.Time, actor policy.AccessContext, input DecisionInput, key [32]byte) (decisionNames, error) {
	if ctx == nil || beginner == nil || remote == nil || clock == nil || !policy.CanAdminister(actor) {
		return decisionNames{}, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return decisionNames{}, fmt.Errorf("registration decision context: %w", err)
	}
	if input.RegistrationID <= 0 || input.Revision <= 0 || !input.RequestID.Valid || input.RequestID.Bytes == ([16]byte{}) ||
		key == ([32]byte{}) || !validReason(input.Reason) {
		return decisionNames{}, ErrInput
	}
	switch input.Decision {
	case Approve:
		return decisionNames{"approval_required", "approved", "request_registration_approval", "approve_registration"}, nil
	case Reject:
		return decisionNames{"rejection_required", "rejected", "request_registration_rejection", "reject_registration"}, nil
	default:
		return decisionNames{}, ErrInput
	}
}

func applyRemoteDecision(ctx context.Context, remote Gateway, decision Decision, subject string) error {
	if decision == Approve {
		if err := remote.AddUser(ctx, "accepted", subject); err != nil {
			return err
		}
	} else if err := remote.RemoveUser(ctx, "accepted", subject); err != nil {
		return err
	}
	return remote.RemoveUser(ctx, "pending", subject)
}

func recordDecisionFailure(ctx context.Context, beginner transactionBeginner, actorID int64, input DecisionInput, names decisionNames, reference string, phaseRevision int64, failure string, at time.Time) error {
	return inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		row, err := queries.RecordPendingRegistrationDecisionFailure(txctx, db.RecordPendingRegistrationDecisionFailureParams{
			FailureClass: pgtype.Text{String: failure, Valid: true}, RegistrationID: input.RegistrationID,
			RequiredStatus: names.required, RequestID: input.RequestID, ExpectedRevision: phaseRevision,
			ActorUserID: pgtype.Int8{Int64: actorID, Valid: true}, Reason: pgtype.Text{String: input.Reason, Valid: true},
			RegistrationRef: reference, ObservedAt: finiteTime(at),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil || row.Status != names.required || row.AdministrationRevision != phaseRevision+1 || row.AuditID <= 0 {
			return fmt.Errorf("%w: persist remote failure", ErrUnavailable)
		}
		return nil
	})
}

func inDecisionTx(ctx context.Context, beginner transactionBeginner, body func(context.Context, *db.Queries) error) error {
	txctx, cancel := context.WithTimeout(ctx, decisionTransactionTimeout)
	defer cancel()
	return store.WithinTxOptions(txctx, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		configured, err := queries.ConfigureAdministrationTransaction(txctx)
		if err != nil || configured.SetConfig != "2s" || configured.SetConfig_2 != "250ms" {
			return fmt.Errorf("%w: configure transaction", ErrUnavailable)
		}
		return body(txctx, queries)
	})
}

func lockAdministrator(ctx context.Context, queries *db.Queries, actorID int64, at time.Time) error {
	locked, err := queries.LockRegistrationAdministrator(ctx, db.LockRegistrationAdministratorParams{ActorUserID: actorID, ObservedAt: finiteTime(at)})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil || locked != actorID {
		return fmt.Errorf("%w: revalidate administrator", ErrUnavailable)
	}
	return nil
}

func observedAt(clock func() time.Time) (time.Time, error) {
	at := clock()
	if at.IsZero() {
		return time.Time{}, fmt.Errorf("%w: zero clock", ErrUnavailable)
	}
	return at.UTC().Truncate(time.Microsecond), nil
}

func finiteTime(at time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: at, Valid: true}
}

func canonicalUUID(value pgtype.UUID) (string, error) {
	if !value.Valid || value.Bytes == ([16]byte{}) {
		return "", ErrUnavailable
	}
	b := value.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", binary.BigEndian.Uint32(b[0:4]), binary.BigEndian.Uint16(b[4:6]), binary.BigEndian.Uint16(b[6:8]), binary.BigEndian.Uint16(b[8:10]), b[10:16]), nil
}

func sameUUID(left, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.Bytes == right.Bytes
}

func registrationReference(key [32]byte, id int64) string {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("gotth-bb/pending-registration-audit/v1\x00"))
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(id))
	_, _ = mac.Write(raw[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func remoteFailureClass(err error) string {
	switch {
	case errors.Is(err, authentikgateway.ErrRemoteUnavailable):
		return "remote_unavailable"
	case errors.Is(err, authentikgateway.ErrRemoteConflict):
		return "remote_conflict"
	case errors.Is(err, authentikgateway.ErrAbsentObject):
		return "remote_absent"
	case errors.Is(err, authentikgateway.ErrRemoteInvalid), errors.Is(err, authentikgateway.ErrInvalidRequest):
		return "remote_invalid"
	default:
		return "remote_failure"
	}
}

func validReason(reason string) bool {
	return len(reason) >= 1 && len(reason) <= 2_000 && utf8.ValidString(reason) &&
		utf8.RuneCountInString(reason) <= 2_000 && strings.TrimSpace(reason) == reason && !containsControl(reason)
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
