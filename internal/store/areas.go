package store

import (
	"context"
	"fmt"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

type visibleAreaQuerier interface {
	ListVisibleAreas(context.Context, db.ListVisibleAreasParams) ([]db.Area, error)
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
