package policy

type TopicState string

const (
	TopicOpen     TopicState = "open"
	TopicLocked   TopicState = "locked"
	TopicHidden   TopicState = "hidden"
	TopicArchived TopicState = "archived"
)

// CanReply applies the complete area and topic publishing policy to one
// current access snapshot. Archived areas or topics admit nobody. Members may
// reply only to open topics in normal visible areas; moderator/administrator
// staff may also reply in read-only areas and locked or hidden topics.
//
// Complexity: with a actor groups and p area groups, delegated visibility
// makes worst-case time O(a*p+a+p), Omega(1), and tight Theta(a*p+a+p) for a
// valid group-restricted miss after full validation. Across all cases one
// tight bound is not established because invalid or denied states may return
// early. Auxiliary space is tight Theta(1); no slice is copied.
func CanReply(actor AccessContext, areaPolicy AreaPolicy, topicState TopicState) bool {
	if topicState != TopicOpen && topicState != TopicLocked && topicState != TopicHidden && topicState != TopicArchived {
		return false
	}
	if topicState == TopicArchived || !CanCreateTopic(actor, areaPolicy) {
		return false
	}
	if topicState == TopicOpen {
		return true
	}
	return actor.Role == RoleModerator || actor.Role == RoleAdministrator
}
