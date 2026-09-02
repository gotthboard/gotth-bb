package store

import (
	"context"
	"fmt"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

type visibleAreaQuerier interface {
	ListVisibleAreas(context.Context, db.ListVisibleAreasParams) ([]db.Area, error)
}

type visibleAreaBySlugQuerier interface {
	GetVisibleAreaBySlug(context.Context, db.GetVisibleAreaBySlugParams) (db.Area, error)
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
