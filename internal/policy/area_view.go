package policy

import "time"

type Role uint8

const (
	RoleMember Role = iota + 1
	RoleModerator
	RoleAdministrator
)

type AccessContext struct {
	Authenticated bool
	UserID        int64
	Role          Role
	GroupIDs      []int64
	Suspended     bool
	MutedUntil    *time.Time
	ValidatedAt   time.Time
}

type Visibility string

const (
	VisibilityPublic        Visibility = "public"
	VisibilityAuthenticated Visibility = "authenticated"
	VisibilityGroups        Visibility = "groups"
)

type PostingMode string

const (
	PostingNormal   PostingMode = "normal"
	PostingReadOnly PostingMode = "read_only"
	PostingArchived PostingMode = "archived"
)

type AreaPolicy struct {
	Visibility  Visibility
	PostingMode PostingMode
	GroupIDs    []int64
}

// CanViewArea applies the in-memory area visibility policy for one verified
// actor snapshot. It fails closed on contradictory identity state, unknown
// closed values, invalid group IDs, or group mappings attached to non-group
// visibility. Suspension and mute state do not remove otherwise valid reads;
// publishing policies enforce those restrictions separately.
//
// Complexity: with a actor groups and p area groups, worst-case time is
// O(a*p+a+p), Omega(1), and tight Theta(a*p+a+p) for a valid group-restricted
// miss after full validation. Across all visibility/input cases one tight bound
// is not established because malformed and public inputs can return early.
// Auxiliary space is O(1), Omega(1), and tight Theta(1); slice storage is owned
// by the caller and no group map or copy is allocated.
func CanViewArea(actor AccessContext, policy AreaPolicy) bool {
	if policy.PostingMode != PostingNormal && policy.PostingMode != PostingReadOnly && policy.PostingMode != PostingArchived {
		return false
	}
	if policy.Visibility != VisibilityPublic && policy.Visibility != VisibilityAuthenticated && policy.Visibility != VisibilityGroups {
		return false
	}
	if policy.Visibility != VisibilityGroups && len(policy.GroupIDs) != 0 {
		return false
	}
	for _, groupID := range policy.GroupIDs {
		if groupID <= 0 {
			return false
		}
	}
	if !actor.Valid() {
		return false
	}
	if !actor.Authenticated {
		return policy.Visibility == VisibilityPublic
	}
	if actor.Role == RoleModerator || actor.Role == RoleAdministrator {
		return true
	}
	if policy.Visibility == VisibilityPublic || policy.Visibility == VisibilityAuthenticated {
		return true
	}
	for _, actorGroupID := range actor.GroupIDs {
		for _, areaGroupID := range policy.GroupIDs {
			if actorGroupID == areaGroupID {
				return true
			}
		}
	}
	return false
}
