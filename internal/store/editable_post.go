package store

import (
	"context"
	"fmt"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

type editablePostQuerier interface {
	GetEditablePost(context.Context, db.GetEditablePostParams) (db.GetEditablePostRow, error)
}

type EditablePost struct {
	PostID         int64
	TopicID        int64
	PostNumber     int32
	MarkdownSource string
	Revision       int32
}

const maximumEditablePostRevision = int32(1<<31 - 1)

// GetEditablePost returns the current source and revision only when one active
// authenticated actor owns the visible post. The later edit transaction must
// reauthorize the locked current state; this read is presentation authority,
// not mutation authority.
//
// Complexity: with g actor groups and delegated query cost Q(g), time and
// auxiliary space are O(g+Q(g)), Omega(1), without a tight bound because
// PostgreSQL work varies. It performs at most one query and makes no copy of
// the actor group slice.
func GetEditablePost(ctx context.Context, querier editablePostQuerier, postID int64, actor policy.AccessContext) (EditablePost, error) {
	if ctx == nil {
		return EditablePost{}, fmt.Errorf("editable post context is required")
	}
	if querier == nil {
		return EditablePost{}, fmt.Errorf("editable post querier is required")
	}
	if !actor.Valid() {
		return EditablePost{}, fmt.Errorf("editable post access context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return EditablePost{}, fmt.Errorf("get editable post: %w", err)
	}
	if postID <= 0 || !actor.Authenticated || actor.Suspended || actor.MutedUntil != nil {
		return EditablePost{}, fmt.Errorf("get editable post: %w", pgx.ErrNoRows)
	}
	row, err := querier.GetEditablePost(ctx, db.GetEditablePostParams{
		PostID: postID, AuthorID: actor.UserID,
		IsStaff:  actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		GroupIds: actor.GroupIDs,
	})
	if err != nil {
		return EditablePost{}, fmt.Errorf("query editable post: %w", err)
	}
	if row.PostID != postID || row.TopicID <= 0 || row.PostNumber <= 0 || row.Revision <= 0 || row.Revision == maximumEditablePostRevision ||
		row.MarkdownSource == "" || len(row.MarkdownSource) > 65_536 || !utf8.ValidString(row.MarkdownSource) {
		return EditablePost{}, fmt.Errorf("editable post query returned an invalid row")
	}
	return EditablePost{
		PostID: row.PostID, TopicID: row.TopicID, PostNumber: row.PostNumber,
		MarkdownSource: row.MarkdownSource, Revision: row.Revision,
	}, nil
}
