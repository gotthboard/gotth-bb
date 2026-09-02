package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestListVisibleAreaSummariesDerivesAccessAndConvertsCompleteRows(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, time.September, 2, 20, 0, 0, 0, time.UTC)
	row := validVisibleAreaSummaryRow(created)
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
			querier := &visibleAreaSummaryTestQuerier{rows: []db.ListVisibleAreaSummariesRow{row}}
			got, err := ListVisibleAreaSummaries(context.Background(), querier, test.actor)
			if err != nil || len(got) != 1 || querier.calls != 1 {
				t.Fatalf("ListVisibleAreaSummaries() = (%+v, %v, calls %d)", got, err, querier.calls)
			}
			if got[0].Area.ID != 7 || got[0].Area.Slug != "general" || got[0].TopicCount != 2 || got[0].PostCount != 5 || got[0].LatestPost == nil ||
				got[0].LatestPost.TopicID != 41 || got[0].LatestPost.TopicTitle != "Current topic" || got[0].LatestPost.PostID != 91 ||
				got[0].LatestPost.PostNumber != 4 || got[0].LatestPost.TreeOrdinal != 3 || got[0].LatestPost.Author != "Alice" || !got[0].LatestPost.CreatedAt.Equal(created.Add(time.Hour)) {
				t.Fatalf("summary = %+v", got[0])
			}
			parameters := querier.parameters
			if parameters.IsStaff != test.wantStaff || parameters.IsMember != test.wantMember || !equalGroupIDs(parameters.GroupIds, test.wantGroups) {
				t.Fatalf("parameters = %+v, want staff=%t member=%t groups=%v", parameters, test.wantStaff, test.wantMember, test.wantGroups)
			}
		})
	}
}

func TestListVisibleAreaSummariesAcceptsAnEmptyArea(t *testing.T) {
	t.Parallel()

	row := validVisibleAreaSummaryRow(time.Date(2026, time.September, 2, 20, 0, 0, 0, time.UTC))
	row.TopicCount = 0
	row.PostCount = 0
	row.LatestTopicID = pgtype.Int8{}
	row.LatestTopicTitle = pgtype.Text{}
	row.LatestPostID = pgtype.Int8{}
	row.LatestPostNumber = pgtype.Int4{}
	row.LatestPostOrdinal = pgtype.Int8{}
	row.LatestPostAuthor = pgtype.Text{}
	row.LatestPostCreatedAt = pgtype.Timestamptz{}
	querier := &visibleAreaSummaryTestQuerier{rows: []db.ListVisibleAreaSummariesRow{row}}
	got, err := ListVisibleAreaSummaries(context.Background(), querier, policy.AccessContext{})
	if err != nil || len(got) != 1 || got[0].LatestPost != nil || got[0].TopicCount != 0 || got[0].PostCount != 0 {
		t.Fatalf("ListVisibleAreaSummaries(empty) = (%+v, %v)", got, err)
	}
}

func TestListVisibleAreaSummariesRejectsMalformedRowsWithoutPartialResults(t *testing.T) {
	t.Parallel()

	valid := validVisibleAreaSummaryRow(time.Date(2026, time.September, 2, 20, 0, 0, 0, time.UTC))
	cases := []struct {
		name   string
		change func(*db.ListVisibleAreaSummariesRow)
	}{
		{name: "area ID", change: func(row *db.ListVisibleAreaSummariesRow) { row.ID = 0 }},
		{name: "slug", change: func(row *db.ListVisibleAreaSummariesRow) { row.Slug = "Bad" }},
		{name: "name", change: func(row *db.ListVisibleAreaSummariesRow) { row.Name = "" }},
		{name: "display order", change: func(row *db.ListVisibleAreaSummariesRow) { row.DisplayOrder = -1 }},
		{name: "creator", change: func(row *db.ListVisibleAreaSummariesRow) { row.CreatedBy = 0 }},
		{name: "updater", change: func(row *db.ListVisibleAreaSummariesRow) { row.UpdatedBy = 0 }},
		{name: "created time", change: func(row *db.ListVisibleAreaSummariesRow) { row.CreatedAt = pgtype.Timestamptz{} }},
		{name: "updated time", change: func(row *db.ListVisibleAreaSummariesRow) { row.UpdatedAt.Time = row.CreatedAt.Time.Add(-time.Second) }},
		{name: "visibility", change: func(row *db.ListVisibleAreaSummariesRow) { row.Visibility = "unknown" }},
		{name: "posting mode", change: func(row *db.ListVisibleAreaSummariesRow) { row.PostingMode = "unknown" }},
		{name: "topic count", change: func(row *db.ListVisibleAreaSummariesRow) { row.TopicCount = -1 }},
		{name: "post count", change: func(row *db.ListVisibleAreaSummariesRow) { row.PostCount = -1 }},
		{name: "posts without topics", change: func(row *db.ListVisibleAreaSummariesRow) { row.TopicCount = 0 }},
		{name: "latest post with zero count", change: func(row *db.ListVisibleAreaSummariesRow) { row.PostCount = 0 }},
		{name: "partial latest", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestPostAuthor = pgtype.Text{} }},
		{name: "latest topic ID", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestTopicID.Int64 = 0 }},
		{name: "latest topic title", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestTopicTitle.String = "" }},
		{name: "latest post ID", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestPostID.Int64 = 0 }},
		{name: "latest post number", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestPostNumber.Int32 = 0 }},
		{name: "latest post ordinal", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestPostOrdinal.Int64 = 0 }},
		{name: "latest author", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestPostAuthor.String = "" }},
		{name: "latest time", change: func(row *db.ListVisibleAreaSummariesRow) { row.LatestPostCreatedAt.InfinityModifier = pgtype.Infinity }},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			malformed := valid
			test.change(&malformed)
			querier := &visibleAreaSummaryTestQuerier{rows: []db.ListVisibleAreaSummariesRow{valid, malformed}}
			got, err := ListVisibleAreaSummaries(context.Background(), querier, policy.AccessContext{})
			if err == nil || got != nil || querier.calls != 1 {
				t.Fatalf("ListVisibleAreaSummaries(malformed) = (%+v, %v, calls %d)", got, err, querier.calls)
			}
		})
	}
}

