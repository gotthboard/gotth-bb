package store

import (
	"context"
	"errors"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
)

func TestListVisibleAreasDerivesOnlyVerifiedAccessFacts(t *testing.T) {
	t.Parallel()

	wantAreas := []db.Area{{ID: 1, Slug: "public"}}
	for _, test := range []struct {
		name       string
		actor      policy.AccessContext
		wantStaff  bool
		wantMember bool
		wantGroups []int64
	}{
		{name: "visitor"},
		{name: "member", actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, GroupIDs: []int64{3, 5}}, wantMember: true, wantGroups: []int64{3, 5}},
		{name: "moderator", actor: policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleModerator, GroupIDs: []int64{7}}, wantStaff: true, wantMember: true, wantGroups: []int64{7}},
		{name: "administrator", actor: policy.AccessContext{Authenticated: true, UserID: 13, Role: policy.RoleAdministrator}, wantStaff: true, wantMember: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			querier := &visibleAreaTestQuerier{areas: wantAreas}
			got, err := ListVisibleAreas(context.Background(), querier, test.actor)
			if err != nil || len(got) != 1 || got[0] != wantAreas[0] || querier.calls != 1 {
				t.Fatalf("ListVisibleAreas() = (%+v, %v, calls %d)", got, err, querier.calls)
			}
			if querier.parameters.IsStaff != test.wantStaff || querier.parameters.IsMember != test.wantMember || !equalGroupIDs(querier.parameters.GroupIds, test.wantGroups) {
				t.Fatalf("parameters = %+v, want staff=%t member=%t groups=%v", querier.parameters, test.wantStaff, test.wantMember, test.wantGroups)
			}
		})
	}
}

func TestListVisibleAreasRejectsInvalidAuthorityBeforeQuery(t *testing.T) {
	t.Parallel()

	for _, actor := range []policy.AccessContext{
		{UserID: 1},
		{Authenticated: true, Role: policy.RoleMember},
		{Authenticated: true, UserID: 1, Role: policy.Role(99)},
		{Authenticated: true, UserID: 1, Role: policy.RoleMember, GroupIDs: []int64{0}},
	} {
		actor := actor
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()
			if areas, err := ListVisibleAreas(context.Background(), panicVisibleAreaQuerier{}, actor); err == nil || areas != nil {
				t.Fatalf("ListVisibleAreas() = (%+v, %v), want nil/error", areas, err)
			}
		})
	}
}

func TestListVisibleAreasRejectsDependenciesAndPreservesQueryFailure(t *testing.T) {
	t.Parallel()

	validActor := policy.AccessContext{}
	if areas, err := ListVisibleAreas(nil, panicVisibleAreaQuerier{}, validActor); err == nil || areas != nil {
		t.Fatalf("nil context = (%+v, %v)", areas, err)
	}
	if areas, err := ListVisibleAreas(context.Background(), nil, validActor); err == nil || areas != nil {
		t.Fatalf("nil querier = (%+v, %v)", areas, err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if areas, err := ListVisibleAreas(canceledContext, panicVisibleAreaQuerier{}, validActor); !errors.Is(err, context.Canceled) || areas != nil {
		t.Fatalf("canceled context = (%+v, %v)", areas, err)
	}
	cause := errors.New("query failed")
	querier := &visibleAreaTestQuerier{areas: []db.Area{{ID: 99}}, err: cause}
	if areas, err := ListVisibleAreas(context.Background(), querier, validActor); !errors.Is(err, cause) || areas != nil || querier.calls != 1 {
		t.Fatalf("query failure = (%+v, %v, calls %d)", areas, err, querier.calls)
	}
}

type visibleAreaTestQuerier struct {
	areas      []db.Area
	err        error
	calls      int
	parameters db.ListVisibleAreasParams
}

func (querier *visibleAreaTestQuerier) ListVisibleAreas(_ context.Context, parameters db.ListVisibleAreasParams) ([]db.Area, error) {
	querier.calls++
	querier.parameters = parameters
	return querier.areas, querier.err
}

type panicVisibleAreaQuerier struct{}

func (panicVisibleAreaQuerier) ListVisibleAreas(context.Context, db.ListVisibleAreasParams) ([]db.Area, error) {
	panic("visible-area query must not run")
}

func equalGroupIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
