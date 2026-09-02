package policy

import (
	"testing"
	"time"
)

func TestCanEditPostAllowsOnlyActiveOwnerOfVisiblePost(t *testing.T) {
	t.Parallel()

	owner := AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}
	if !CanEditPost(owner, PostOwnership{AuthorID: 11}, PostVisible) {
		t.Fatal("active owner could not edit visible post")
	}
	for _, role := range []Role{RoleModerator, RoleAdministrator} {
		actor := AccessContext{Authenticated: true, UserID: 11, Role: role}
		if !CanEditPost(actor, PostOwnership{AuthorID: 11}, PostVisible) {
			t.Fatalf("active owner role %v could not edit own visible post", role)
		}
	}
}

func TestCanEditPostFailsClosed(t *testing.T) {
	t.Parallel()

	muteExpiry := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		actor     AccessContext
		ownership PostOwnership
		state     PostState
	}{
		{name: "anonymous", ownership: PostOwnership{AuthorID: 11}, state: PostVisible},
		{name: "foreign member", actor: AccessContext{Authenticated: true, UserID: 12, Role: RoleMember}, ownership: PostOwnership{AuthorID: 11}, state: PostVisible},
		{name: "foreign moderator", actor: AccessContext{Authenticated: true, UserID: 12, Role: RoleModerator}, ownership: PostOwnership{AuthorID: 11}, state: PostVisible},
		{name: "suspended", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, Suspended: true}, ownership: PostOwnership{AuthorID: 11}, state: PostVisible},
		{name: "muted", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember, MutedUntil: &muteExpiry}, ownership: PostOwnership{AuthorID: 11}, state: PostVisible},
		{name: "deleted", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}, ownership: PostOwnership{AuthorID: 11}, state: PostDeleted},
		{name: "unknown state", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}, ownership: PostOwnership{AuthorID: 11}, state: PostState("future")},
		{name: "invalid author", actor: AccessContext{Authenticated: true, UserID: 11, Role: RoleMember}, state: PostVisible},
		{name: "invalid actor", actor: AccessContext{Authenticated: true, UserID: 11, Role: Role(99)}, ownership: PostOwnership{AuthorID: 11}, state: PostVisible},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if CanEditPost(test.actor, test.ownership, test.state) {
				t.Fatal("edit policy admitted denied state")
			}
		})
	}
}
