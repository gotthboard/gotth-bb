package policy

import (
	"testing"
	"time"
)

func TestCanReplyCoversPostingTopicStateAndRoleMatrix(t *testing.T) {
	t.Parallel()

	member := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, GroupIDs: []int64{7}}
	moderator := AccessContext{Authenticated: true, UserID: 12, Role: RoleModerator}
	administrator := AccessContext{Authenticated: true, UserID: 13, Role: RoleAdministrator}
	for _, test := range []struct {
		name       string
		actor      AccessContext
		areaPolicy AreaPolicy
		topicState TopicState
		want       bool
	}{
		{name: "visitor open", areaPolicy: topicPolicy(VisibilityPublic, PostingNormal), topicState: TopicOpen},
		{name: "member open", actor: member, areaPolicy: topicPolicy(VisibilityPublic, PostingNormal), topicState: TopicOpen, want: true},
		{name: "member matching group open", actor: member, areaPolicy: topicPolicy(VisibilityGroups, PostingNormal, 7), topicState: TopicOpen, want: true},
		{name: "member nonmatching group open", actor: member, areaPolicy: topicPolicy(VisibilityGroups, PostingNormal, 8), topicState: TopicOpen},
		{name: "member read only", actor: member, areaPolicy: topicPolicy(VisibilityPublic, PostingReadOnly), topicState: TopicOpen},
		{name: "member locked", actor: member, areaPolicy: topicPolicy(VisibilityPublic, PostingNormal), topicState: TopicLocked},
		{name: "member hidden", actor: member, areaPolicy: topicPolicy(VisibilityPublic, PostingNormal), topicState: TopicHidden},
		{name: "moderator locked", actor: moderator, areaPolicy: topicPolicy(VisibilityPublic, PostingNormal), topicState: TopicLocked, want: true},
		{name: "moderator hidden", actor: moderator, areaPolicy: topicPolicy(VisibilityGroups, PostingNormal), topicState: TopicHidden, want: true},
		{name: "moderator read only", actor: moderator, areaPolicy: topicPolicy(VisibilityAuthenticated, PostingReadOnly), topicState: TopicOpen, want: true},
		{name: "administrator locked read only", actor: administrator, areaPolicy: topicPolicy(VisibilityGroups, PostingReadOnly), topicState: TopicLocked, want: true},
		{name: "member archived topic", actor: member, areaPolicy: topicPolicy(VisibilityPublic, PostingNormal), topicState: TopicArchived},
		{name: "moderator archived topic", actor: moderator, areaPolicy: topicPolicy(VisibilityPublic, PostingNormal), topicState: TopicArchived},
		{name: "administrator archived area", actor: administrator, areaPolicy: topicPolicy(VisibilityPublic, PostingArchived), topicState: TopicOpen},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := CanReply(test.actor, test.areaPolicy, test.topicState); got != test.want {
				t.Fatalf("CanReply(%+v, %+v, %q) = %t, want %t", test.actor, test.areaPolicy, test.topicState, got, test.want)
			}
		})
	}
}

func TestCanReplyDeniesRestrictedAndMalformedInputs(t *testing.T) {
	t.Parallel()

	muteExpiry := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	validArea := topicPolicy(VisibilityPublic, PostingNormal)
	for _, test := range []struct {
		name       string
		actor      AccessContext
		areaPolicy AreaPolicy
		topicState TopicState
	}{
		{name: "suspended", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, Suspended: true}, areaPolicy: validArea, topicState: TopicOpen},
		{name: "muted", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, MutedUntil: &muteExpiry}, areaPolicy: validArea, topicState: TopicOpen},
		{name: "malformed actor", actor: AccessContext{Authenticated: true, UserID: 11, Role: Role(99)}, areaPolicy: validArea, topicState: TopicOpen},
		{name: "unknown topic state", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}, areaPolicy: validArea, topicState: TopicState("future")},
		{name: "unknown posting mode", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}, areaPolicy: AreaPolicy{Visibility: VisibilityPublic, PostingMode: PostingMode("future")}, topicState: TopicOpen},
		{name: "malformed group policy", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}, areaPolicy: AreaPolicy{Visibility: VisibilityGroups, PostingMode: PostingNormal, GroupIDs: []int64{0}}, topicState: TopicOpen},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if CanReply(test.actor, test.areaPolicy, test.topicState) {
				t.Fatalf("CanReply(%+v, %+v, %q) admitted denied input", test.actor, test.areaPolicy, test.topicState)
			}
		})
	}
}
