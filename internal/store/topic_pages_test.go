package store

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

func TestGetVisibleAreaTopicPageDerivesAuthorityAndPagination(t *testing.T) {
	t.Parallel()

	wantArea := db.Area{ID: 7, Slug: "members", Name: "Members"}
	wantTopics := validVisibleAreaTopicRows(2, 27)
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
			if test.actor.Authenticated {
				querier.authenticatedTopics = validAuthenticatedVisibleAreaTopicRows(2, 27, ReadStateUnread)
			}
			got, err := GetVisibleAreaTopicPage(context.Background(), querier, "members", 2, test.actor)
			if err != nil || got.Area != wantArea || len(got.Topics) != 2 || got.Topics[0].TopicID != wantTopics[0].TopicID || got.Topics[1].TopicID != wantTopics[1].TopicID ||
				got.Number != 2 || got.TotalTopics != 27 || got.TotalPages != 2 || querier.areaCalls != 1 || querier.topicCalls+querier.authenticatedTopicCalls != 1 {
				t.Fatalf("GetVisibleAreaTopicPage() = (%+v, %v, area calls %d, topic calls %d/%d)", got, err, querier.areaCalls, querier.topicCalls, querier.authenticatedTopicCalls)
			}
			if querier.areaParameters.IsStaff != test.wantStaff || querier.areaParameters.IsMember != test.wantMember || !equalGroupIDs(querier.areaParameters.GroupIds, test.wantGroups) {
				t.Fatalf("area access facts = %+v", querier.areaParameters)
			}
			if test.actor.Authenticated {
				parameters := querier.authenticatedTopicParameters
				if querier.topicCalls != 0 || got.Topics[0].ReadState == nil || *got.Topics[0].ReadState != ReadStateUnread ||
					parameters.ActorUserID != test.actor.UserID || parameters.AreaSlug != "members" || parameters.IsStaff != test.wantStaff ||
					!equalGroupIDs(parameters.GroupIds, test.wantGroups) || parameters.PageLimit != TopicPageSize || parameters.PageOffset != TopicPageSize {
					t.Fatalf("authenticated topics/parameters = (%+v, %+v)", got.Topics, parameters)
				}
			} else if querier.authenticatedTopicCalls != 0 || got.Topics[0].ReadState != nil || querier.topicParameters.AreaSlug != "members" ||
				querier.topicParameters.IsStaff || querier.topicParameters.IsMember || len(querier.topicParameters.GroupIds) != 0 ||
				querier.topicParameters.PageLimit != TopicPageSize || querier.topicParameters.PageOffset != TopicPageSize {
				t.Fatalf("visitor topics/parameters = (%+v, %+v)", got.Topics, querier.topicParameters)
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
		topics: validVisibleAreaTopicRows(1, total),
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
	topicFailure := &visibleTopicPageTestQuerier{area: db.Area{ID: 1, Slug: "public"}, topics: validVisibleAreaTopicRows(1, 1), topicErr: topicCause}
	if got, err := GetVisibleAreaTopicPage(context.Background(), topicFailure, "public", 1, actor); !errors.Is(err, topicCause) || !topicPageIsZero(got) || topicFailure.topicCalls != 1 {
		t.Fatalf("topic failure = (%+v, %v, calls %d)", got, err, topicFailure.topicCalls)
	}
}

func TestGetVisibleAreaTopicPageRejectsInconsistentQueryMetadata(t *testing.T) {
	t.Parallel()

	rows := validVisibleAreaTopicRows
	for _, test := range []struct {
		page   int32
		topics []db.ListVisibleTopicsByAreaSlugRow
	}{
		{page: 1, topics: rows(int(TopicPageSize)+1, int64(TopicPageSize)+1)},
		{page: 1, topics: rows(1, 0)},
		{page: 1, topics: func() []db.ListVisibleTopicsByAreaSlugRow {
			result := rows(2, 2)
			result[1].TotalVisibleTopics = 3
			return result
		}()},
		{page: 1, topics: func() []db.ListVisibleTopicsByAreaSlugRow {
			result := rows(2, 2)
			result[1].TopicID = result[0].TopicID
			return result
		}()},
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

func TestGetVisibleAreaTopicPageReturnsClosedAuthenticatedStates(t *testing.T) {
	t.Parallel()

	rows := []db.ListAuthenticatedVisibleTopicsByAreaSlugRow{
		validAuthenticatedVisibleAreaTopicRows(1, 3, ReadStateNew)[0],
		validAuthenticatedVisibleAreaTopicRows(1, 3, ReadStateUnread)[0],
		validAuthenticatedVisibleAreaTopicRows(1, 3, ReadStateRead)[0],
	}
	for index := range rows {
		rows[index].TopicID = int64(index + 1)
		rows[index].TotalVisibleTopics = 3
	}
	querier := &visibleTopicPageTestQuerier{area: db.Area{ID: 1, Slug: "public"}, authenticatedTopics: rows}
	got, err := GetVisibleAreaTopicPage(context.Background(), querier, "public", 1, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember})
	if err != nil || len(got.Topics) != 3 {
		t.Fatalf("authenticated states = (%+v, %v)", got, err)
	}
	want := []ReadState{ReadStateNew, ReadStateUnread, ReadStateRead}
	for index := range got.Topics {
		if got.Topics[index].ReadState == nil || *got.Topics[index].ReadState != want[index] {
			t.Fatalf("topic %d state = %+v, want %q", index, got.Topics[index].ReadState, want[index])
		}
	}
}

func TestGetVisibleAreaTopicPageRejectsMalformedAuthenticatedRowsWithoutPartialResults(t *testing.T) {
	t.Parallel()

	valid := validAuthenticatedVisibleAreaTopicRows(1, 1, ReadStateUnread)[0]
	cases := []func(*db.ListAuthenticatedVisibleTopicsByAreaSlugRow){
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.Title = "" },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.NextPostNumber = 1 },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.ReadHead = -1 },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.ReadHead = row.NextPostNumber },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.ReadAt = pgtype.Timestamptz{} },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.LastReadPostNumber = pgtype.Int4{} },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.LastReadPostNumber.Int32 = 0 },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) {
			row.LastReadPostNumber.Int32 = row.NextPostNumber
		},
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) {
			row.ReadAt.InfinityModifier = pgtype.Infinity
		},
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.ReadState = string(ReadStateRead) },
		func(row *db.ListAuthenticatedVisibleTopicsByAreaSlugRow) { row.ReadState = "invented" },
	}
	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for index, change := range cases {
		malformed := valid
		change(&malformed)
		querier := &visibleTopicPageTestQuerier{area: db.Area{ID: 1, Slug: "public"}, authenticatedTopics: []db.ListAuthenticatedVisibleTopicsByAreaSlugRow{valid, malformed}}
		querier.authenticatedTopics[0].TotalVisibleTopics = 2
		querier.authenticatedTopics[1].TotalVisibleTopics = 2
		got, err := GetVisibleAreaTopicPage(context.Background(), querier, "public", 1, actor)
		if err == nil || !topicPageIsZero(got) || querier.topicCalls != 0 || querier.authenticatedTopicCalls != 1 {
			t.Fatalf("malformed case %d = (%+v, %v, calls %d/%d)", index, got, err, querier.topicCalls, querier.authenticatedTopicCalls)
		}
	}
}

