package store

import (
	"context"
	"fmt"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	TopicPageSize    int32 = 25
	MaximumTopicPage int32 = 10_000
)

type visibleAreaTopicPageQuerier interface {
	visibleAreaBySlugQuerier
	ListVisibleTopicsByAreaSlug(context.Context, db.ListVisibleTopicsByAreaSlugParams) ([]db.ListVisibleTopicsByAreaSlugRow, error)
	ListAuthenticatedVisibleTopicsByAreaSlug(context.Context, db.ListAuthenticatedVisibleTopicsByAreaSlugParams) ([]db.ListAuthenticatedVisibleTopicsByAreaSlugRow, error)
}

// ReadState is the closed signed-in state of one authorized topic.
type ReadState string

const (
	ReadStateNew    ReadState = "new"
	ReadStateUnread ReadState = "unread"
	ReadStateRead   ReadState = "read"
)

// VisibleAreaTopic is the public topic-list projection. ReadState is nil for
// visitors and non-nil for authenticated actors; private marker details never
// leave the store boundary.
type VisibleAreaTopic struct {
	TopicID            int64
	Title              string
	Slug               pgtype.Text
	State              string
	PinnedAt           pgtype.Timestamptz
	ReplyCount         int32
	AuthorDisplayName  string
	LastActivityAt     pgtype.Timestamptz
	TotalVisibleTopics int64
	ReadState          *ReadState
}

// VisibleAreaTopicPage is one access-filtered conventional topic-list page and
// the visible area metadata required to render its breadcrumb and heading.
type VisibleAreaTopicPage struct {
	Area        db.Area
	Topics      []VisibleAreaTopic
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
	staff := actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator
	var topics []VisibleAreaTopic
	if actor.Authenticated {
		rows, queryErr := querier.ListAuthenticatedVisibleTopicsByAreaSlug(ctx, db.ListAuthenticatedVisibleTopicsByAreaSlugParams{
			ActorUserID: actor.UserID, AreaSlug: slug, IsStaff: staff, GroupIds: actor.GroupIDs,
			PageOffset: offset, PageLimit: TopicPageSize,
		})
		if queryErr != nil {
			return VisibleAreaTopicPage{}, fmt.Errorf("query authenticated visible area topics: %w", queryErr)
		}
		topics = make([]VisibleAreaTopic, len(rows))
		for index, row := range rows {
			topic, valid := authenticatedVisibleAreaTopicFromRow(row)
			if !valid {
				return VisibleAreaTopicPage{}, fmt.Errorf("query authenticated visible area topics: malformed row %d", index)
			}
			topics[index] = topic
		}
	} else {
		rows, queryErr := querier.ListVisibleTopicsByAreaSlug(ctx, db.ListVisibleTopicsByAreaSlugParams{
			AreaSlug: slug, IsStaff: staff, IsMember: false, GroupIds: actor.GroupIDs,
			PageOffset: offset, PageLimit: TopicPageSize,
		})
		if queryErr != nil {
			return VisibleAreaTopicPage{}, fmt.Errorf("query visible area topics: %w", queryErr)
		}
		topics = make([]VisibleAreaTopic, len(rows))
		for index, row := range rows {
			topic, valid := visibleAreaTopicFromRow(row)
			if !valid {
				return VisibleAreaTopicPage{}, fmt.Errorf("query visible area topics: malformed row %d", index)
			}
			topics[index] = topic
		}
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

func visibleAreaTopicFromRow(row db.ListVisibleTopicsByAreaSlugRow) (VisibleAreaTopic, bool) {
	if row.TopicID <= 0 || row.Title == "" || row.AuthorDisplayName == "" || !validVisibleTopicState(row.State) ||
		row.ReplyCount < 0 || !finiteTimestamp(row.LastActivityAt) || row.TotalVisibleTopics <= 0 ||
		row.PinnedAt.Valid && row.PinnedAt.InfinityModifier != pgtype.Finite || row.Slug.Valid && row.Slug.String == "" {
		return VisibleAreaTopic{}, false
	}
	return VisibleAreaTopic{
		TopicID: row.TopicID, Title: row.Title, Slug: row.Slug, State: row.State, PinnedAt: row.PinnedAt,
		ReplyCount: row.ReplyCount, AuthorDisplayName: row.AuthorDisplayName,
		LastActivityAt: row.LastActivityAt, TotalVisibleTopics: row.TotalVisibleTopics,
	}, true
}

func authenticatedVisibleAreaTopicFromRow(row db.ListAuthenticatedVisibleTopicsByAreaSlugRow) (VisibleAreaTopic, bool) {
	topic, valid := visibleAreaTopicFromRow(db.ListVisibleTopicsByAreaSlugRow{
		TopicID: row.TopicID, Title: row.Title, Slug: row.Slug, State: row.State, PinnedAt: row.PinnedAt,
		ReplyCount: row.ReplyCount, AuthorDisplayName: row.AuthorDisplayName,
		LastActivityAt: row.LastActivityAt, TotalVisibleTopics: row.TotalVisibleTopics,
	})
	markerPresent := row.LastReadPostNumber.Valid && row.ReadAt.Valid
	markerAbsent := !row.LastReadPostNumber.Valid && !row.ReadAt.Valid
	if !valid || row.NextPostNumber < 2 || row.ReadHead < 0 || row.ReadHead > row.NextPostNumber-1 ||
		!markerPresent && !markerAbsent || markerPresent && (row.LastReadPostNumber.Int32 <= 0 ||
		row.LastReadPostNumber.Int32 > row.NextPostNumber-1 || row.ReadAt.InfinityModifier != pgtype.Finite) {
		return VisibleAreaTopic{}, false
	}
	expected := ReadStateRead
	if row.ReadHead > 0 && markerAbsent {
		expected = ReadStateNew
	} else if row.ReadHead > 0 && row.LastReadPostNumber.Int32 < row.ReadHead {
		expected = ReadStateUnread
	}
	if ReadState(row.ReadState) != expected {
		return VisibleAreaTopic{}, false
	}
	topic.ReadState = &expected
	return topic, true
}

func finiteTimestamp(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite
}
