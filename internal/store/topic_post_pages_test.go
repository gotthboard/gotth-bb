package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGetVisibleTopicPostPageDerivesAuthorityAndPagination(t *testing.T) {
	t.Parallel()

	wantRows := validVisibleTopicPostRows(26, 27)
	wantRows[1].Revision = pgtype.Int4{Int32: 2, Valid: true}
	wantRows[1].PostEditedAt = wantRows[1].PostUpdatedAt
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
			querier := &visibleTopicPostPageTestQuerier{rows: wantRows}
			got, err := GetVisibleTopicPostPage(context.Background(), querier, 9, 2, test.actor)
			if err != nil || got.Number != 2 || got.TotalPosts != 27 || got.TotalPages != 2 || !reflect.DeepEqual(got.Rows, wantRows) || querier.calls != 1 {
				t.Fatalf("GetVisibleTopicPostPage() = (%+v, %v, calls %d)", got, err, querier.calls)
			}
			parameters := querier.parameters
			if parameters.TopicID != 9 || parameters.PageOffset != PostPageSize || parameters.PageLimit != PostPageSize ||
				parameters.IsStaff != test.wantStaff || parameters.IsMember != test.wantMember || !equalGroupIDs(parameters.GroupIds, test.wantGroups) {
				t.Fatalf("parameters = %+v, want staff=%t member=%t groups=%v", parameters, test.wantStaff, test.wantMember, test.wantGroups)
			}
		})
	}
}

func TestGetVisibleTopicPostPageAcceptsAuthorizedEmptyFirstPage(t *testing.T) {
	t.Parallel()

	row := validVisibleTopicPostRows(1, 1)[0]
	clearVisiblePost(&row)
	row.TotalVisiblePosts = 0
	got, err := GetVisibleTopicPostPage(context.Background(), &visibleTopicPostPageTestQuerier{rows: []db.GetVisibleTopicPostPageRow{row}}, 9, 1, policy.AccessContext{})
	if err != nil || got.Number != 1 || got.TotalPosts != 0 || got.TotalPages != 0 || len(got.Rows) != 1 || got.Rows[0].PostID.Valid {
		t.Fatalf("GetVisibleTopicPostPage(empty) = (%+v, %v)", got, err)
	}
}

func TestGetVisibleTopicPostPagePreservesSoftDeletedPostNumberGaps(t *testing.T) {
	t.Parallel()

	rows := validVisibleTopicPostRows(1, 2)
	rows[1].PostNumber.Int32 = 3
	got, err := GetVisibleTopicPostPage(context.Background(), &visibleTopicPostPageTestQuerier{rows: rows}, 9, 1, policy.AccessContext{})
	if err != nil || len(got.Rows) != 2 || got.Rows[0].PostNumber.Int32 != 1 || got.Rows[1].PostNumber.Int32 != 3 || got.TotalPosts != 2 {
		t.Fatalf("GetVisibleTopicPostPage(gapped) = (%+v, %v)", got, err)
	}
}

func TestGetVisibleTopicPostPageAcceptsMaximumPage(t *testing.T) {
	t.Parallel()

	firstPost := int64(MaximumPostPage-1)*int64(PostPageSize) + 1
	rows := validVisibleTopicPostRows(firstPost, firstPost+int64(PostPageSize)-1)
	querier := &visibleTopicPostPageTestQuerier{rows: rows}
	got, err := GetVisibleTopicPostPage(context.Background(), querier, 9, MaximumPostPage, policy.AccessContext{})
	if err != nil || got.Number != MaximumPostPage || got.TotalPages != int64(MaximumPostPage) || len(got.Rows) != int(PostPageSize) ||
		querier.parameters.PageOffset != (MaximumPostPage-1)*PostPageSize {
		t.Fatalf("GetVisibleTopicPostPage(maximum) = (%+v, %v, parameters %+v)", got, err, querier.parameters)
	}
}

