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
	ErrPostEditDenied   = errors.New("post edit denied")
	ErrPostEditConflict = errors.New("post edit revision conflict")
)

const maximumPostRevision = int32(1<<31 - 1)

type EditResult struct {
	TopicID     int64
	PostID      int64
	PostNumber  int32
	NodeOrdinal int64
	Revision    int32
}

// EditPost validates and renders one replacement body before locking the
// current post, topic, area, and group policy. It authorizes visibility and
// ownership before disclosing a revision conflict, then commits one guarded
// revision increment.
//
// Complexity: for bounded Markdown bytes m, actor groups a, area groups p,
// renderer work R(m), and database work D, time is
// O(m+a*p+a+p+R(m)+D), Omega(1), without one tight bound because invalid input
// and external database work vary. Auxiliary space is O(m+R(m)+p), Omega(1).
// There is one transaction with three application statements plus
// begin/commit, no retry, and no detached work.
func EditPost(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	postID int64,
	expectedRevision int32,
	markdownSource string,
) (EditResult, error) {
	if ctx == nil {
		return EditResult{}, fmt.Errorf("edit post context is required")
	}
	if beginner == nil {
		return EditResult{}, fmt.Errorf("edit post transaction beginner is required")
	}
	if clock == nil {
		return EditResult{}, fmt.Errorf("edit post clock is required")
	}
	if !actor.Valid() || !actor.Authenticated {
		return EditResult{}, fmt.Errorf("edit post actor is invalid")
	}
	if postID <= 0 {
		return EditResult{}, InvalidPublishingInput{Field: "post"}
	}
	if expectedRevision <= 0 || expectedRevision == maximumPostRevision {
		return EditResult{}, InvalidPublishingInput{Field: "revision"}
	}
	if err := ctx.Err(); err != nil {
		return EditResult{}, fmt.Errorf("edit post: %w", err)
	}
	rendered, err := renderPublishingDraft(markdownSource)
	if err != nil {
		return EditResult{}, err
	}
	renderedHTML, rendererVersion, err := rendered.PersistenceValues()
	if err != nil {
		return EditResult{}, fmt.Errorf("edit post body persistence: %w", err)
	}
	atTime, err := publishingTime(clock)
	if err != nil {
		return EditResult{}, fmt.Errorf("edit post: %w", err)
	}

	result := EditResult{}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		locked, err := queries.LockPostForEdit(ctx, postID)
		if err != nil {
			return fmt.Errorf("lock post for edit: %w", err)
		}
		if locked.PostID != postID || locked.AuthorID <= 0 || locked.Revision <= 0 || locked.TopicID <= 0 || locked.PostNumber <= 0 || locked.AreaID <= 0 {
			return fmt.Errorf("post edit lock returned an invalid result")
		}
		areaPolicy, err := lockedAreaPolicy(ctx, queries, locked.AreaID, locked.Visibility, locked.PostingMode)
		if err != nil {
			return fmt.Errorf("load post edit area policy: %w", err)
		}
		if !policy.CanViewArea(actor, areaPolicy) || !policy.CanViewTopic(actor, policy.TopicState(locked.TopicState)) ||
			!policy.CanEditPost(actor, policy.PostOwnership{AuthorID: locked.AuthorID}, policy.PostVisible) {
			return ErrPostEditDenied
		}
		if locked.Revision != expectedRevision {
			return ErrPostEditConflict
		}
		updated, err := queries.UpdatePostRevision(ctx, db.UpdatePostRevisionParams{
			MarkdownSource: markdownSource, RenderedHtml: renderedHTML, RendererVersion: rendererVersion,
			AtTime: atTime, PostID: postID, ExpectedRevision: expectedRevision,
		})
		if err != nil {
			return fmt.Errorf("update post revision: %w", err)
		}
		if updated.PostID != postID || updated.TopicID != locked.TopicID || updated.PostNumber != locked.PostNumber || updated.NodeOrdinal <= 0 || updated.Revision != expectedRevision+1 {
			return fmt.Errorf("post edit returned an invalid result")
		}
		result = EditResult{TopicID: updated.TopicID, PostID: updated.PostID, PostNumber: updated.PostNumber, NodeOrdinal: updated.NodeOrdinal, Revision: updated.Revision}
		return nil
	})
	if err != nil {
		return EditResult{}, fmt.Errorf("edit post transaction: %w", err)
	}
	return result, nil
}
