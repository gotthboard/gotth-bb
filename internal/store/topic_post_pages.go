package store

import (
	"context"
	"fmt"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	PostPageSize    int32 = 25
	MaximumPostPage int32 = 10_000
)

type visibleTopicPostPageQuerier interface {
	GetVisibleTopicPostPage(context.Context, db.GetVisibleTopicPostPageParams) ([]db.GetVisibleTopicPostPageRow, error)
}

// VisibleTopicPostPage is one atomically access-filtered topic metadata and
// chronological visible-post page. Rows retain the generated nullable sentinel
// only for an authorized topic with no visible posts on page one.
type VisibleTopicPostPage struct {
	Rows       []db.GetVisibleTopicPostPageRow
	Number     int32
	TotalPosts int64
	TotalPages int64
}

// GetVisibleTopicPostPage validates one canonical authority and bounded topic
// page request, derives only closed SQL authority parameters, and rejects any
// malformed or incomplete generated row set before returning it to presentation.
// Missing/inaccessible topics, invalid identifiers/pages, and empty later pages
// retain the same no-row behavior.
//
// Complexity: with g actor groups, r <= 25 returned rows, and delegated query
// cost Q(g,n), time is O(g+r+Q), Omega(1), without a tight Theta bound because
// PostgreSQL work varies. Auxiliary space is O(A(Q)), Omega(1); the generated
// row slice and actor group slice are reused without copies. No operation is
// retried or detached.
func GetVisibleTopicPostPage(
	ctx context.Context,
	querier visibleTopicPostPageQuerier,
	topicID int64,
	page int32,
	actor policy.AccessContext,
) (VisibleTopicPostPage, error) {
	if ctx == nil {
		return VisibleTopicPostPage{}, fmt.Errorf("visible topic post page context is required")
	}
	if querier == nil {
		return VisibleTopicPostPage{}, fmt.Errorf("visible topic post page querier is required")
	}
	if !actor.Valid() {
		return VisibleTopicPostPage{}, fmt.Errorf("visible topic post page access context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return VisibleTopicPostPage{}, fmt.Errorf("get visible topic post page: %w", err)
	}
	if topicID <= 0 || page < 1 || page > MaximumPostPage {
		return VisibleTopicPostPage{}, fmt.Errorf("get visible topic post page: %w", pgx.ErrNoRows)
	}
	offset := (page - 1) * PostPageSize
	rows, err := querier.GetVisibleTopicPostPage(ctx, db.GetVisibleTopicPostPageParams{
		TopicID: topicID, IsStaff: actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember: actor.Authenticated, GroupIds: actor.GroupIDs, PageOffset: offset, PageLimit: PostPageSize,
	})
	if err != nil {
		return VisibleTopicPostPage{}, fmt.Errorf("query visible topic post page: %w", err)
	}
	if len(rows) == 0 {
		return VisibleTopicPostPage{}, fmt.Errorf("get visible topic post page: %w", pgx.ErrNoRows)
	}
	if len(rows) > int(PostPageSize) {
		return VisibleTopicPostPage{}, fmt.Errorf("visible topic post query exceeded page size")
	}
	first := rows[0]
	validMetadata := first.AreaID > 0 && policy.ValidAreaSlug(first.AreaSlug) && first.AreaName != "" &&
		validPostingMode(first.AreaPostingMode) &&
		first.TopicID == topicID && first.TopicFirstPostID > 0 && first.TopicTitle != "" && validVisibleTopicState(first.TopicState) &&
		first.TopicCreatedAt.Valid && first.TopicCreatedAt.InfinityModifier == pgtype.Finite && first.TopicAuthorDisplayName != "" &&
		(!first.TopicPinnedAt.Valid || first.TopicPinnedAt.InfinityModifier == pgtype.Finite && !first.TopicPinnedAt.Time.Before(first.TopicCreatedAt.Time)) &&
		(first.TopicState != "hidden" || actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator)
	if !validMetadata || first.TotalVisiblePosts < 0 {
		return VisibleTopicPostPage{}, fmt.Errorf("visible topic post query returned invalid metadata")
	}
	if first.TotalVisiblePosts == 0 {
		if page != 1 || len(rows) != 1 || !validEmptyVisiblePost(first) {
			return VisibleTopicPostPage{}, fmt.Errorf("visible topic post query returned an invalid empty page")
		}
		return VisibleTopicPostPage{Rows: rows, Number: page}, nil
	}
	offset64 := int64(offset)
	remaining := first.TotalVisiblePosts - offset64
	expectedRows := int64(PostPageSize)
	if remaining < expectedRows {
		expectedRows = remaining
	}
	if remaining <= 0 || int64(len(rows)) != expectedRows {
		return VisibleTopicPostPage{}, fmt.Errorf("visible topic post query returned an incomplete page")
	}
	previousPostNumber := int32(0)
	for _, row := range rows {
		if !sameVisibleTopicMetadata(first, row) || row.TotalVisiblePosts != first.TotalVisiblePosts ||
			!validVisiblePost(row, previousPostNumber) {
			return VisibleTopicPostPage{}, fmt.Errorf("visible topic post query returned malformed rows")
		}
		previousPostNumber = row.PostNumber.Int32
	}
	totalPages := int64(1) + (first.TotalVisiblePosts-1)/int64(PostPageSize)
	return VisibleTopicPostPage{
		Rows: rows, Number: page, TotalPosts: first.TotalVisiblePosts, TotalPages: totalPages,
	}, nil
}

// validVisibleTopicState accepts only the closed topic states stored by the
// schema. Authorization for hidden state remains a separate caller check.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validVisibleTopicState(state string) bool {
	return state == "open" || state == "locked" || state == "archived" || state == "hidden"
}

