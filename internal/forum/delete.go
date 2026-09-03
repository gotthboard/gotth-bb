package forum

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
)

var (
	ErrPostDeleteDenied   = errors.New("post delete denied")
	ErrPostDeleteConflict = errors.New("post delete revision conflict")
)

type DeleteResult struct {
	TopicID    int64
	PostID     int64
	PostNumber int32
	Revision   int32
}

// DeletePost soft-deletes one active author's own visible post after locking
// current post, topic, area, and group policy. Authorization precedes revision
// conflict disclosure, and no topic identity, numbering, or counters change.
//
// Complexity: for actor groups a, area groups p, and database work D, time is
// O(a*p+a+p+D), Omega(1), without one tight bound because invalid input and
// external database work vary. Auxiliary space is O(p), Omega(1). There is one
// transaction with three application statements plus begin/commit, no retry,
// and no detached work.
func DeletePost(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	postID int64,
	expectedRevision int32,
) (DeleteResult, error) {
	if ctx == nil {
		return DeleteResult{}, fmt.Errorf("delete post context is required")
	}
	if beginner == nil {
		return DeleteResult{}, fmt.Errorf("delete post transaction beginner is required")
	}
	if clock == nil {
		return DeleteResult{}, fmt.Errorf("delete post clock is required")
	}
	if !actor.Valid() || !actor.Authenticated {
		return DeleteResult{}, fmt.Errorf("delete post actor is invalid")
	}
	if postID <= 0 {
		return DeleteResult{}, InvalidPublishingInput{Field: "post"}
	}
	if expectedRevision <= 0 {
		return DeleteResult{}, InvalidPublishingInput{Field: "revision"}
	}
	if err := ctx.Err(); err != nil {
		return DeleteResult{}, fmt.Errorf("delete post: %w", err)
	}
	atTime, err := publishingTime(clock)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("delete post: %w", err)
	}

	result := DeleteResult{}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		locked, err := queries.LockPostForEdit(ctx, postID)
		if err != nil {
			return fmt.Errorf("lock post for delete: %w", err)
		}
		if locked.PostID != postID || locked.AuthorID <= 0 || locked.Revision <= 0 || locked.TopicID <= 0 || locked.PostNumber <= 0 || locked.AreaID <= 0 {
			return fmt.Errorf("post delete lock returned an invalid result")
		}
		areaPolicy, err := lockedAreaPolicy(ctx, queries, locked.AreaID, locked.Visibility, locked.PostingMode)
		if err != nil {
			return fmt.Errorf("load post delete area policy: %w", err)
		}
		if !policy.CanViewArea(actor, areaPolicy) || !policy.CanViewTopic(actor, policy.TopicState(locked.TopicState)) ||
			!policy.CanEditPost(actor, policy.PostOwnership{AuthorID: locked.AuthorID}, policy.PostVisible) {
			return ErrPostDeleteDenied
		}
		if locked.Revision != expectedRevision {
			return ErrPostDeleteConflict
		}
		deleted, err := queries.SoftDeletePost(ctx, db.SoftDeletePostParams{
			AtTime: atTime, AuthorID: actor.UserID, PostID: postID, ExpectedRevision: expectedRevision,
		})
		if err != nil {
			return fmt.Errorf("soft delete post: %w", err)
		}
		if deleted.PostID != postID || deleted.TopicID != locked.TopicID || deleted.PostNumber != locked.PostNumber || deleted.Revision != expectedRevision {
			return fmt.Errorf("post delete returned an invalid result")
		}
		result = DeleteResult{TopicID: deleted.TopicID, PostID: deleted.PostID, PostNumber: deleted.PostNumber, Revision: deleted.Revision}
		return nil
	})
	if err != nil {
		return DeleteResult{}, fmt.Errorf("delete post transaction: %w", err)
	}
	return result, nil
}
