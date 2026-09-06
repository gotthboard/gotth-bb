package moderation

import (
	"context"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type ExtendedAction string

const (
	PinTopic    ExtendedAction = "pin_topic"
	UnpinTopic  ExtendedAction = "unpin_topic"
	MoveTopic   ExtendedAction = "move_topic"
	HidePost    ExtendedAction = "hide_post"
	RestorePost ExtendedAction = "restore_post"
	RedactPost  ExtendedAction = "redact_post"
	WarnUser    ExtendedAction = "warn_user"
	MuteUser    ExtendedAction = "mute_user"
)

type ExtendedActionInput struct {
	Action              ExtendedAction
	TargetID            int64
	Reason              string
	DestinationAreaSlug string
	MuteDuration        time.Duration
}

type ExtendedActionResult struct {
	Action     ExtendedAction
	TargetID   int64
	TopicID    int64
	TargetPage int64
	AuditID    int64
	WarningID  int64
	MutedUntil *time.Time
}

// ApplyExtendedAction completes the remaining AN-01 content and account
// actions. Each path revalidates persisted staff authority and commits exactly
// one typed mutation plus audit without retry.
func ApplyExtendedAction(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	input ExtendedActionInput,
	requestID pgtype.UUID,
) (ExtendedActionResult, error) {
	if ctx == nil || beginner == nil || clock == nil {
		return ExtendedActionResult{}, fmt.Errorf("extended moderation dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return ExtendedActionResult{}, fmt.Errorf("apply extended moderation: %w", err)
	}
	if !activeStaffActor(actor) {
		return ExtendedActionResult{}, ErrUserModerationDenied
	}
	if input.TargetID <= 0 || !validExtendedReason(input) || !validExtendedInput(input) {
		return ExtendedActionResult{}, fmt.Errorf("%w: extended action", ErrUserModerationInput)
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return ExtendedActionResult{}, fmt.Errorf("extended moderation request ID is invalid")
	}
	now := clock()
	if now.IsZero() {
		return ExtendedActionResult{}, fmt.Errorf("extended moderation clock returned zero time")
	}
	now = now.UTC().Truncate(time.Microsecond)
	var result ExtendedActionResult
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		if input.Action == WarnUser || input.Action == MuteUser {
			return applyUserAction(ctx, queries, now, actor, input, requestID, &result)
		}
		persisted, err := queries.LockUserForSuspension(ctx, actor.UserID)
		if err != nil {
			return fmt.Errorf("lock extended moderation actor: %w", err)
		}
		if !persistedStaffMatches(persisted, actor, now) {
			return ErrUserModerationDenied
		}
		switch input.Action {
		case PinTopic, UnpinTopic, MoveTopic:
			return applyTopicAction(ctx, queries, now, actor.UserID, input, requestID, &result)
		case HidePost, RestorePost, RedactPost:
			return applyPostAction(ctx, queries, now, actor.UserID, input, requestID, &result)
		default:
			return fmt.Errorf("%w: unknown action", ErrUserModerationInput)
		}
	})
	if err != nil {
		return ExtendedActionResult{}, fmt.Errorf("extended moderation transaction: %w", err)
	}
	return result, nil
}