func TestGetVisibleTopicPostPageTreatsInvalidIdentifiersAndEmptyResultsAsMissing(t *testing.T) {
	t.Parallel()

	for _, input := range []struct {
		topicID int64
		page    int32
	}{{topicID: 0, page: 1}, {topicID: -1, page: 1}, {topicID: 9, page: 0}, {topicID: 9, page: -1}, {topicID: 9, page: MaximumPostPage + 1}} {
		input := input
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()
			got, err := GetVisibleTopicPostPage(context.Background(), panicVisibleTopicPostPageQuerier{}, input.topicID, input.page, policy.AccessContext{})
			if !errors.Is(err, pgx.ErrNoRows) || !visibleTopicPostPageIsZero(got) {
				t.Fatalf("GetVisibleTopicPostPage(%d, %d) = (%+v, %v), want zero/no rows", input.topicID, input.page, got, err)
			}
		})
	}
	querier := &visibleTopicPostPageTestQuerier{}
	got, err := GetVisibleTopicPostPage(context.Background(), querier, 9, 2, policy.AccessContext{})
	if !errors.Is(err, pgx.ErrNoRows) || !visibleTopicPostPageIsZero(got) || querier.calls != 1 {
		t.Fatalf("GetVisibleTopicPostPage(empty later) = (%+v, %v, calls %d)", got, err, querier.calls)
	}
}

func TestGetVisibleTopicPostPageRejectsDependenciesAuthorityAndQueryFailure(t *testing.T) {
	t.Parallel()

	validActor := policy.AccessContext{}
	if got, err := GetVisibleTopicPostPage(nil, panicVisibleTopicPostPageQuerier{}, 9, 1, validActor); err == nil || !visibleTopicPostPageIsZero(got) {
		t.Fatalf("nil context = (%+v, %v)", got, err)
	}
	if got, err := GetVisibleTopicPostPage(context.Background(), nil, 9, 1, validActor); err == nil || !visibleTopicPostPageIsZero(got) {
		t.Fatalf("nil querier = (%+v, %v)", got, err)
	}
	invalidActor := policy.AccessContext{Authenticated: true, Role: policy.RoleMember}
	if got, err := GetVisibleTopicPostPage(context.Background(), panicVisibleTopicPostPageQuerier{}, 9, 1, invalidActor); err == nil || !visibleTopicPostPageIsZero(got) {
		t.Fatalf("invalid actor = (%+v, %v)", got, err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := GetVisibleTopicPostPage(canceledContext, panicVisibleTopicPostPageQuerier{}, 9, 1, validActor); !errors.Is(err, context.Canceled) || !visibleTopicPostPageIsZero(got) {
		t.Fatalf("canceled context = (%+v, %v)", got, err)
	}
	cause := errors.New("query failed")
	querier := &visibleTopicPostPageTestQuerier{err: cause}
	if got, err := GetVisibleTopicPostPage(context.Background(), querier, 9, 1, validActor); !errors.Is(err, cause) || !visibleTopicPostPageIsZero(got) || querier.calls != 1 {
		t.Fatalf("query failure = (%+v, %v, calls %d)", got, err, querier.calls)
	}
}

func TestGetVisibleTopicPostPageRejectsMalformedRows(t *testing.T) {
	t.Parallel()

	validRows := func() []db.GetVisibleTopicPostPageRow { return validVisibleTopicPostRows(1, 2) }
	for _, mutate := range []func([]db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow{
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].AreaID = 0
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].AreaSlug = "bad/slash"
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].AreaName = ""
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TopicID = 8
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TopicTitle = ""
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TopicState = "invented"
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TopicState = "hidden"
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TopicCreatedAt = pgtype.Timestamptz{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TopicCreatedAt.InfinityModifier = pgtype.Infinity
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TopicAuthorDisplayName = ""
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].TotalVisiblePosts = -1
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[1].AreaID = 4
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[1].TotalVisiblePosts = 3
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].PostID = pgtype.Int8{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].PostNumber = pgtype.Int4{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].RenderedHtml = pgtype.Text{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].RendererVersion = pgtype.Text{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].Revision = pgtype.Int4{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].Revision.Int32 = 2
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].PostCreatedAt = pgtype.Timestamptz{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].PostCreatedAt.InfinityModifier = pgtype.Infinity
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].PostUpdatedAt = pgtype.Timestamptz{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].PostAuthorID = pgtype.Int8{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[0].PostAuthorDisplayName = pgtype.Text{}
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			rows[1].PostNumber.Int32 = 1
			return rows
		},
		func(rows []db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow { return rows[:1] },
		func([]db.GetVisibleTopicPostPageRow) []db.GetVisibleTopicPostPageRow {
			return validVisibleTopicPostRows(1, int64(PostPageSize)+1)
		},
	} {
		mutate := mutate
		t.Run("malformed", func(t *testing.T) {
			t.Parallel()
			rows := mutate(validRows())
			got, err := GetVisibleTopicPostPage(context.Background(), &visibleTopicPostPageTestQuerier{rows: rows}, 9, 1, policy.AccessContext{})
			if err == nil || errors.Is(err, pgx.ErrNoRows) || !visibleTopicPostPageIsZero(got) {
				t.Fatalf("GetVisibleTopicPostPage(malformed) = (%+v, %v), want zero/internal error", got, err)
			}
		})
	}

	sentinel := validVisibleTopicPostRows(1, 1)[0]
	clearVisiblePost(&sentinel)
	sentinel.TotalVisiblePosts = 0
	for _, test := range []struct {
		name string
		page int32
		row  db.GetVisibleTopicPostPageRow
	}{
		{name: "sentinel later page", page: 2, row: sentinel},
		{name: "sentinel with post ID", page: 1, row: func() db.GetVisibleTopicPostPageRow {
			row := sentinel
			row.PostID = pgtype.Int8{Int64: 1, Valid: true}
			return row
		}()},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetVisibleTopicPostPage(context.Background(), &visibleTopicPostPageTestQuerier{rows: []db.GetVisibleTopicPostPageRow{test.row}}, 9, test.page, policy.AccessContext{})
			if err == nil || errors.Is(err, pgx.ErrNoRows) || !visibleTopicPostPageIsZero(got) {
				t.Fatalf("GetVisibleTopicPostPage(%s) = (%+v, %v), want zero/internal error", test.name, got, err)
			}
		})
	}
}

