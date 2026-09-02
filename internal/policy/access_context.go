package policy

// Valid reports whether an access snapshot is a canonical anonymous context or
// a canonical authenticated local member/staff context. It fails closed on
// contradictory identity facts, unknown roles, and nonpositive local group
// IDs. Suspension, mute, and validation timestamps are state facts interpreted
// by operation-specific policies rather than structural invalidity.
//
// Complexity: with g group IDs, time is O(g), Omega(1), and tight Theta(g) for
// a valid authenticated context because every group ID is checked. Across all
// inputs one tight bound is not established because malformed and anonymous
// contexts may return early. Auxiliary space is O(1), Omega(1), and tight
// Theta(1); the caller retains ownership of the group slice.
func (actor AccessContext) Valid() bool {
	if !actor.Authenticated {
		return actor.UserID == 0 && actor.Role == 0 && len(actor.GroupIDs) == 0
	}
	if actor.UserID <= 0 || actor.Role != RoleMember && actor.Role != RoleModerator && actor.Role != RoleAdministrator {
		return false
	}
	for _, groupID := range actor.GroupIDs {
		if groupID <= 0 {
			return false
		}
	}
	return true
}
