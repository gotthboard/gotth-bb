package policy

// CanAdminister admits only one current, active local administrator authority.
// Authentication alone, external identity claims, moderation status, and stale
// presentation state never grant administrative mutation authority.
//
// Complexity: delegated authority validation is O(g) time for g local group
// IDs and O(1) auxiliary space. No allocation or I/O occurs.
func CanAdminister(actor AccessContext) bool {
	return actor.Valid() && actor.Authenticated && !actor.Suspended && actor.MutedUntil == nil && actor.Role == RoleAdministrator
}
