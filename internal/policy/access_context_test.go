package policy

import "testing"

func TestAccessContextValidAcceptsCanonicalAnonymousAndAuthenticatedStates(t *testing.T) {
	t.Parallel()

	for _, actor := range []AccessContext{
		{},
		{Authenticated: true, UserID: 1, Role: RoleMember},
		{Authenticated: true, UserID: 2, Role: RoleMember, GroupIDs: []int64{3, 5}, Suspended: true},
		{Authenticated: true, UserID: 3, Role: RoleModerator},
		{Authenticated: true, UserID: 4, Role: RoleAdministrator, GroupIDs: []int64{7}},
	} {
		actor := actor
		t.Run(actorName(actor), func(t *testing.T) {
			t.Parallel()
			if !actor.Valid() {
				t.Fatalf("AccessContext.Valid(%+v) = false", actor)
			}
		})
	}
}

func TestAccessContextValidRejectsContradictoryOrMalformedAuthority(t *testing.T) {
	t.Parallel()

	for _, actor := range []AccessContext{
		{UserID: 1},
		{Role: RoleMember},
		{GroupIDs: []int64{1}},
		{Authenticated: true, Role: RoleMember},
		{Authenticated: true, UserID: -1, Role: RoleMember},
		{Authenticated: true, UserID: 1},
		{Authenticated: true, UserID: 1, Role: Role(99)},
		{Authenticated: true, UserID: 1, Role: RoleMember, GroupIDs: []int64{0}},
		{Authenticated: true, UserID: 1, Role: RoleMember, GroupIDs: []int64{-1}},
	} {
		actor := actor
		t.Run(actorName(actor), func(t *testing.T) {
			t.Parallel()
			if actor.Valid() {
				t.Fatalf("AccessContext.Valid(%+v) = true", actor)
			}
		})
	}
}

func actorName(actor AccessContext) string {
	return string(rune('a'+actor.Role)) + "/" + boolName(actor.Authenticated) + "/" + intName(actor.UserID) + "/" + intName(int64(len(actor.GroupIDs)))
}

func boolName(value bool) string {
	if value {
		return "authenticated"
	}
	return "anonymous"
}

func intName(value int64) string {
	if value < 0 {
		return "negative"
	}
	if value == 0 {
		return "zero"
	}
	return "positive"
}
