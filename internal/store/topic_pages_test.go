package store

import (
	"context"
	"errors"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

func TestGetVisibleAreaTopicPageDerivesAuthorityAndPagination(t *testing.T) {
	t.Parallel()

	wantArea := db.Area{ID: 7, Slug: "members", Name: "Members"}
	wantTopics := []db.ListVisibleTopicsByAreaSlugRow{
		{TopicID: 41, Title: "First", TotalVisibleTopics: 27},
		{TopicID: 40, Title: "Second", TotalVisibleTopics: 27},
	}
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
			querier := &visibleTopicPageTestQuerier{area: wantArea, topics: wantTopics}
			got, err := GetVisibleAreaTopicPage(context.Background(), querier, "members", 2, test.actor)
			if err != nil || got.Area != wantArea || len(got.Topics) != 2 || got.Topics[0] != wantTopics[0] || got.Topics[1] != wantTopics[1] ||
				got.Number != 2 || got.TotalTopics != 27 || got.TotalPages != 2 || querier.areaCalls != 1 || querier.topicCalls != 1 {
				t.Fatalf("GetVisibleAreaTopicPage() = (%+v, %v, area calls %d, topic calls %d)", got, err, querier.areaCalls, querier.topicCalls)
			}
			for _, facts := range []struct {
				staff  bool
				member bool
				groups []int64
			}{
				{staff: querier.areaParameters.IsStaff, member: querier.areaParameters.IsMember, groups: querier.areaParameters.GroupIds},
				{staff: querier.topicParameters.IsStaff, member: querier.topicParameters.IsMember, groups: querier.topicParameters.GroupIds},
			} {
				if facts.staff != test.wantStaff || facts.member != test.wantMember || !equalGroupIDs(facts.groups, test.wantGroups) {
					t.Fatalf("access facts = %+v, want staff=%t member=%t groups=%v", facts, test.wantStaff, test.wantMember, test.wantGroups)
				}
			}
			if querier.areaParameters.Slug != "members" || querier.topicParameters.AreaSlug != "members" ||
				querier.topicParameters.PageLimit != TopicPageSize || querier.topicParameters.PageOffset != TopicPageSize {
				t.Fatalf("query parameters = (area %+v, topics %+v)", querier.areaParameters, querier.topicParameters)
			}
		})
	}
}

func TestGetVisibleAreaTopicPageAcceptsEmptyFirstPage(t *testing.T) {
	t.Parallel()

	querier := &visibleTopicPageTestQuerier{area: db.Area{ID: 3, Slug: "empty"}}
	got, err := GetVisibleAreaTopicPage(context.Background(), querier, "empty", 1, policy.AccessContext{})
	if err != nil || got.Area.ID != 3 || len(got.Topics) != 0 || got.Number != 1 || got.TotalTopics != 0 || got.TotalPages != 0 {
		t.Fatalf("GetVisibleAreaTopicPage(empty) = (%+v, %v)", got, err)
	}
}

func TestGetVisibleAreaTopicPageAcceptsMaximumPage(t *testing.T) {
	t.Parallel()

	offset := (MaximumTopicPage - 1) * TopicPageSize
	total := int64(offset) + 1
	querier := &visibleTopicPageTestQuerier{
		area:   db.Area{ID: 3, Slug: "public"},
		topics: []db.ListVisibleTopicsByAreaSlugRow{{TopicID: 99, TotalVisibleTopics: total}},
	}
	got, err := GetVisibleAreaTopicPage(context.Background(), querier, "public", MaximumTopicPage, policy.AccessContext{})
	if err != nil || got.Number != MaximumTopicPage || got.TotalTopics != total || got.TotalPages != int64(MaximumTopicPage) ||
		querier.topicParameters.PageOffset != offset || querier.topicParameters.PageLimit != TopicPageSize {
		t.Fatalf("maximum page = (%+v, %v, parameters %+v)", got, err, querier.topicParameters)
	}
}

func TestGetVisibleAreaTopicPageTreatsInvalidAndEmptyLaterPagesAsMissing(t *testing.T) {
	t.Parallel()

	for _, page := range []int32{0, -1, MaximumTopicPage + 1} {
		page := page
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()
			got, err := GetVisibleAreaTopicPage(context.Background(), panicVisibleTopicPageQuerier{}, "public", page, policy.AccessContext{})
			if !errors.Is(err, pgx.ErrNoRows) || !topicPageIsZero(got) {
				t.Fatalf("page %d = (%+v, %v), want zero/pgx.ErrNoRows", page, got, err)
			}
		})
	}
	querier := &visibleTopicPageTestQuerier{area: db.Area{ID: 3, Slug: "empty"}}
	got, err := GetVisibleAreaTopicPage(context.Background(), querier, "empty", 2, policy.AccessContext{})
	if !errors.Is(err, pgx.ErrNoRows) || !topicPageIsZero(got) || querier.areaCalls != 1 || querier.topicCalls != 1 {
		t.Fatalf("empty second page = (%+v, %v, area calls %d, topic calls %d)", got, err, querier.areaCalls, querier.topicCalls)
	}
}

