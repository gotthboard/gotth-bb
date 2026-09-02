package policy

import (
	"testing"
	"time"
)

func TestCanAdministerRequiresActiveLocalAdministrator(t *testing.T) {
	t.Parallel()
	mute := time.Now().Add(time.Hour)
	for _, test := range []struct {
		name  string
		actor AccessContext
		want  bool
	}{
		{name: "administrator", actor: AccessContext{Authenticated: true, UserID: 1, Role: RoleAdministrator}, want: true},
		{name: "member", actor: AccessContext{Authenticated: true, UserID: 1, Role: RoleMember}},
		{name: "moderator", actor: AccessContext{Authenticated: true, UserID: 1, Role: RoleModerator}},
		{name: "anonymous", actor: AccessContext{}},
		{name: "suspended", actor: AccessContext{Authenticated: true, UserID: 1, Role: RoleAdministrator, Suspended: true}},
		{name: "muted", actor: AccessContext{Authenticated: true, UserID: 1, Role: RoleAdministrator, MutedUntil: &mute}},
		{name: "invalid group", actor: AccessContext{Authenticated: true, UserID: 1, Role: RoleAdministrator, GroupIDs: []int64{0}}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := CanAdminister(test.actor); got != test.want {
				t.Fatalf("CanAdminister() = %t, want %t", got, test.want)
			}
		})
	}
}
