package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type moderationUserStatusQuerier interface {
	GetModerationUserStatus(context.Context, db.GetModerationUserStatusParams) (db.GetModerationUserStatusRow, error)
}

// ModerationUserStatus is the validated local account state needed by the
// alpha moderation page. Nullable timestamps remain explicit database values;
// no identity-provider attributes or email addresses cross this boundary.
type ModerationUserStatus struct {
	UserID           int64
	DisplayName      string
	Role             policy.Role
	Suspended        bool
	SuspendedAt      pgtype.Timestamptz
	SuspendedUntil   pgtype.Timestamptz
	SuspensionReason pgtype.Text
	MutedUntil       pgtype.Timestamptz
	CreatedAt        pgtype.Timestamptz
	UpdatedAt        pgtype.Timestamptz
	LastLoginAt      pgtype.Timestamptz
}

// GetModerationUserStatus returns one locally authorized account-status view.
// An active moderator may view only another member; an active administrator
// may view any other account. Missing, unauthorized, self, and invalid target
// identifiers share the same no-row result.
//
// Complexity: with g actor-group IDs and delegated indexed query work Q, local
// time is O(g+Q), Omega(1), and auxiliary space is O(A(Q)), Omega(1), without
// one tight bound because validation may return before PostgreSQL. The query
// returns one fixed-width row and is never retried or detached.
func GetModerationUserStatus(
	ctx context.Context,
	querier moderationUserStatusQuerier,
	actor policy.AccessContext,
	targetUserID int64,
	observedAt time.Time,
) (ModerationUserStatus, error) {
	if ctx == nil {
		return ModerationUserStatus{}, fmt.Errorf("moderation user status context is required")
	}
	if querier == nil {
		return ModerationUserStatus{}, fmt.Errorf("moderation user status querier is required")
	}
	if !actor.Valid() {
		return ModerationUserStatus{}, fmt.Errorf("moderation user status access context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return ModerationUserStatus{}, fmt.Errorf("get moderation user status: %w", err)
	}
	if !actor.Authenticated || actor.Suspended || actor.MutedUntil != nil ||
		actor.Role != policy.RoleModerator && actor.Role != policy.RoleAdministrator ||
		targetUserID <= 0 || targetUserID == actor.UserID {
		return ModerationUserStatus{}, fmt.Errorf("get moderation user status: %w", pgx.ErrNoRows)
	}
	if observedAt.IsZero() {
		return ModerationUserStatus{}, fmt.Errorf("moderation user status observation time is required")
	}
	observedAt = observedAt.UTC().Truncate(time.Microsecond)
	row, err := querier.GetModerationUserStatus(ctx, db.GetModerationUserStatusParams{
		TargetUserID:    targetUserID,
		ActorUserID:     actor.UserID,
		IsAdministrator: actor.Role == policy.RoleAdministrator,
		IsModerator:     actor.Role == policy.RoleModerator,
	})
	if err != nil {
		return ModerationUserStatus{}, fmt.Errorf("query moderation user status: %w", err)
	}
	role, roleValid := moderationUserRole(row.Role)
	if row.ID != targetUserID || row.DisplayName == "" || !roleValid ||
		actor.Role == policy.RoleModerator && role != policy.RoleMember ||
		!validModerationUserTime(row.CreatedAt) || !validModerationUserTime(row.UpdatedAt) ||
		!validModerationUserTime(row.LastLoginAt) || row.UpdatedAt.Time.Before(row.CreatedAt.Time) ||
		row.LastLoginAt.Time.Before(row.CreatedAt.Time) ||
		row.MutedUntil.Valid && (!validModerationUserTime(row.MutedUntil) || !row.MutedUntil.Time.After(row.CreatedAt.Time)) ||
		!validModerationSuspension(row) {
		return ModerationUserStatus{}, fmt.Errorf("moderation user status query returned an invalid row")
	}
	suspended := row.SuspendedAt.Valid && !row.SuspendedAt.Time.After(observedAt) &&
		(!row.SuspendedUntil.Valid || row.SuspendedUntil.Time.After(observedAt))
	return ModerationUserStatus{
		UserID: row.ID, DisplayName: row.DisplayName, Role: role, Suspended: suspended,
		SuspendedAt: row.SuspendedAt, SuspendedUntil: row.SuspendedUntil,
		SuspensionReason: row.SuspensionReason, MutedUntil: row.MutedUntil,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, LastLoginAt: row.LastLoginAt,
	}, nil
}

// validModerationSuspension closes the persisted three-field suspension state
// before it reaches presentation.
//
// Complexity: for r <= 2,000 reason bytes, time is O(r), Omega(1), and tight
// Theta(r) for a valid suspension. Auxiliary space is tight Theta(1).
func validModerationSuspension(row db.GetModerationUserStatusRow) bool {
	if !row.SuspendedAt.Valid {
		return !row.SuspendedUntil.Valid && !row.SuspensionReason.Valid
	}
	if !validModerationUserTime(row.SuspendedAt) || row.SuspendedAt.Time.Before(row.CreatedAt.Time) ||
		!row.SuspensionReason.Valid || !validModerationReason(row.SuspensionReason.String) {
		return false
	}
	return !row.SuspendedUntil.Valid || validModerationUserTime(row.SuspendedUntil) && row.SuspendedUntil.Time.After(row.SuspendedAt.Time)
}

// validModerationReason accepts the project-owned canonical suspension reason
// through both schema character and service byte limits.
//
// Complexity: for r <= 2,000 bytes, time is O(r), Omega(1), and tight Theta(r)
// for valid input. Auxiliary space is tight Theta(1).
func validModerationReason(reason string) bool {
	if len(reason) == 0 || len(reason) > 2_000 || !utf8.ValidString(reason) ||
		utf8.RuneCountInString(reason) > 500 || strings.TrimFunc(reason, unicode.IsSpace) != reason {
		return false
	}
	for _, character := range reason {
		if character == '\n' || character == '\r' || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// moderationUserRole maps the closed persisted role into policy authority.
//
// Complexity: time and auxiliary space are tight Theta(1).
func moderationUserRole(role string) (policy.Role, bool) {
	switch role {
	case "member":
		return policy.RoleMember, true
	case "moderator":
		return policy.RoleModerator, true
	case "administrator":
		return policy.RoleAdministrator, true
	default:
		return 0, false
	}
}

// validModerationUserTime rejects null, infinite, and zero persisted times.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validModerationUserTime(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite && !value.Time.IsZero()
}