func validVisibleAreaTopicRows(count int, total int64) []db.ListVisibleTopicsByAreaSlugRow {
	activity := pgtype.Timestamptz{Time: time.Date(2026, time.September, 7, 20, 0, 0, 0, time.UTC), Valid: true}
	rows := make([]db.ListVisibleTopicsByAreaSlugRow, count)
	for index := range rows {
		rows[index] = db.ListVisibleTopicsByAreaSlugRow{
			TopicID: int64(index + 1), Title: "Topic", State: "open", ReplyCount: 1,
			AuthorDisplayName: "Author", LastActivityAt: activity, TotalVisibleTopics: total,
		}
	}
	return rows
}

func validAuthenticatedVisibleAreaTopicRows(count int, total int64, state ReadState) []db.ListAuthenticatedVisibleTopicsByAreaSlugRow {
	visitorRows := validVisibleAreaTopicRows(count, total)
	rows := make([]db.ListAuthenticatedVisibleTopicsByAreaSlugRow, count)
	for index, visitor := range visitorRows {
		row := db.ListAuthenticatedVisibleTopicsByAreaSlugRow{
			TopicID: visitor.TopicID, Title: visitor.Title, Slug: visitor.Slug, State: visitor.State,
			PinnedAt: visitor.PinnedAt, ReplyCount: visitor.ReplyCount, AuthorDisplayName: visitor.AuthorDisplayName,
			LastActivityAt: visitor.LastActivityAt, TotalVisibleTopics: visitor.TotalVisibleTopics,
			NextPostNumber: 3, ReadHead: 2, ReadState: string(state),
		}
		switch state {
		case ReadStateNew:
		case ReadStateUnread:
			row.LastReadPostNumber = pgtype.Int4{Int32: 1, Valid: true}
			row.ReadAt = pgtype.Timestamptz{Time: visitor.LastActivityAt.Time, Valid: true}
		case ReadStateRead:
			row.LastReadPostNumber = pgtype.Int4{Int32: 2, Valid: true}
			row.ReadAt = pgtype.Timestamptz{Time: visitor.LastActivityAt.Time, Valid: true}
		default:
			panic("invalid test read state")
		}
		rows[index] = row
	}
	return rows
}

type visibleTopicPageTestQuerier struct {
	area                         db.Area
	areaErr                      error
	topics                       []db.ListVisibleTopicsByAreaSlugRow
	authenticatedTopics          []db.ListAuthenticatedVisibleTopicsByAreaSlugRow
	topicErr                     error
	areaCalls                    int
	topicCalls                   int
	authenticatedTopicCalls      int
	areaParameters               db.GetVisibleAreaBySlugParams
	topicParameters              db.ListVisibleTopicsByAreaSlugParams
	authenticatedTopicParameters db.ListAuthenticatedVisibleTopicsByAreaSlugParams
}

func (querier *visibleTopicPageTestQuerier) ListAuthenticatedVisibleTopicsByAreaSlug(_ context.Context, parameters db.ListAuthenticatedVisibleTopicsByAreaSlugParams) ([]db.ListAuthenticatedVisibleTopicsByAreaSlugRow, error) {
	querier.authenticatedTopicCalls++
	querier.authenticatedTopicParameters = parameters
	return querier.authenticatedTopics, querier.topicErr
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

func (panicVisibleTopicPageQuerier) ListAuthenticatedVisibleTopicsByAreaSlug(context.Context, db.ListAuthenticatedVisibleTopicsByAreaSlugParams) ([]db.ListAuthenticatedVisibleTopicsByAreaSlugRow, error) {
	panic("authenticated topic query must not run")
}
