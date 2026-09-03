package store

import (
	"context"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type visibleAreaQuerier interface {
	ListVisibleAreas(context.Context, db.ListVisibleAreasParams) ([]db.Area, error)
}

type visibleAreaSummaryQuerier interface {
	ListVisibleAreaSummaries(context.Context, db.ListVisibleAreaSummariesParams) ([]db.ListVisibleAreaSummariesRow, error)
}

type visibleAreaBySlugQuerier interface {
	GetVisibleAreaBySlug(context.Context, db.GetVisibleAreaBySlugParams) (db.Area, error)
}

// VisibleAreaLatestPost is the bounded latest-post metadata exposed by the
// board index. It contains no user-authored post body.
type VisibleAreaLatestPost struct {
	TopicID     int64
	TopicTitle  string
	PostID      int64
	PostNumber  int32
	TreeOrdinal int64
	Author      string
	CreatedAt   time.Time
}

// VisibleAreaSummary is one actor-visible area and its exact actor-visible
// forum statistics.
type VisibleAreaSummary struct {
	Area       db.Area
	TopicCount int64
	PostCount  int64
	LatestPost *VisibleAreaLatestPost
}

// ListVisibleAreaSummaries validates one server-owned access snapshot,
// delegates visibility filtering and aggregation to one PostgreSQL statement,
// and rejects malformed or partially nullable projections before returning
// any row.
//
// Complexity: with g actor groups, r visible areas, t actor-visible topics,
// and p non-deleted posts, delegated query/scanning cost Q(g,r,t,p), time is
// O(g+r+Q), Omega(1), with no tighter bound because PostgreSQL work varies.
// Auxiliary space is O(A(Q)+r), Omega(1); the result projection owns one
// bounded latest-post value per non-empty area and never copies post bodies.
func ListVisibleAreaSummaries(ctx context.Context, querier visibleAreaSummaryQuerier, actor policy.AccessContext) ([]VisibleAreaSummary, error) {
	if ctx == nil {
		return nil, fmt.Errorf("visible area summary context is required")
	}
	if querier == nil {
		return nil, fmt.Errorf("visible area summary querier is required")
	}
	if !actor.Valid() {
		return nil, fmt.Errorf("visible area summary access context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list visible area summaries: %w", err)
	}
	rows, err := querier.ListVisibleAreaSummaries(ctx, db.ListVisibleAreaSummariesParams{
		IsStaff:  actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember: actor.Authenticated,
		GroupIds: actor.GroupIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("query visible area summaries: %w", err)
	}
	summaries := make([]VisibleAreaSummary, len(rows))
	for index, row := range rows {
		summary, valid := visibleAreaSummaryFromRow(row)
		if !valid {
			return nil, fmt.Errorf("query visible area summaries: malformed row %d", index)
		}
		summaries[index] = summary
	}
	return summaries, nil
}

// visibleAreaSummaryFromRow converts only a complete schema-valid aggregate
// row. A latest-post tuple is either entirely absent or entirely present.
//
// Complexity: time and auxiliary space are tight Theta(1).
func visibleAreaSummaryFromRow(row db.ListVisibleAreaSummariesRow) (VisibleAreaSummary, bool) {
	validArea := row.ID > 0 && policy.ValidAreaSlug(row.Slug) && row.Name != "" && row.DisplayOrder >= 0 &&
		row.CreatedBy > 0 && row.UpdatedBy > 0 && row.CreatedAt.Valid && row.CreatedAt.InfinityModifier == pgtype.Finite &&
		row.UpdatedAt.Valid && row.UpdatedAt.InfinityModifier == pgtype.Finite && !row.UpdatedAt.Time.Before(row.CreatedAt.Time) &&
		(row.Visibility == string(policy.VisibilityPublic) || row.Visibility == string(policy.VisibilityAuthenticated) || row.Visibility == string(policy.VisibilityGroups)) &&
		(row.PostingMode == string(policy.PostingNormal) || row.PostingMode == string(policy.PostingReadOnly) || row.PostingMode == string(policy.PostingArchived))
	if !validArea || row.TopicCount < 0 || row.PostCount < 0 || row.PostCount > 0 && row.TopicCount == 0 {
		return VisibleAreaSummary{}, false
	}
	present := row.LatestTopicID.Valid && row.LatestTopicTitle.Valid && row.LatestPostID.Valid &&
		row.LatestPostNumber.Valid && row.LatestPostOrdinal.Valid && row.LatestPostAuthor.Valid && row.LatestPostCreatedAt.Valid
	absent := !row.LatestTopicID.Valid && !row.LatestTopicTitle.Valid && !row.LatestPostID.Valid &&
		!row.LatestPostNumber.Valid && !row.LatestPostOrdinal.Valid && !row.LatestPostAuthor.Valid && !row.LatestPostCreatedAt.Valid
	if row.PostCount == 0 {
		if !absent {
			return VisibleAreaSummary{}, false
		}
	} else if !present || row.LatestTopicID.Int64 <= 0 || row.LatestTopicTitle.String == "" ||
		row.LatestPostID.Int64 <= 0 || row.LatestPostNumber.Int32 <= 0 || row.LatestPostOrdinal.Int64 <= 0 || row.LatestPostAuthor.String == "" ||
		row.LatestPostCreatedAt.InfinityModifier != pgtype.Finite {
		return VisibleAreaSummary{}, false
	}
	area := db.Area{
		ID: row.ID, Slug: row.Slug, Name: row.Name, Description: row.Description,
		DisplayOrder: row.DisplayOrder, Visibility: row.Visibility, PostingMode: row.PostingMode,
		CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	summary := VisibleAreaSummary{Area: area, TopicCount: row.TopicCount, PostCount: row.PostCount}
	if present {
		summary.LatestPost = &VisibleAreaLatestPost{
			TopicID: row.LatestTopicID.Int64, TopicTitle: row.LatestTopicTitle.String,
			PostID: row.LatestPostID.Int64, PostNumber: row.LatestPostNumber.Int32, TreeOrdinal: row.LatestPostOrdinal.Int64,
			Author: row.LatestPostAuthor.String, CreatedAt: row.LatestPostCreatedAt.Time,
		}
	}
	return summary, true
}

// ListVisibleAreas validates one server-owned access snapshot, derives the
// closed member/staff booleans and local group IDs, and delegates filtering to
// PostgreSQL. It returns no partial rows when the query fails. Browser or form
// fields never participate in the derived query authority.
//
// Complexity: with g actor groups and delegated query/scanning cost Q(g,r) for
// r returned rows, time is O(g+Q(g,r)), Omega(1), with no tight Theta bound
// because PostgreSQL and driver work varies. Auxiliary space is O(A(Q)+r),
// Omega(1), with no tight Theta bound established; the group slice is passed
// read-only without a copy and returned rows are allocated by the querier.
func ListVisibleAreas(ctx context.Context, querier visibleAreaQuerier, actor policy.AccessContext) ([]db.Area, error) {
	if ctx == nil {
		return nil, fmt.Errorf("visible area list context is required")
	}
	if querier == nil {
		return nil, fmt.Errorf("visible area list querier is required")
	}
	if !actor.Valid() {
		return nil, fmt.Errorf("visible area list access context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list visible areas: %w", err)
	}
	areas, err := querier.ListVisibleAreas(ctx, db.ListVisibleAreasParams{
		IsStaff:  actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember: actor.Authenticated,
		GroupIds: actor.GroupIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("query visible areas: %w", err)
	}
	return areas, nil
}

// GetVisibleAreaBySlug validates one server-owned access snapshot, derives the
// closed member/staff booleans and local group IDs, and delegates both slug
// matching and visibility enforcement to PostgreSQL. Slugs outside the schema
// grammar, missing slugs, and unauthorized slugs all retain the query's same
// no-row behavior.
//
// Complexity: with s slug bytes, g actor groups, and delegated indexed-query
// cost Q(g), time is O(s+g+Q(g)), Omega(1), with no tight Theta bound because
// PostgreSQL and driver work varies. Auxiliary space is O(A(Q)), Omega(1),
// with no tight Theta bound established; the group slice is passed read-only.
func GetVisibleAreaBySlug(ctx context.Context, querier visibleAreaBySlugQuerier, slug string, actor policy.AccessContext) (db.Area, error) {
	if ctx == nil {
		return db.Area{}, fmt.Errorf("visible area lookup context is required")
	}
	if querier == nil {
		return db.Area{}, fmt.Errorf("visible area lookup querier is required")
	}
	if !actor.Valid() {
		return db.Area{}, fmt.Errorf("visible area lookup access context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return db.Area{}, fmt.Errorf("get visible area by slug: %w", err)
	}
	if !policy.ValidAreaSlug(slug) {
		return db.Area{}, fmt.Errorf("query visible area by slug: %w", pgx.ErrNoRows)
	}
	area, err := querier.GetVisibleAreaBySlug(ctx, db.GetVisibleAreaBySlugParams{
		Slug:     slug,
		IsStaff:  actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
		IsMember: actor.Authenticated,
		GroupIds: actor.GroupIDs,
	})
	if err != nil {
		return db.Area{}, fmt.Errorf("query visible area by slug: %w", err)
	}
	return area, nil
}
