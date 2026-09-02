package store

import (
	"context"
	"fmt"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

const (
	TopicPageSize    int32 = 25
	MaximumTopicPage int32 = 10_000
)

type visibleAreaTopicPageQuerier interface {
	visibleAreaBySlugQuerier
	ListVisibleTopicsByAreaSlug(context.Context, db.ListVisibleTopicsByAreaSlugParams) ([]db.ListVisibleTopicsByAreaSlugRow, error)
}

// VisibleAreaTopicPage is one access-filtered conventional topic-list page and
// the visible area metadata required to render its breadcrumb and heading.
type VisibleAreaTopicPage struct {
	Area        db.Area
	Topics      []db.ListVisibleTopicsByAreaSlugRow
	Number      int32
	TotalTopics int64
	TotalPages  int64
}

// GetVisibleAreaTopicPage resolves visible area metadata, rechecks the same
// authority inside the topic query, applies fixed bounded pagination, and
// validates the query's exact-count metadata before returning it. Invalid or
// empty later pages retain the same no-row behavior as an invisible area.
//
// Complexity: with s slug bytes, g actor groups, bounded page rows r <= 25,
// visible-topic count n, and delegated query costs A(s,g) and T(g,n), time is
// O(s+g+r+A(s,g)+T(g,n)), Omega(1), without a tight Theta bound because
// PostgreSQL work varies. Auxiliary space is O(r+A+T), Omega(1); returned query
// storage is reused without another copy.
func GetVisibleAreaTopicPage(ctx context.Context, querier visibleAreaTopicPageQuerier, slug string, page int32, actor policy.AccessContext) (VisibleAreaTopicPage, error) {
	if ctx == nil {
		return VisibleAreaTopicPage{}, fmt.Errorf("visible area topic page context is required")
	}
	if querier == nil {
		return VisibleAreaTopicPage{}, fmt.Errorf("visible area topic page querier is required")
	}
	if err := ctx.Err(); err != nil {
		return VisibleAreaTopicPage{}, fmt.Errorf("get visible area topic page: %w", err)
	}
	if page < 1 || page > MaximumTopicPage {
		return VisibleAreaTopicPage{}, fmt.Errorf("get visible area topic page: %w", pgx.ErrNoRows)
	}
	area, err := GetVisibleAreaBySlug(ctx, querier, slug, actor)
	if err != nil {
		return VisibleAreaTopicPage{}, fmt.Errorf("get topic page area: %w", err)
	}
	offset := (page - 1) * TopicPageSize
	topics, err := querier.ListVisibleTopicsByAreaSlug(ctx, db.ListVisibleTopicsByAreaSlugParams{
		AreaSlug:   slug,
		IsStaff:    actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember:   actor.Authenticated,
		GroupIds:   actor.GroupIDs,
		PageOffset: offset,
		PageLimit:  TopicPageSize,
	})
	if err != nil {
		return VisibleAreaTopicPage{}, fmt.Errorf("query visible area topics: %w", err)
	}
	if len(topics) == 0 {
		if page != 1 {
			return VisibleAreaTopicPage{}, fmt.Errorf("get visible area topic page: %w", pgx.ErrNoRows)
		}
		return VisibleAreaTopicPage{Area: area, Topics: topics, Number: page}, nil
	}
	if len(topics) > int(TopicPageSize) {
		return VisibleAreaTopicPage{}, fmt.Errorf("visible area topic query exceeded page size")
	}
	totalTopics := topics[0].TotalVisibleTopics
	minimumTotal := int64(offset) + int64(len(topics))
	if totalTopics < minimumTotal {
		return VisibleAreaTopicPage{}, fmt.Errorf("visible area topic query returned an invalid total")
	}
	for index := 1; index < len(topics); index++ {
		if topics[index].TotalVisibleTopics != totalTopics {
			return VisibleAreaTopicPage{}, fmt.Errorf("visible area topic query returned inconsistent totals")
		}
	}
	remaining := totalTopics - int64(offset)
	expectedRows := int64(TopicPageSize)
	if remaining < expectedRows {
		expectedRows = remaining
	}
	if int64(len(topics)) != expectedRows {
		return VisibleAreaTopicPage{}, fmt.Errorf("visible area topic query returned an incomplete page")
	}
	totalPages := int64(1) + (totalTopics-1)/int64(TopicPageSize)
	return VisibleAreaTopicPage{
		Area: area, Topics: topics, Number: page, TotalTopics: totalTopics, TotalPages: totalPages,
	}, nil
}
