package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGetVisibleTopicPostPageBindsAccessAndPaginationAndScansRows(t *testing.T) {
	t.Parallel()

	want := []GetVisibleTopicPostPageRow{{
		AreaID: 3, AreaSlug: "members", AreaName: "Members", AreaDescription: "Private",
		TopicID: 9, TopicTitle: "Welcome", TopicState: "locked", TopicPinnedAt: pgtype.Timestamptz{Valid: true},
		TopicCreatedAt: pgtype.Timestamptz{Valid: true}, TopicAuthorDisplayName: "Starter",
		PostID: pgtype.Int8{Int64: 17, Valid: true}, PostNumber: pgtype.Int4{Int32: 2, Valid: true},
		RenderedHtml: pgtype.Text{String: "<p>Reply</p>", Valid: true}, RendererVersion: pgtype.Text{String: "v1", Valid: true},
		Revision: pgtype.Int4{Int32: 3, Valid: true}, PostCreatedAt: pgtype.Timestamptz{Valid: true},
		PostUpdatedAt: pgtype.Timestamptz{Valid: true}, PostEditedAt: pgtype.Timestamptz{Valid: true},
		PostAuthorDisplayName: pgtype.Text{String: "Replier", Valid: true}, TotalVisiblePosts: 12,
	}}
	ctx := context.WithValue(context.Background(), topicPostContextKey{}, "preserved")
	database := &topicPostDBTX{rows: &topicPostRows{items: want}}
	got, err := New(database).GetVisibleTopicPostPage(ctx, GetVisibleTopicPostPageParams{
		TopicID: 9, IsStaff: false, IsMember: true, GroupIds: []int64{11, 13}, PageLimit: 25, PageOffset: 50,
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetVisibleTopicPostPage() = (%+v, %v), want %+v", got, err, want)
	}
	if database.ctx != ctx || len(database.args) != 6 || database.args[0] != int32(50) || database.args[1] != int32(25) ||
		database.args[2] != int64(9) || database.args[3] != false || database.args[4] != true ||
		!reflect.DeepEqual(database.args[5], []int64{11, 13}) {
		t.Fatalf("query call = (context %v, args %#v)", database.ctx, database.args)
	}
	for _, required := range []string{
		"WITH visible_topic AS",
		"topic.id = $3",
		"topic.deleted_at IS NULL",
		"$4::boolean OR topic.state <> 'hidden'",
		"area.visibility = 'groups'",
		"membership.group_id = ANY($6::bigint[])",
		"LEFT JOIN public.posts AS post",
		"post.deleted_at IS NULL",
		"count(post.id) OVER ()::bigint",
		"ORDER BY post.post_number ASC NULLS FIRST",
		"LIMIT $2::integer OFFSET $1::integer",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("visible topic-post query lacks %q", required)
		}
	}
	if database.rows.closeCalls != 1 {
		t.Fatalf("rows close calls = %d, want one", database.rows.closeCalls)
	}
}

func TestGetVisibleTopicPostPagePreservesQueryScanAndRowsFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("topic post page failed")
	for _, test := range []struct {
		name     string
		database *topicPostDBTX
	}{
		{name: "query", database: &topicPostDBTX{queryErr: cause}},
		{name: "scan", database: &topicPostDBTX{rows: &topicPostRows{items: []GetVisibleTopicPostPageRow{{TopicID: 1}}, scanErr: cause}}},
		{name: "rows", database: &topicPostDBTX{rows: &topicPostRows{rowsErr: cause}}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := New(test.database).GetVisibleTopicPostPage(context.Background(), GetVisibleTopicPostPageParams{})
			if !errors.Is(err, cause) || len(got) != 0 {
				t.Fatalf("GetVisibleTopicPostPage() = (%+v, %v), want empty/cause", got, err)
			}
		})
	}
}

type topicPostContextKey struct{}

type topicPostDBTX struct {
	DBTX
	ctx      context.Context
	query    string
	args     []any
	rows     *topicPostRows
	queryErr error
}

func (database *topicPostDBTX) Query(ctx context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	if database.queryErr != nil {
		return nil, database.queryErr
	}
	return database.rows, nil
}

type topicPostRows struct {
	pgx.Rows
	items      []GetVisibleTopicPostPageRow
	index      int
	scanErr    error
	rowsErr    error
	closeCalls int
}

func (rows *topicPostRows) Close() { rows.closeCalls++ }

func (rows *topicPostRows) Next() bool { return rows.index < len(rows.items) }

func (rows *topicPostRows) Scan(destinations ...any) error {
	if rows.scanErr != nil {
		return rows.scanErr
	}
	item := rows.items[rows.index]
	rows.index++
	*(destinations[0].(*int64)) = item.AreaID
	*(destinations[1].(*string)) = item.AreaSlug
	*(destinations[2].(*string)) = item.AreaName
	*(destinations[3].(*string)) = item.AreaDescription
	*(destinations[4].(*int64)) = item.TopicID
	*(destinations[5].(*string)) = item.TopicTitle
	*(destinations[6].(*string)) = item.TopicState
	*(destinations[7].(*pgtype.Timestamptz)) = item.TopicPinnedAt
	*(destinations[8].(*pgtype.Timestamptz)) = item.TopicCreatedAt
	*(destinations[9].(*string)) = item.TopicAuthorDisplayName
	*(destinations[10].(*pgtype.Int8)) = item.PostID
	*(destinations[11].(*pgtype.Int4)) = item.PostNumber
	*(destinations[12].(*pgtype.Text)) = item.RenderedHtml
	*(destinations[13].(*pgtype.Text)) = item.RendererVersion
	*(destinations[14].(*pgtype.Int4)) = item.Revision
	*(destinations[15].(*pgtype.Timestamptz)) = item.PostCreatedAt
	*(destinations[16].(*pgtype.Timestamptz)) = item.PostUpdatedAt
	*(destinations[17].(*pgtype.Timestamptz)) = item.PostEditedAt
	*(destinations[18].(*pgtype.Text)) = item.PostAuthorDisplayName
	*(destinations[19].(*int64)) = item.TotalVisiblePosts
	return nil
}

func (rows *topicPostRows) Err() error { return rows.rowsErr }
