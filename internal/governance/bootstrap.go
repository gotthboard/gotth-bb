package governance

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type BootstrapResult struct {
	UserID  int64
	AuditID int64
}

// BootstrapAdministrator grants the first local administrator role to one
// already-provisioned external identity. The governance singleton serializes
// the zero-administrator decision; the role and immutable operator audit event
// commit together. No OIDC authorization claim participates.
//
// Complexity: for valid input, time is O(i+s+o+D), Omega(i+s+o+D), and tight
// Theta(i+s+o+D), where i, s, and o are issuer, subject, and operator bytes and
// D is the delegated transaction/database work. Invalid input can exit in
// Omega(1), so one tight bound across every input is not established. Auxiliary
// space is O(1), Omega(1), and tight Theta(1), excluding driver-owned query and
// result buffers. The transaction performs one begin, singleton lock,
// administrator count, unique identity read, coupled role/audit statement, and
// commit. Database wait time is unbounded and caller-context-owned; no retry
// occurs.
func BootstrapAdministrator(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	issuer string,
	subject string,
	operatorIdentifier string,
	requestID pgtype.UUID,
) (BootstrapResult, error) {
	if ctx == nil {
		return BootstrapResult{}, fmt.Errorf("administrator bootstrap context is required")
	}
	if beginner == nil {
		return BootstrapResult{}, fmt.Errorf("administrator bootstrap transaction beginner is required")
	}
	if clock == nil {
		return BootstrapResult{}, fmt.Errorf("administrator bootstrap clock is required")
	}
	validText := func(value string, maximum int) bool {
		if !utf8.ValidString(value) {
			return false
		}
		length := utf8.RuneCountInString(value)
		return length >= 1 && length <= maximum && strings.IndexFunc(value, unicode.IsControl) < 0
	}
	if !validText(issuer, 2048) || !validText(subject, 512) {
		return BootstrapResult{}, fmt.Errorf("administrator bootstrap identity is invalid")
	}
	if !validText(operatorIdentifier, 200) {
		return BootstrapResult{}, fmt.Errorf("administrator bootstrap operator identifier is invalid")
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return BootstrapResult{}, fmt.Errorf("administrator bootstrap request ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap administrator: %w", err)
	}
	now := clock()
	if now.IsZero() {
		return BootstrapResult{}, fmt.Errorf("administrator bootstrap clock returned a zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	atTime := pgtype.Timestamptz{Time: now, Valid: true}
	result := BootstrapResult{}
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		locked, err := queries.LockGovernanceState(ctx)
		if err != nil {
			return fmt.Errorf("lock administrator governance state: %w", err)
		}
		if !locked {
			return fmt.Errorf("administrator governance singleton is missing")
		}
		administrators, err := queries.CountActiveAdministrators(ctx, atTime)
		if err != nil {
			return fmt.Errorf("count active administrators: %w", err)
		}
		if administrators != 0 {
			return fmt.Errorf("administrator bootstrap is already closed")
		}
		user, err := queries.GetUserByExternalIdentity(ctx, db.GetUserByExternalIdentityParams{
			Issuer: issuer, Subject: subject,
		})
		if err != nil {
			return fmt.Errorf("load administrator bootstrap identity: %w", err)
		}
		if user.ID <= 0 {
			return fmt.Errorf("administrator bootstrap identity returned an invalid user")
		}
		bootstrapped, err := queries.BootstrapAdministratorAndAudit(ctx, db.BootstrapAdministratorAndAuditParams{
			UserID:             user.ID,
			AtTime:             atTime,
			OperatorIdentifier: pgtype.Text{String: operatorIdentifier, Valid: true},
			RequestID:          requestID,
		})
		if err != nil {
			return fmt.Errorf("write administrator bootstrap and audit: %w", err)
		}
		if bootstrapped.UserID != user.ID || bootstrapped.AuditID <= 0 {
			return fmt.Errorf("administrator bootstrap returned an invalid result")
		}
		result = BootstrapResult{UserID: bootstrapped.UserID, AuditID: bootstrapped.AuditID}
		return nil
	})
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap first administrator: %w", err)
	}
	return result, nil
}
