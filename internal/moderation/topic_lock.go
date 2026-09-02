package moderation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrTopicModerationInput    = errors.New("invalid topic moderation input")
	ErrTopicModerationDenied   = errors.New("topic moderation denied")
	ErrTopicModerationConflict = errors.New("topic moderation state conflict")
)

type TopicLockResult struct {
	TopicID int64
	State   policy.TopicState
	AuditID int64
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// ChangeTopicLock locks or unlocks one existing topic and appends exactly one
// immutable audit row in the same transaction. Only active moderator or
// administrator authority is admitted; wrong current states conflict without
// mutation or audit.
//
// Complexity: for g actor-group IDs, r reason bytes, and delegated database
// work D, local time is O(g+r), Omega(1), and auxiliary space is tight Theta(1).
// Total time is O(g+r+D), Omega(1), without one tight bound because validation
// may return early and database work varies. One transaction performs two
// application statements plus begin/commit, no retry, and no detached work.
func ChangeTopicLock(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	topicID int64,
	lock bool,
	reason string,
	requestID pgtype.UUID,
) (TopicLockResult, error) {
	if ctx == nil {
		return TopicLockResult{}, fmt.Errorf("topic moderation context is required")
	}
	if beginner == nil {
		return TopicLockResult{}, fmt.Errorf("topic moderation transaction beginner is required")
	}
	if clock == nil {
		return TopicLockResult{}, fmt.Errorf("topic moderation clock is required")
	}
	if !actor.Valid() || !actor.Authenticated {
		return TopicLockResult{}, fmt.Errorf("topic moderation actor is invalid")
	}
	if actor.Suspended || actor.MutedUntil != nil || actor.Role != policy.RoleModerator && actor.Role != policy.RoleAdministrator {
		return TopicLockResult{}, ErrTopicModerationDenied
	}
	if topicID <= 0 {
		return TopicLockResult{}, fmt.Errorf("%w: target", ErrTopicModerationInput)
	}
	if !validReason(reason) {
		return TopicLockResult{}, fmt.Errorf("%w: reason", ErrTopicModerationInput)
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return TopicLockResult{}, fmt.Errorf("topic moderation request ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return TopicLockResult{}, fmt.Errorf("moderate topic: %w", err)
	}
	now := clock()
	if now.IsZero() {
		return TopicLockResult{}, fmt.Errorf("topic moderation clock returned a zero time")
	}
	atTime := pgtype.Timestamptz{Time: now.UTC().Truncate(time.Microsecond), Valid: true}
	previous, resulting, action := string(policy.TopicOpen), string(policy.TopicLocked), "lock_topic"
	if !lock {
		previous, resulting, action = string(policy.TopicLocked), string(policy.TopicOpen), "unlock_topic"
	}

	result := TopicLockResult{}
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		current, err := queries.LockTopicForModeration(ctx, topicID)
		if err != nil {
			return fmt.Errorf("lock topic for moderation: %w", err)
		}
		if current.ID != topicID || !validTopicState(current.State) {
			return fmt.Errorf("topic moderation lock returned an invalid result")
		}
		if current.State != previous {
			return ErrTopicModerationConflict
		}
		changed, err := queries.ChangeTopicStateAndAudit(ctx, db.ChangeTopicStateAndAuditParams{
			ResultingState: resulting, AtTime: atTime, TopicID: topicID, PreviousState: previous,
			ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, ActionType: action,
			Reason: pgtype.Text{String: reason, Valid: true}, RequestID: requestID,
		})
		if err != nil {
			return fmt.Errorf("change topic state and audit: %w", err)
		}
		if changed.TopicID != topicID || changed.State != resulting || !changed.UpdatedAt.Valid || changed.UpdatedAt.InfinityModifier != pgtype.Finite || changed.AuditID <= 0 {
			return fmt.Errorf("topic moderation returned an invalid result")
		}
		result = TopicLockResult{TopicID: changed.TopicID, State: policy.TopicState(changed.State), AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return TopicLockResult{}, fmt.Errorf("topic moderation transaction: %w", err)
	}
	return result, nil
}

// validReason accepts canonical nonblank single-line UTF-8 prose through 2,000
// bytes, without leading or trailing Unicode whitespace.
//
// Complexity: for r bytes, time is O(r), Omega(1), and tight Theta(r) for a
// valid reason. Auxiliary space is tight Theta(1); standard-library scans do
// not construct a normalized copy.
func validReason(reason string) bool {
	if len(reason) == 0 || len(reason) > 2_000 || !utf8.ValidString(reason) {
		return false
	}
	trimmed := strings.TrimSpace(reason)
	return trimmed != "" && trimmed == reason &&
		strings.IndexFunc(reason, func(value rune) bool {
			return unicode.IsControl(value) || value == '\u2028' || value == '\u2029'
		}) < 0
}

// validTopicState closes the generated database value before policy use.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validTopicState(state string) bool {
	return state == string(policy.TopicOpen) || state == string(policy.TopicLocked) ||
		state == string(policy.TopicHidden) || state == string(policy.TopicArchived)
}
