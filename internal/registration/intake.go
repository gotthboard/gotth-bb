package registration

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

const intakeTimeout = 2 * time.Second

// Intake is the closed verified projection accepted from Authentik's approval
// flow. It contains no bearer token or authorization claim.
type Intake struct {
	AuthentikUserID int64
	Subject         string
	Username        string
	DisplayName     string
	VerifiedEmail   string
}

type IntakeStore interface {
	UpsertPendingRegistrationIntake(context.Context, db.UpsertPendingRegistrationIntakeParams) (bool, error)
}

// AcceptIntake persists or refreshes one verified pending identity. Terminal
// rows are observed idempotently and never reopened.
func AcceptIntake(ctx context.Context, store IntakeStore, clock func() time.Time, intake Intake) error {
	if ctx == nil || store == nil || clock == nil || intake.AuthentikUserID <= 0 ||
		!validIntakeText(intake.Username, 1, 150, 150) ||
		!validIntakeText(intake.DisplayName, 1, 320, 80) ||
		!validIntakeText(intake.VerifiedEmail, 3, 320, 320) || !intakeAddressOnly(intake.VerifiedEmail) {
		return fmt.Errorf("invalid registration intake")
	}
	subject, err := parseCanonicalUUID(intake.Subject)
	if err != nil {
		return fmt.Errorf("invalid registration intake subject")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("accept registration intake: %w", err)
	}
	at := clock().UTC().Truncate(time.Microsecond)
	if at.IsZero() {
		return fmt.Errorf("registration intake clock returned zero time")
	}
	queryContext, cancel := context.WithTimeout(ctx, intakeTimeout)
	defer cancel()
	accepted, err := store.UpsertPendingRegistrationIntake(queryContext, db.UpsertPendingRegistrationIntakeParams{
		AuthentikSubject: subject, AuthentikUserID: intake.AuthentikUserID,
		DisplayName: intake.DisplayName, VerifiedEmail: intake.VerifiedEmail,
		IntakeAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil || !accepted {
		return fmt.Errorf("persist registration intake failed")
	}
	return nil
}

func intakeAddressOnly(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Name == "" && address.Address == value
}

func parseCanonicalUUID(value string) (pgtype.UUID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || strings.ToLower(value) != value {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID")
	}
	compact := strings.ReplaceAll(value, "-", "")
	raw, err := hex.DecodeString(compact)
	if err != nil || len(raw) != 16 || raw[6]>>4 < 1 || raw[6]>>4 > 5 || raw[8]>>6 != 2 {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID")
	}
	var bytes [16]byte
	copy(bytes[:], raw)
	return pgtype.UUID{Bytes: bytes, Valid: true}, nil
}

func validIntakeText(value string, minimumBytes, maximumBytes, maximumRunes int) bool {
	return utf8.ValidString(value) && len(value) >= minimumBytes && len(value) <= maximumBytes &&
		utf8.RuneCountInString(value) <= maximumRunes && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}