// validPostingMode accepts only the area publishing modes closed by schema.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validPostingMode(mode string) bool {
	return mode == string(policy.PostingNormal) || mode == string(policy.PostingReadOnly) || mode == string(policy.PostingArchived)
}

// sameVisibleTopicMetadata proves that repeated window rows describe exactly
// one authorized topic and breadcrumb authority.
//
// Complexity: time and auxiliary space are tight Theta(1).
func sameVisibleTopicMetadata(first, row db.GetVisibleTopicPostPageRow) bool {
	return row.AreaID == first.AreaID && row.AreaSlug == first.AreaSlug && row.AreaName == first.AreaName &&
		row.AreaDescription == first.AreaDescription && row.AreaPostingMode == first.AreaPostingMode &&
		row.TopicID == first.TopicID && row.TopicFirstPostID == first.TopicFirstPostID && row.TopicTitle == first.TopicTitle &&
		row.TopicState == first.TopicState && row.TopicPinnedAt == first.TopicPinnedAt && row.TopicCreatedAt == first.TopicCreatedAt &&
		row.TopicAuthorDisplayName == first.TopicAuthorDisplayName
}

// validVisiblePost validates required nonnull post fields and strict
// chronological ordering before presentation can consume persisted HTML.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validVisiblePost(row db.GetVisibleTopicPostPageRow, previousPostNumber int32) bool {
	if !row.PostID.Valid || row.PostID.Int64 <= 0 || !row.PostNumber.Valid || row.PostNumber.Int32 <= previousPostNumber ||
		!row.RenderedHtml.Valid || !row.RendererVersion.Valid || row.RendererVersion.String == "" ||
		!row.Revision.Valid || row.Revision.Int32 <= 0 || !row.PostCreatedAt.Valid || row.PostCreatedAt.InfinityModifier != pgtype.Finite ||
		!row.PostUpdatedAt.Valid || row.PostUpdatedAt.InfinityModifier != pgtype.Finite ||
		row.PostUpdatedAt.Time.Before(row.PostCreatedAt.Time) || !row.PostAuthorID.Valid || row.PostAuthorID.Int64 <= 0 ||
		!row.PostAuthorDisplayName.Valid || row.PostAuthorDisplayName.String == "" {
		return false
	}
	if row.Revision.Int32 == 1 {
		return !row.PostEditedAt.Valid
	}
	return row.PostEditedAt.Valid && row.PostEditedAt.InfinityModifier == pgtype.Finite && !row.PostEditedAt.Time.Before(row.PostCreatedAt.Time)
}

// validEmptyVisiblePost requires every nullable post projection to be SQL NULL
// for the authorized empty-topic sentinel.
//
// Complexity: time and auxiliary space are tight Theta(1).
func validEmptyVisiblePost(row db.GetVisibleTopicPostPageRow) bool {
	return !row.PostID.Valid && !row.PostNumber.Valid && !row.RenderedHtml.Valid && !row.RendererVersion.Valid &&
		!row.Revision.Valid && !row.PostCreatedAt.Valid && !row.PostUpdatedAt.Valid && !row.PostEditedAt.Valid &&
		!row.PostAuthorID.Valid && !row.PostAuthorDisplayName.Valid
}