func validVisibleTopicPostRows(firstPost, lastPost int64) []db.GetVisibleTopicPostPageRow {
	createdAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	total := lastPost
	rows := make([]db.GetVisibleTopicPostPageRow, 0, lastPost-firstPost+1)
	for postNumber := firstPost; postNumber <= lastPost; postNumber++ {
		rows = append(rows, db.GetVisibleTopicPostPageRow{
			AreaID: 3, AreaSlug: "public", AreaName: "Public", AreaDescription: "Open area", AreaPostingMode: "normal",
			TopicID: 9, TopicFirstPostID: 101, TopicTitle: "Welcome", TopicState: "open", TopicCreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true}, TopicAuthorDisplayName: "Starter",
			PostID: pgtype.Int8{Int64: 100 + postNumber, Valid: true}, PostNumber: pgtype.Int4{Int32: int32(postNumber), Valid: true},
			RenderedHtml: pgtype.Text{String: "<p>Post</p>", Valid: true}, RendererVersion: pgtype.Text{String: "test-v1", Valid: true},
			Revision: pgtype.Int4{Int32: 1, Valid: true}, PostCreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
			PostUpdatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true}, PostAuthorID: pgtype.Int8{Int64: 11, Valid: true},
			PostAuthorDisplayName: pgtype.Text{String: "Author", Valid: true},
			TotalVisiblePosts:     total,
		})
	}
	return rows
}

func clearVisiblePost(row *db.GetVisibleTopicPostPageRow) {
	row.PostID = pgtype.Int8{}
	row.PostNumber = pgtype.Int4{}
	row.RenderedHtml = pgtype.Text{}
	row.RendererVersion = pgtype.Text{}
	row.Revision = pgtype.Int4{}
	row.PostCreatedAt = pgtype.Timestamptz{}
	row.PostUpdatedAt = pgtype.Timestamptz{}
	row.PostEditedAt = pgtype.Timestamptz{}
	row.PostAuthorID = pgtype.Int8{}
	row.PostAuthorDisplayName = pgtype.Text{}
}

func visibleTopicPostPageIsZero(page VisibleTopicPostPage) bool {
	return page.Rows == nil && page.Number == 0 && page.TotalPosts == 0 && page.TotalPages == 0
}

type visibleTopicPostPageTestQuerier struct {
	rows       []db.GetVisibleTopicPostPageRow
	err        error
	parameters db.GetVisibleTopicPostPageParams
	calls      int
}

func (querier *visibleTopicPostPageTestQuerier) GetVisibleTopicPostPage(_ context.Context, parameters db.GetVisibleTopicPostPageParams) ([]db.GetVisibleTopicPostPageRow, error) {
	querier.calls++
	querier.parameters = parameters
	return querier.rows, querier.err
}

type panicVisibleTopicPostPageQuerier struct{}

func (panicVisibleTopicPostPageQuerier) GetVisibleTopicPostPage(context.Context, db.GetVisibleTopicPostPageParams) ([]db.GetVisibleTopicPostPageRow, error) {
	panic("visible topic post page query must not run")
}