func TestGetVisibleAreaTopicPageRejectsDependenciesAndFailures(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{}
	if got, err := GetVisibleAreaTopicPage(nil, panicVisibleTopicPageQuerier{}, "public", 1, actor); err == nil || !topicPageIsZero(got) {
		t.Fatalf("nil context = (%+v, %v)", got, err)
	}
	if got, err := GetVisibleAreaTopicPage(context.Background(), nil, "public", 1, actor); err == nil || !topicPageIsZero(got) {
		t.Fatalf("nil querier = (%+v, %v)", got, err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := GetVisibleAreaTopicPage(canceledContext, panicVisibleTopicPageQuerier{}, "public", 1, actor); !errors.Is(err, context.Canceled) || !topicPageIsZero(got) {
		t.Fatalf("canceled context = (%+v, %v)", got, err)
	}
	areaCause := errors.New("area failed")
	areaFailure := &visibleTopicPageTestQuerier{areaErr: areaCause}
	if got, err := GetVisibleAreaTopicPage(context.Background(), areaFailure, "public", 1, actor); !errors.Is(err, areaCause) || !topicPageIsZero(got) || areaFailure.topicCalls != 0 {
		t.Fatalf("area failure = (%+v, %v, topic calls %d)", got, err, areaFailure.topicCalls)
	}
	topicCause := errors.New("topics failed")
	topicFailure := &visibleTopicPageTestQuerier{area: db.Area{ID: 1, Slug: "public"}, topics: []db.ListVisibleTopicsByAreaSlugRow{{TopicID: 9, TotalVisibleTopics: 1}}, topicErr: topicCause}
	if got, err := GetVisibleAreaTopicPage(context.Background(), topicFailure, "public", 1, actor); !errors.Is(err, topicCause) || !topicPageIsZero(got) || topicFailure.topicCalls != 1 {
		t.Fatalf("topic failure = (%+v, %v, calls %d)", got, err, topicFailure.topicCalls)
	}
}

func TestGetVisibleAreaTopicPageRejectsInconsistentQueryMetadata(t *testing.T) {
	t.Parallel()

	rows := func(count int, total int64) []db.ListVisibleTopicsByAreaSlugRow {
		result := make([]db.ListVisibleTopicsByAreaSlugRow, count)
		for index := range result {
			result[index] = db.ListVisibleTopicsByAreaSlugRow{TopicID: int64(index + 1), TotalVisibleTopics: total}
		}
		return result
	}
	for _, test := range []struct {
		page   int32
		topics []db.ListVisibleTopicsByAreaSlugRow
	}{
		{page: 1, topics: rows(int(TopicPageSize)+1, int64(TopicPageSize)+1)},
		{page: 1, topics: rows(1, 0)},
		{page: 1, topics: []db.ListVisibleTopicsByAreaSlugRow{{TopicID: 1, TotalVisibleTopics: 2}, {TopicID: 2, TotalVisibleTopics: 3}}},
		{page: 1, topics: rows(2, int64(TopicPageSize)+1)},
	} {
		test := test
		t.Run("inconsistent", func(t *testing.T) {
			t.Parallel()
			querier := &visibleTopicPageTestQuerier{area: db.Area{ID: 1, Slug: "public"}, topics: test.topics}
			got, err := GetVisibleAreaTopicPage(context.Background(), querier, "public", test.page, policy.AccessContext{})
			if err == nil || errors.Is(err, pgx.ErrNoRows) || !topicPageIsZero(got) {
				t.Fatalf("inconsistent rows = (%+v, %v), want zero/internal error", got, err)
			}
		})
	}
}

type visibleTopicPageTestQuerier struct {
	area            db.Area
	areaErr         error
	topics          []db.ListVisibleTopicsByAreaSlugRow
	topicErr        error
	areaCalls       int
	topicCalls      int
	areaParameters  db.GetVisibleAreaBySlugParams
	topicParameters db.ListVisibleTopicsByAreaSlugParams
}

func topicPageIsZero(page VisibleAreaTopicPage) bool {
	return page.Area == (db.Area{}) && page.Topics == nil && page.Number == 0 && page.TotalTopics == 0 && page.TotalPages == 0
}

func (querier *visibleTopicPageTestQuerier) GetVisibleAreaBySlug(_ context.Context, parameters db.GetVisibleAreaBySlugParams) (db.Area, error) {
	querier.areaCalls++
	querier.areaParameters = parameters
	return querier.area, querier.areaErr
}

func (querier *visibleTopicPageTestQuerier) ListVisibleTopicsByAreaSlug(_ context.Context, parameters db.ListVisibleTopicsByAreaSlugParams) ([]db.ListVisibleTopicsByAreaSlugRow, error) {
	querier.topicCalls++
	querier.topicParameters = parameters
	return querier.topics, querier.topicErr
}

type panicVisibleTopicPageQuerier struct{}

func (panicVisibleTopicPageQuerier) GetVisibleAreaBySlug(context.Context, db.GetVisibleAreaBySlugParams) (db.Area, error) {
	panic("area query must not run")
}

func (panicVisibleTopicPageQuerier) ListVisibleTopicsByAreaSlug(context.Context, db.ListVisibleTopicsByAreaSlugParams) ([]db.ListVisibleTopicsByAreaSlugRow, error) {
	panic("topic query must not run")
}
