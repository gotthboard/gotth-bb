package policy

import "testing"

func TestCanViewTopicMatchesClosedReadPredicate(t *testing.T) {
	t.Parallel()

	visitor := AccessContext{}
	member := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}
	moderator := AccessContext{Authenticated: true, UserID: 12, Role: RoleModerator}
	for _, state := range []TopicState{TopicOpen, TopicLocked, TopicArchived} {
		if !CanViewTopic(visitor, state) || !CanViewTopic(member, state) || !CanViewTopic(moderator, state) {
			t.Fatalf("ordinary visible state %q was denied", state)
		}
	}
	if CanViewTopic(visitor, TopicHidden) || CanViewTopic(member, TopicHidden) || !CanViewTopic(moderator, TopicHidden) {
		t.Fatal("hidden topic visibility did not require staff")
	}
	if CanViewTopic(member, TopicState("future")) || CanViewTopic(AccessContext{Authenticated: true, UserID: 11, Role: Role(99)}, TopicOpen) {
		t.Fatal("unknown topic or actor state was admitted")
	}
}
