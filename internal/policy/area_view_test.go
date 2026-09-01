package policy

import "testing"

func TestCanViewAreaCoversCompleteRoleVisibilityMatrix(t *testing.T) {
	t.Parallel()

	visitor := AccessContext{}
	member := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, GroupIDs: []int64{7, 9}}
	moderator := AccessContext{Authenticated: true, UserID: 12, Role: RoleModerator}
	administrator := AccessContext{Authenticated: true, UserID: 13, Role: RoleAdministrator}
	for _, test := range []struct {
		name   string
		actor  AccessContext
		policy AreaPolicy
		want   bool
	}{
		{name: "visitor public", actor: visitor, policy: viewPolicy(VisibilityPublic), want: true},
		{name: "visitor authenticated", actor: visitor, policy: viewPolicy(VisibilityAuthenticated)},
		{name: "visitor groups", actor: visitor, policy: viewPolicy(VisibilityGroups, 7)},
		{name: "member public", actor: member, policy: viewPolicy(VisibilityPublic), want: true},
		{name: "member authenticated", actor: member, policy: viewPolicy(VisibilityAuthenticated), want: true},
		{name: "member matching first group", actor: member, policy: viewPolicy(VisibilityGroups, 7, 21), want: true},
		{name: "member matching later group", actor: member, policy: viewPolicy(VisibilityGroups, 21, 9), want: true},
		{name: "member nonmatching groups", actor: member, policy: viewPolicy(VisibilityGroups, 20, 21)},
		{name: "member empty area groups", actor: member, policy: viewPolicy(VisibilityGroups)},
		{name: "moderator public", actor: moderator, policy: viewPolicy(VisibilityPublic), want: true},
		{name: "moderator authenticated", actor: moderator, policy: viewPolicy(VisibilityAuthenticated), want: true},
		{name: "moderator groups", actor: moderator, policy: viewPolicy(VisibilityGroups), want: true},
		{name: "administrator public", actor: administrator, policy: viewPolicy(VisibilityPublic), want: true},
		{name: "administrator authenticated", actor: administrator, policy: viewPolicy(VisibilityAuthenticated), want: true},
		{name: "administrator groups", actor: administrator, policy: viewPolicy(VisibilityGroups), want: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := CanViewArea(test.actor, test.policy); got != test.want {
				t.Fatalf("CanViewArea(%+v, %+v) = %t, want %t", test.actor, test.policy, got, test.want)
			}
		})
	}
}

func TestCanViewAreaFailsClosedOnMalformedAuthorityOrPolicy(t *testing.T) {
	t.Parallel()

	validMember := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, GroupIDs: []int64{7}}
	for _, test := range []struct {
		name   string
		actor  AccessContext
		policy AreaPolicy
	}{
		{name: "unknown visibility", actor: validMember, policy: viewPolicy(Visibility("future"))},
		{name: "unknown posting mode", actor: validMember, policy: AreaPolicy{Visibility: VisibilityPublic, PostingMode: PostingMode("future")}},
		{name: "anonymous user ID", actor: AccessContext{UserID: 11}, policy: viewPolicy(VisibilityPublic)},
		{name: "anonymous role", actor: AccessContext{Role: RoleMember}, policy: viewPolicy(VisibilityPublic)},
		{name: "anonymous groups", actor: AccessContext{GroupIDs: []int64{7}}, policy: viewPolicy(VisibilityPublic)},
		{name: "authenticated missing user", actor: AccessContext{Authenticated: true, Role: RoleMember}, policy: viewPolicy(VisibilityPublic)},
		{name: "authenticated unknown role", actor: AccessContext{Authenticated: true, UserID: 11, Role: Role(99)}, policy: viewPolicy(VisibilityPublic)},
		{name: "actor zero group", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, GroupIDs: []int64{0}}, policy: viewPolicy(VisibilityPublic)},
		{name: "actor negative group", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, GroupIDs: []int64{-1}}, policy: viewPolicy(VisibilityPublic)},
		{name: "public policy with groups", actor: validMember, policy: viewPolicy(VisibilityPublic, 7)},
		{name: "authenticated policy with groups", actor: validMember, policy: viewPolicy(VisibilityAuthenticated, 7)},
		{name: "area zero group", actor: validMember, policy: viewPolicy(VisibilityGroups, 0)},
		{name: "area negative group", actor: validMember, policy: viewPolicy(VisibilityGroups, -1)},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if CanViewArea(test.actor, test.policy) {
				t.Fatalf("CanViewArea(%+v, %+v) admitted malformed state", test.actor, test.policy)
			}
		})
	}
}

func TestCanViewAreaTreatsSuspensionAsPublishingStateNotVisibility(t *testing.T) {
	t.Parallel()

	actor := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, Suspended: true}
	if !CanViewArea(actor, viewPolicy(VisibilityAuthenticated)) {
		t.Fatal("CanViewArea() denied a suspended member's otherwise visible read")
	}
}

func viewPolicy(visibility Visibility, groupIDs ...int64) AreaPolicy {
	return AreaPolicy{Visibility: visibility, PostingMode: PostingNormal, GroupIDs: groupIDs}
}
