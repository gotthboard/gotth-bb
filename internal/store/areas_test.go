package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
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

func TestGetVisibleAreaBySlugDerivesOnlyVerifiedAccessFacts(t *testing.T) {
	t.Parallel()

	wantArea := db.Area{ID: 7, Slug: "members"}
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
			querier := &visibleAreaBySlugTestQuerier{area: wantArea}
			got, err := GetVisibleAreaBySlug(context.Background(), querier, "members", test.actor)
			if err != nil || got != wantArea || querier.calls != 1 {
				t.Fatalf("GetVisibleAreaBySlug() = (%+v, %v, calls %d)", got, err, querier.calls)
			}
			parameters := querier.parameters
			if parameters.Slug != "members" || parameters.IsStaff != test.wantStaff ||
				parameters.IsMember != test.wantMember || !equalGroupIDs(parameters.GroupIds, test.wantGroups) {
				t.Fatalf("parameters = %+v, want slug=members staff=%t member=%t groups=%v", parameters, test.wantStaff, test.wantMember, test.wantGroups)
			}
		})
	}
}

func TestGetVisibleAreaBySlugRejectsInvalidAuthorityBeforeQuery(t *testing.T) {
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
			if area, err := GetVisibleAreaBySlug(context.Background(), panicVisibleAreaBySlugQuerier{}, "public", actor); err == nil || area != (db.Area{}) {
				t.Fatalf("GetVisibleAreaBySlug() = (%+v, %v), want zero/error", area, err)
			}
		})
	}
}

func TestGetVisibleAreaBySlugTreatsInvalidSlugAsMissingBeforeQuery(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{
		"",
		strings.Repeat("a", 81),
		"Uppercase",
		"with/slash",
		"-leading",
		"trailing-",
		"double--hyphen",
		"nul\x00byte",
	} {
		slug := slug
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()
			area, err := GetVisibleAreaBySlug(context.Background(), panicVisibleAreaBySlugQuerier{}, slug, policy.AccessContext{})
			if !errors.Is(err, pgx.ErrNoRows) || area != (db.Area{}) {
				t.Fatalf("GetVisibleAreaBySlug(%q) = (%+v, %v), want zero/pgx.ErrNoRows", slug, area, err)
			}
		})
	}
}

func TestGetVisibleAreaBySlugAcceptsSchemaMaximum(t *testing.T) {
	t.Parallel()

	slug := strings.Repeat("a", 80)
	querier := &visibleAreaBySlugTestQuerier{area: db.Area{ID: 9, Slug: slug}}
	area, err := GetVisibleAreaBySlug(context.Background(), querier, slug, policy.AccessContext{})
	if err != nil || area.Slug != slug || querier.calls != 1 || querier.parameters.Slug != slug {
		t.Fatalf("GetVisibleAreaBySlug(maximum) = (%+v, %v, calls %d, parameters %+v)", area, err, querier.calls, querier.parameters)
	}
}

func TestGetVisibleAreaBySlugRejectsDependenciesAndPreservesQueryFailure(t *testing.T) {
	t.Parallel()

	validActor := policy.AccessContext{}
	if area, err := GetVisibleAreaBySlug(nil, panicVisibleAreaBySlugQuerier{}, "public", validActor); err == nil || area != (db.Area{}) {
		t.Fatalf("nil context = (%+v, %v)", area, err)
	}
	if area, err := GetVisibleAreaBySlug(context.Background(), nil, "public", validActor); err == nil || area != (db.Area{}) {
		t.Fatalf("nil querier = (%+v, %v)", area, err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if area, err := GetVisibleAreaBySlug(canceledContext, panicVisibleAreaBySlugQuerier{}, "public", validActor); !errors.Is(err, context.Canceled) || area != (db.Area{}) {
		t.Fatalf("canceled context = (%+v, %v)", area, err)
	}
	cause := errors.New("query failed")
	querier := &visibleAreaBySlugTestQuerier{area: db.Area{ID: 99}, err: cause}
	if area, err := GetVisibleAreaBySlug(context.Background(), querier, "public", validActor); !errors.Is(err, cause) || area != (db.Area{}) || querier.calls != 1 {
		t.Fatalf("query failure = (%+v, %v, calls %d)", area, err, querier.calls)
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

type visibleAreaBySlugTestQuerier struct {
	area       db.Area
	err        error
	calls      int
	parameters db.GetVisibleAreaBySlugParams
}

func (querier *visibleAreaBySlugTestQuerier) GetVisibleAreaBySlug(_ context.Context, parameters db.GetVisibleAreaBySlugParams) (db.Area, error) {
	querier.calls++
	querier.parameters = parameters
	return querier.area, querier.err
}

type panicVisibleAreaBySlugQuerier struct{}

func (panicVisibleAreaBySlugQuerier) GetVisibleAreaBySlug(context.Context, db.GetVisibleAreaBySlugParams) (db.Area, error) {
	panic("visible-area-by-slug query must not run")
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
