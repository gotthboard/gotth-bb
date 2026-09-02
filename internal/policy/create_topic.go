package policy

// CanCreateTopic applies the complete area-level topic-publishing policy to one
// current access snapshot. Publishing requires authenticated visibility plus no
// suspension or active mute. Normal areas admit eligible members, read-only
// areas admit only moderator/administrator staff, and archived areas admit no
// actor until an administrator changes the area policy.
//
// Complexity: with a actor groups and p area groups, delegated visibility makes
// worst-case time O(a*p+a+p), Omega(1), and tight Theta(a*p+a+p) for a valid
// group-restricted miss after full validation. Across all cases one tight bound
// is not established because unauthenticated, suspended, muted, malformed, and
// archived inputs may return early. Auxiliary space is O(1), Omega(1), and
// tight Theta(1); no group copy or map is allocated.
func CanCreateTopic(actor AccessContext, areaPolicy AreaPolicy) bool {
	if !actor.Authenticated || actor.Suspended || actor.MutedUntil != nil {
		return false
	}
	if !CanViewArea(actor, areaPolicy) {
		return false
	}
	if areaPolicy.PostingMode == PostingArchived {
		return false
	}
	if areaPolicy.PostingMode == PostingReadOnly {
		return actor.Role == RoleModerator || actor.Role == RoleAdministrator
	}
	return true
}
