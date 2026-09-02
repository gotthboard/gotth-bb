package policy

import (
	"testing"
	"time"
)

func TestCanCreateTopicCoversVisibilityPostingAndRoleMatrix(t *testing.T) {
	t.Parallel()

	member := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, GroupIDs: []int64{7}}
	moderator := AccessContext{Authenticated: true, UserID: 12, Role: RoleModerator}
	administrator := AccessContext{Authenticated: true, UserID: 13, Role: RoleAdministrator}
	for _, test := range []struct {
		name   string
		actor  AccessContext
		policy AreaPolicy
		want   bool
	}{
		{name: "visitor public normal", policy: topicPolicy(VisibilityPublic, PostingNormal)},
		{name: "member public normal", actor: member, policy: topicPolicy(VisibilityPublic, PostingNormal), want: true},
		{name: "member authenticated normal", actor: member, policy: topicPolicy(VisibilityAuthenticated, PostingNormal), want: true},
		{name: "member matching group normal", actor: member, policy: topicPolicy(VisibilityGroups, PostingNormal, 7), want: true},
		{name: "member nonmatching group normal", actor: member, policy: topicPolicy(VisibilityGroups, PostingNormal, 8)},
		{name: "member public read only", actor: member, policy: topicPolicy(VisibilityPublic, PostingReadOnly)},
		{name: "moderator public normal", actor: moderator, policy: topicPolicy(VisibilityPublic, PostingNormal), want: true},
		{name: "moderator restricted normal", actor: moderator, policy: topicPolicy(VisibilityGroups, PostingNormal), want: true},
		{name: "moderator read only", actor: moderator, policy: topicPolicy(VisibilityAuthenticated, PostingReadOnly), want: true},
		{name: "administrator read only", actor: administrator, policy: topicPolicy(VisibilityGroups, PostingReadOnly), want: true},
		{name: "member archived", actor: member, policy: topicPolicy(VisibilityPublic, PostingArchived)},
		{name: "moderator archived", actor: moderator, policy: topicPolicy(VisibilityPublic, PostingArchived)},
		{name: "administrator archived", actor: administrator, policy: topicPolicy(VisibilityGroups, PostingArchived)},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := CanCreateTopic(test.actor, test.policy); got != test.want {
				t.Fatalf("CanCreateTopic(%+v, %+v) = %t, want %t", test.actor, test.policy, got, test.want)
			}
		})
	}
}

func TestCanCreateTopicDeniesSuspendedMutedAndMalformedActors(t *testing.T) {
	t.Parallel()

	muteExpiry := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	for _, actor := range []AccessContext{
		{Authenticated: true, UserID: 11, Role: RoleMember, Suspended: true},
		{Authenticated: true, UserID: 11, Role: RoleMember, MutedUntil: &muteExpiry},
		{Authenticated: true, UserID: 12, Role: RoleModerator, Suspended: true},
		{Authenticated: true, UserID: 13, Role: RoleAdministrator, MutedUntil: &muteExpiry},
		{Authenticated: true, UserID: 11, Role: Role(99)},
		{Authenticated: true, UserID: 11, Role: RoleMember, GroupIDs: []int64{0}},
	} {
		actor := actor
		t.Run(actorName(actor), func(t *testing.T) {
			t.Parallel()
			if CanCreateTopic(actor, topicPolicy(VisibilityPublic, PostingNormal)) {
				t.Fatalf("CanCreateTopic(%+v) admitted denied actor", actor)
			}
		})
	}
}

func TestCanCreateTopicFailsClosedOnMalformedAreaPolicy(t *testing.T) {
	t.Parallel()

	member := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}
	for _, areaPolicy := range []AreaPolicy{
		{Visibility: Visibility("future"), PostingMode: PostingNormal},
		{Visibility: VisibilityPublic, PostingMode: PostingMode("future")},
		{Visibility: VisibilityPublic, PostingMode: PostingNormal, GroupIDs: []int64{7}},
		{Visibility: VisibilityGroups, PostingMode: PostingNormal, GroupIDs: []int64{0}},
	} {
		areaPolicy := areaPolicy
		t.Run(string(areaPolicy.Visibility)+"/"+string(areaPolicy.PostingMode), func(t *testing.T) {
			t.Parallel()
			if CanCreateTopic(member, areaPolicy) {
				t.Fatalf("CanCreateTopic(%+v) admitted malformed policy", areaPolicy)
			}
		})
	}
}

func topicPolicy(visibility Visibility, postingMode PostingMode, groupIDs ...int64) AreaPolicy {
	return AreaPolicy{Visibility: visibility, PostingMode: postingMode, GroupIDs: groupIDs}
}