func TestListVisibleAreaSummariesRejectsDependenciesAuthorityAndQueryFailure(t *testing.T) {
	t.Parallel()

	validActor := policy.AccessContext{}
	if got, err := ListVisibleAreaSummaries(nil, panicVisibleAreaSummaryQuerier{}, validActor); err == nil || got != nil {
		t.Fatalf("nil context = (%+v, %v)", got, err)
	}
	if got, err := ListVisibleAreaSummaries(context.Background(), nil, validActor); err == nil || got != nil {
		t.Fatalf("nil querier = (%+v, %v)", got, err)
	}
	if got, err := ListVisibleAreaSummaries(context.Background(), panicVisibleAreaSummaryQuerier{}, policy.AccessContext{UserID: 1}); err == nil || got != nil {
		t.Fatalf("invalid actor = (%+v, %v)", got, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := ListVisibleAreaSummaries(canceled, panicVisibleAreaSummaryQuerier{}, validActor); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled context = (%+v, %v)", got, err)
	}
	cause := errors.New("summary query failed")
	querier := &visibleAreaSummaryTestQuerier{err: cause}
	if got, err := ListVisibleAreaSummaries(context.Background(), querier, validActor); !errors.Is(err, cause) || got != nil || querier.calls != 1 {
		t.Fatalf("query failure = (%+v, %v, calls %d)", got, err, querier.calls)
	}
}

func validVisibleAreaSummaryRow(created time.Time) db.ListVisibleAreaSummariesRow {
	return db.ListVisibleAreaSummariesRow{
		ID: 7, Slug: "general", Name: "General", Description: "Discussion", DisplayOrder: 1,
		Visibility: "public", PostingMode: "normal", CreatedBy: 11, UpdatedBy: 12,
		CreatedAt: pgtype.Timestamptz{Time: created, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: created, Valid: true},
		TopicCount: 2, PostCount: 5,
		LatestTopicID: pgtype.Int8{Int64: 41, Valid: true}, LatestTopicTitle: pgtype.Text{String: "Current topic", Valid: true},
		LatestPostID: pgtype.Int8{Int64: 91, Valid: true}, LatestPostNumber: pgtype.Int4{Int32: 4, Valid: true},
		LatestPostOrdinal: pgtype.Int8{Int64: 3, Valid: true},
		LatestPostAuthor:  pgtype.Text{String: "Alice", Valid: true}, LatestPostCreatedAt: pgtype.Timestamptz{Time: created.Add(time.Hour), Valid: true},
	}
}

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

type visibleAreaSummaryTestQuerier struct {
	rows       []db.ListVisibleAreaSummariesRow
	err        error
	calls      int
	parameters db.ListVisibleAreaSummariesParams
}

func (querier *visibleAreaSummaryTestQuerier) ListVisibleAreaSummaries(_ context.Context, parameters db.ListVisibleAreaSummariesParams) ([]db.ListVisibleAreaSummariesRow, error) {
	querier.calls++
	querier.parameters = parameters
	return querier.rows, querier.err
}

type panicVisibleAreaSummaryQuerier struct{}

func (panicVisibleAreaSummaryQuerier) ListVisibleAreaSummaries(context.Context, db.ListVisibleAreaSummariesParams) ([]db.ListVisibleAreaSummariesRow, error) {
	panic("visible-area-summary query must not run")
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