func applyTopicAction(
	ctx context.Context,
	queries *db.Queries,
	now time.Time,
	actorID int64,
	input ExtendedActionInput,
	requestID pgtype.UUID,
	result *ExtendedActionResult,
) error {
	topic, err := queries.LockTopicForExtendedModeration(ctx, input.TargetID)
	if err != nil {
		return fmt.Errorf("lock topic for extended moderation: %w", err)
	}
	invalidTopic := topic.ID != input.TargetID || topic.AreaID <= 0 || !validTopicState(topic.State) ||
		!finiteTimestamp(topic.CreatedAt) || !finiteTimestamp(topic.UpdatedAt) ||
		topic.UpdatedAt.Time.Before(topic.CreatedAt.Time)
	invalidPin := topic.PinnedAt.Valid &&
		(!finiteTimestamp(topic.PinnedAt) || topic.PinnedAt.Time.Before(topic.CreatedAt.Time))
	if invalidTopic || invalidPin {
		return fmt.Errorf("extended topic lock returned invalid state")
	}
	at := now
	if topic.UpdatedAt.Time.After(at) {
		at = topic.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	atTime := pgtype.Timestamptz{Time: at, Valid: true}
	actor := pgtype.Int8{Int64: actorID, Valid: true}
	if input.Action == PinTopic || input.Action == UnpinTopic {
		alreadyPinned := input.Action == PinTopic && topic.PinnedAt.Valid
		alreadyUnpinned := input.Action == UnpinTopic && !topic.PinnedAt.Valid
		if topic.State == "archived" || topic.State == "hidden" || alreadyPinned || alreadyUnpinned {
			return ErrTopicModerationConflict
		}
		resulting := pgtype.Timestamptz{}
		if input.Action == PinTopic {
			resulting = atTime
		}
		changed, changeErr := queries.ChangeTopicPinAndAudit(ctx, db.ChangeTopicPinAndAuditParams{
			ResultingPinnedAt: resulting,
			AtTime:            atTime,
			TopicID:           topic.ID,
			PreviousPinnedAt:  topic.PinnedAt,
			ActorUserID:       actor,
			ActionType:        string(input.Action),
			Reason:            pgtype.Text{String: input.Reason, Valid: true},
			RequestID:         requestID,
		})
		if changeErr != nil {
			return fmt.Errorf("change topic pin and audit: %w", changeErr)
		}
		invalidPinResult := input.Action == PinTopic && !changed.PinnedAt.Valid ||
			input.Action == UnpinTopic && changed.PinnedAt.Valid
		if changed.TopicID != topic.ID || changed.AuditID <= 0 || invalidPinResult {
			return fmt.Errorf("topic pin returned invalid result")
		}
		*result = ExtendedActionResult{Action: input.Action, TargetID: topic.ID, TopicID: topic.ID, AuditID: changed.AuditID}
		return nil
	}
	if topic.State == "archived" {
		return ErrTopicModerationConflict
	}
	destination, err := queries.GetDestinationAreaBySlug(ctx, input.DestinationAreaSlug)
	if err != nil {
		return fmt.Errorf("load move destination: %w", err)
	}
	if !validMoveArea(destination.ID, destination.Slug, destination.Name, destination.PostingMode) ||
		destination.ID == topic.AreaID || destination.Slug != input.DestinationAreaSlug ||
		destination.PostingMode == "archived" {
		return ErrTopicModerationConflict
	}
	first, second := topic.AreaID, destination.ID
	if second < first {
		first, second = second, first
	}
	firstArea, err := queries.LockAreaForTopicMove(ctx, first)
	if err != nil {
		return fmt.Errorf("lock first move area: %w", err)
	}
	secondArea, err := queries.LockAreaForTopicMove(ctx, second)
	if err != nil {
		return fmt.Errorf("lock second move area: %w", err)
	}
	invalidFirst := firstArea.ID != first ||
		!validMoveArea(firstArea.ID, firstArea.Slug, firstArea.Name, firstArea.PostingMode)
	invalidSecond := secondArea.ID != second ||
		!validMoveArea(secondArea.ID, secondArea.Slug, secondArea.Name, secondArea.PostingMode)
	changedDestination := firstArea.ID == destination.ID &&
		(firstArea.Slug != destination.Slug || firstArea.PostingMode == "archived") ||
		secondArea.ID == destination.ID &&
			(secondArea.Slug != destination.Slug || secondArea.PostingMode == "archived")
	if invalidFirst || invalidSecond || changedDestination {
		return ErrTopicModerationConflict
	}
	changed, err := queries.MoveTopicAndAudit(ctx, db.MoveTopicAndAuditParams{
		ResultingAreaID: destination.ID,
		AtTime:          atTime,
		TopicID:         topic.ID,
		PreviousAreaID:  topic.AreaID,
		ActorUserID:     actor,
		Reason:          pgtype.Text{String: input.Reason, Valid: true},
		RequestID:       requestID,
	})
	if err != nil {
		return fmt.Errorf("move topic and audit: %w", err)
	}
	if changed.TopicID != topic.ID || changed.AreaID != destination.ID || changed.AuditID <= 0 {
		return fmt.Errorf("move topic returned invalid result")
	}
	*result = ExtendedActionResult{Action: input.Action, TargetID: topic.ID, TopicID: topic.ID, AuditID: changed.AuditID}
	return nil
}

func applyPostAction(
	ctx context.Context,
	queries *db.Queries,
	now time.Time,
	actorID int64,
	input ExtendedActionInput,
	requestID pgtype.UUID,
	result *ExtendedActionResult,
) error {
	post, err := queries.LockPostForModeration(ctx, input.TargetID)
	if err != nil {
		return fmt.Errorf("lock post for moderation: %w", err)
	}
	if post.ID != input.TargetID || post.TopicID <= 0 || post.AuthorID <= 0 || post.Revision <= 0 ||
		post.NodeOrdinal <= 0 || post.NodeOrdinal > int64(store.MaximumPostPage)*int64(store.PostPageSize) ||
		!finiteTimestamp(post.CreatedAt) || !finiteTimestamp(post.UpdatedAt) ||
		post.UpdatedAt.Time.Before(post.CreatedAt.Time) {
		return fmt.Errorf("post moderation lock returned invalid state")
	}
	alreadyHidden := input.Action == HidePost && post.DeletedAt.Valid
	alreadyVisible := input.Action == RestorePost && !post.DeletedAt.Valid
	hiddenRedaction := input.Action == RedactPost && post.DeletedAt.Valid
	if post.RedactedAt.Valid || alreadyHidden || alreadyVisible || hiddenRedaction {
		return ErrTopicModerationConflict
	}
	at := now
	if post.UpdatedAt.Time.After(at) {
		at = post.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	atTime := pgtype.Timestamptz{Time: at, Valid: true}
	actor := pgtype.Int8{Int64: actorID, Valid: true}
	targetPage := 1 + (post.NodeOrdinal-1)/int64(store.PostPageSize)
	if input.Action == RedactPost {
		changed, changeErr := queries.RedactPostAndAudit(ctx, db.RedactPostAndAuditParams{
			AtTime: atTime, ActorUserID: actor,
			Reason: pgtype.Text{String: input.Reason, Valid: true},
			PostID: post.ID, PreviousRevision: post.Revision, RequestID: requestID,
		})
		if changeErr != nil {
			return fmt.Errorf("redact post and audit: %w", changeErr)
		}
		if changed.PostID != post.ID || changed.TopicID != post.TopicID ||
			changed.Revision != post.Revision+1 || !changed.RedactedAt.Valid ||
			!changed.RedactedBy.Valid || changed.RedactedBy.Int64 != actorID || changed.AuditID <= 0 {
			return fmt.Errorf("redact post returned invalid result")
		}
		*result = ExtendedActionResult{Action: input.Action, TargetID: post.ID, TopicID: post.TopicID, TargetPage: targetPage, AuditID: changed.AuditID}
		return nil
	}
	deletedAt, deletedBy, deletionReason := pgtype.Timestamptz{}, pgtype.Int8{}, pgtype.Text{}
	if input.Action == HidePost {
		deletedAt, deletedBy, deletionReason = atTime, actor, pgtype.Text{String: input.Reason, Valid: true}
	}
	changed, changeErr := queries.ChangePostVisibilityAndAudit(ctx, db.ChangePostVisibilityAndAuditParams{
		ResultingDeletedAt: deletedAt,
		ResultingDeletedBy: deletedBy,
		ResultingReason:    deletionReason,
		AtTime:             atTime,
		PostID:             post.ID,
		PreviousDeletedAt:  post.DeletedAt,
		ActorUserID:        actor,
		ActionType:         string(input.Action),
		AuditReason:        pgtype.Text{String: input.Reason, Valid: true},
		PreviousDeletedBy:  post.DeletedBy,
		RequestID:          requestID,
	})
	if changeErr != nil {
		return fmt.Errorf("change post visibility and audit: %w", changeErr)
	}
	invalidVisibility := input.Action == HidePost && !changed.DeletedAt.Valid ||
		input.Action == RestorePost && changed.DeletedAt.Valid
	if changed.PostID != post.ID || changed.TopicID != post.TopicID ||
		changed.AuditID <= 0 || invalidVisibility {
		return fmt.Errorf("post visibility returned invalid result")
	}
	*result = ExtendedActionResult{Action: input.Action, TargetID: post.ID, TopicID: post.TopicID, TargetPage: targetPage, AuditID: changed.AuditID}
	return nil
}

func applyUserAction(
	ctx context.Context,
	queries *db.Queries,
	now time.Time,
	actor policy.AccessContext,
	input ExtendedActionInput,
	requestID pgtype.UUID,
	result *ExtendedActionResult,
) error {
	if input.TargetID == actor.UserID {
		return ErrUserModerationDenied
	}
	first, second := actor.UserID, input.TargetID
	if second < first {
		first, second = second, first
	}
	firstUser, err := queries.LockUserForSuspension(ctx, first)
	if err != nil {
		return fmt.Errorf("lock first discipline user: %w", err)
	}
	secondUser, err := queries.LockUserForSuspension(ctx, second)
	if err != nil {
		return fmt.Errorf("lock second discipline user: %w", err)
	}
	actorUser, target := firstUser, secondUser
	if firstUser.ID == input.TargetID {
		actorUser, target = secondUser, firstUser
	}
	if !persistedStaffMatches(actorUser, actor, now) || !validSuspensionTarget(target, input.TargetID) {
		return ErrUserModerationDenied
	}
	if actor.Role == policy.RoleModerator && target.Role != "member" {
		return ErrUserModerationDenied
	}
	at := now
	if target.UpdatedAt.Time.After(at) {
		at = target.UpdatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	atTime := pgtype.Timestamptz{Time: at, Valid: true}
	if input.Action == WarnUser {
		warned, warnErr := queries.WarnUserAndAudit(ctx, db.WarnUserAndAuditParams{
			UserID: target.ID, ActorUserID: actor.UserID, Reason: input.Reason,
			AtTime: atTime, RequestID: requestID,
		})
		if warnErr != nil {
			return fmt.Errorf("warn user and audit: %w", warnErr)
		}
		if warned.WarningID <= 0 || warned.UserID != target.ID || warned.AuditID <= 0 {
			return fmt.Errorf("warn user returned invalid result")
		}
		*result = ExtendedActionResult{Action: input.Action, TargetID: target.ID, WarningID: warned.WarningID, AuditID: warned.AuditID}
		return nil
	}
	if target.MutedUntil.Valid && target.MutedUntil.Time.After(now) {
		return ErrUserModerationConflict
	}
	muteBase := now
	if target.CreatedAt.Time.After(muteBase) {
		muteBase = target.CreatedAt.Time.UTC().Truncate(time.Microsecond)
	}
	mutedUntil := muteBase.Add(input.MuteDuration).UTC().Truncate(time.Microsecond)
	changed, muteErr := queries.MuteUserAndAudit(ctx, db.MuteUserAndAuditParams{
		MutedUntil:         pgtype.Timestamptz{Time: mutedUntil, Valid: true},
		AtTime:             atTime,
		UserID:             target.ID,
		ObservedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		ActorUserID:        pgtype.Int8{Int64: actor.UserID, Valid: true},
		Reason:             pgtype.Text{String: input.Reason, Valid: true},
		PreviousMutedUntil: target.MutedUntil,
		RequestID:          requestID,
	})
	if muteErr != nil {
		return fmt.Errorf("mute user and audit: %w", muteErr)
	}
	if changed.UserID != target.ID || !changed.MutedUntil.Valid || !changed.MutedUntil.Time.Equal(mutedUntil) || changed.AuditID <= 0 {
		return fmt.Errorf("mute user returned invalid result")
	}
	*result = ExtendedActionResult{Action: input.Action, TargetID: target.ID, AuditID: changed.AuditID, MutedUntil: &mutedUntil}
	return nil
}

func activeStaffActor(actor policy.AccessContext) bool {
	return actor.Valid() && actor.Authenticated && !actor.Suspended && actor.MutedUntil == nil && (actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator)
}

func persistedStaffMatches(row db.LockUserForSuspensionRow, actor policy.AccessContext, now time.Time) bool {
	role, valid := roleFromStorage(row.Role)
	active := !userSuspendedAt(row, now) &&
		(!row.MutedUntil.Valid || !row.MutedUntil.Time.After(now))
	staff := role == policy.RoleModerator || role == policy.RoleAdministrator
	return validSuspensionTarget(row, actor.UserID) && valid && role == actor.Role && active && staff
}

func validExtendedInput(input ExtendedActionInput) bool {
	switch input.Action {
	case PinTopic, UnpinTopic, HidePost, RestorePost, RedactPost, WarnUser:
		return input.DestinationAreaSlug == "" && input.MuteDuration == 0
	case MoveTopic:
		return policy.ValidAreaSlug(input.DestinationAreaSlug) && input.MuteDuration == 0
	case MuteUser:
		validDuration := input.MuteDuration == time.Hour || input.MuteDuration == 24*time.Hour ||
			input.MuteDuration == 7*24*time.Hour || input.MuteDuration == 30*24*time.Hour
		return input.DestinationAreaSlug == "" && validDuration
	default:
		return false
	}
}

func validExtendedReason(input ExtendedActionInput) bool {
	if input.Action == HidePost || input.Action == RestorePost || input.Action == RedactPost {
		return validSuspensionReason(input.Reason)
	}
	return validReason(input.Reason)
}

func validMoveArea(id int64, slug, name, postingMode string) bool {
	return id > 0 && policy.ValidAreaSlug(slug) && name != "" &&
		(postingMode == "normal" || postingMode == "read_only" || postingMode == "archived")
}
