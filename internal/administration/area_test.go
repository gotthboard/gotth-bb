package administration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type areaAdministrationTestQuerier struct {
	areas     []db.ListAreasForAdministrationRow
	groups    []db.ListForumGroupsForAreaAdministrationRow
	areasErr  error
	groupsErr error
}

func (querier areaAdministrationTestQuerier) ListAreasForAdministration(context.Context) ([]db.ListAreasForAdministrationRow, error) {
	return querier.areas, querier.areasErr
}

func (querier areaAdministrationTestQuerier) ListForumGroupsForAreaAdministration(context.Context) ([]db.ListForumGroupsForAreaAdministrationRow, error) {
	return querier.groups, querier.groupsErr
}

type panicAreaBeginner struct{}

func (panicAreaBeginner) Begin(context.Context) (pgx.Tx, error) { panic("transaction started") }

func TestLoadAreaManagementRequiresAdministratorAndValidRows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	admin := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	querier := areaAdministrationTestQuerier{
		areas:  []db.ListAreasForAdministrationRow{{ID: 3, Slug: "general", Name: "General", Description: "Talk here", DisplayOrder: 1, Visibility: "groups", PostingMode: "normal", GroupIds: []int64{2}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}}},
		groups: []db.ListForumGroupsForAreaAdministrationRow{{ID: 2, Name: "Members"}},
	}
	page, err := LoadAreaManagement(context.Background(), querier, admin)
	if err != nil || len(page.Areas) != 1 || page.Areas[0].UpdatedAt != now || len(page.Groups) != 1 {
		t.Fatalf("LoadAreaManagement() = (%+v, %v)", page, err)
	}
	querier.areas[0].GroupIds[0] = 99
	if page.Areas[0].GroupIDs[0] != 2 {
		t.Fatal("LoadAreaManagement() retained database slice ownership")
	}
	if denied, deniedErr := LoadAreaManagement(context.Background(), querier, policy.AccessContext{Authenticated: true, UserID: 8, Role: policy.RoleModerator}); len(denied.Areas) != 0 || len(denied.Groups) != 0 || !errors.Is(deniedErr, ErrAreaAdministrationDenied) {
		t.Fatalf("moderator load = (%+v, %v)", denied, deniedErr)
	}
	for _, invalid := range []areaAdministrationTestQuerier{
		{areas: []db.ListAreasForAdministrationRow{{ID: 1, Slug: "Bad", Name: "Bad", Visibility: "public", PostingMode: "normal", UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}}}},
		{groups: []db.ListForumGroupsForAreaAdministrationRow{{ID: 0, Name: "Bad"}}},
		{areasErr: errors.New("secret")},
		{groupsErr: errors.New("secret")},
	} {
		if _, loadErr := LoadAreaManagement(context.Background(), invalid, admin); loadErr == nil {
			t.Fatal("LoadAreaManagement() accepted invalid or failed data")
		}
	}
}

func TestAreaMutationsRejectInvalidBoundariesBeforeDatabaseWork(t *testing.T) {
	t.Parallel()
	admin := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	valid := AreaInput{Slug: "general", Name: "General", DisplayOrder: 0, Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, Reason: "Initial area"}
	for _, test := range []struct {
		name  string
		actor policy.AccessContext
		input AreaInput
		id    pgtype.UUID
		clock func() time.Time
		want  error
	}{
		{name: "member denied", actor: policy.AccessContext{Authenticated: true, UserID: 8, Role: policy.RoleMember}, input: valid, id: requestID, clock: func() time.Time { return now }, want: ErrAreaAdministrationDenied},
		{name: "bad slug", actor: admin, input: func() AreaInput { value := valid; value.Slug = "Bad"; return value }(), id: requestID, clock: func() time.Time { return now }, want: ErrAreaAdministrationInput},
		{name: "blank reason", actor: admin, input: func() AreaInput { value := valid; value.Reason = ""; return value }(), id: requestID, clock: func() time.Time { return now }, want: ErrAreaAdministrationInput},
		{name: "group required", actor: admin, input: func() AreaInput { value := valid; value.Visibility = policy.VisibilityGroups; return value }(), id: requestID, clock: func() time.Time { return now }, want: ErrAreaAdministrationInput},
		{name: "duplicate group", actor: admin, input: func() AreaInput {
			value := valid
			value.Visibility = policy.VisibilityGroups
			value.GroupIDs = []int64{2, 2}
			return value
		}(), id: requestID, clock: func() time.Time { return now }, want: ErrAreaAdministrationInput},
		{name: "missing request", actor: admin, input: valid, clock: func() time.Time { return now }},
		{name: "zero clock", actor: admin, input: valid, id: requestID, clock: func() time.Time { return time.Time{} }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := CreateArea(context.Background(), panicAreaBeginner{}, test.clock, test.actor, test.input, test.id)
			if result != (AreaMutationResult{}) || err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("CreateArea() = (%+v, %v), want %v", result, err, test.want)
			}
		})
	}
	update := valid
	update.Revision = now
	if result, err := UpdateArea(context.Background(), panicAreaBeginner{}, func() time.Time { return now }, admin, 0, update, requestID); result != (AreaMutationResult{}) || !errors.Is(err, ErrAreaAdministrationInput) {
		t.Fatalf("UpdateArea(invalid target) = (%+v, %v)", result, err)
	}
	update.Revision = time.Time{}
	if result, err := UpdateArea(context.Background(), panicAreaBeginner{}, func() time.Time { return now }, admin, 1, update, requestID); result != (AreaMutationResult{}) || !errors.Is(err, ErrAreaAdministrationInput) {
		t.Fatalf("UpdateArea(missing revision) = (%+v, %v)", result, err)
	}
}
