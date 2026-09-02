package policy

// CanViewTopic mirrors the closed topic-state visibility used by read queries:
// hidden topics require staff, while open, locked, and archived topics remain
// visible wherever their area is visible.
//
// Complexity: time and auxiliary space are tight Theta(1).
func CanViewTopic(actor AccessContext, state TopicState) bool {
	if !actor.Valid() {
		return false
	}
	switch state {
	case TopicOpen, TopicLocked, TopicArchived:
		return true
	case TopicHidden:
		return actor.Role == RoleModerator || actor.Role == RoleAdministrator
	default:
		return false
	}
}
