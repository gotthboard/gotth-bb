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

func TestListVisibleTopicsByAreaSlugBindsAccessAndPaginationAndScansSummaries(t *testing.T) {
	t.Parallel()

	want := []ListVisibleTopicsByAreaSlugRow{
		{TopicID: 9, Title: "Pinned", Slug: pgtype.Text{String: "pinned", Valid: true}, State: "locked", PinnedAt: pgtype.Timestamptz{Valid: true}, ReplyCount: 4, AuthorDisplayName: "Author One", LastActivityAt: pgtype.Timestamptz{Valid: true}, TotalVisibleTopics: 12},
		{TopicID: 7, Title: "Recent", State: "open", ReplyCount: 2, AuthorDisplayName: "Author Two", LastActivityAt: pgtype.Timestamptz{Valid: true}, TotalVisibleTopics: 12},
	}
	ctx := context.WithValue(context.Background(), topicListContextKey{}, "preserved")
	database := &topicListDBTX{rows: &topicListRows{topics: want}}
	got, err := New(database).ListVisibleTopicsByAreaSlug(ctx, ListVisibleTopicsByAreaSlugParams{
		AreaSlug: "members", IsStaff: false, IsMember: true, GroupIds: []int64{11, 13}, PageLimit: 26, PageOffset: 25,
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ListVisibleTopicsByAreaSlug() = (%+v, %v), want %+v", got, err, want)
	}
	if database.ctx != ctx || len(database.args) != 6 || database.args[0] != "members" ||
		database.args[1] != false || database.args[2] != true ||
		!reflect.DeepEqual(database.args[3], []int64{11, 13}) ||
		database.args[4] != int32(25) || database.args[5] != int32(26) {
		t.Fatalf("query call = (context %v, args %#v)", database.ctx, database.args)
	}
	for _, required := range []string{
		"FROM public.areas AS area",
		"JOIN public.topics AS topic ON topic.area_id = area.id",
		"area.slug = $1",
		"area.visibility = 'groups'",
		"membership.group_id = ANY($4::bigint[])",
		"topic.deleted_at IS NULL",
		"$2::boolean OR topic.state <> 'hidden'",
		"count(*) OVER ()::bigint",
		"ORDER BY topic.pinned_at DESC NULLS LAST, topic.last_activity_at DESC, topic.id DESC",
		"LIMIT $6::integer OFFSET $5::integer",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("visible-topic query lacks %q", required)
		}
	}
	if database.rows.closeCalls != 1 {
		t.Fatalf("rows close calls = %d, want one", database.rows.closeCalls)
	}
}

func TestListVisibleTopicsByAreaSlugPreservesQueryScanAndRowsFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("topic list failed")
	for _, test := range []struct {
		name     string
		database *topicListDBTX
	}{
		{name: "query", database: &topicListDBTX{queryErr: cause}},
		{name: "scan", database: &topicListDBTX{rows: &topicListRows{topics: []ListVisibleTopicsByAreaSlugRow{{TopicID: 1}}, scanErr: cause}}},
		{name: "rows", database: &topicListDBTX{rows: &topicListRows{rowsErr: cause}}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := New(test.database).ListVisibleTopicsByAreaSlug(context.Background(), ListVisibleTopicsByAreaSlugParams{})
			if !errors.Is(err, cause) || len(got) != 0 {
				t.Fatalf("ListVisibleTopicsByAreaSlug() = (%+v, %v), want empty/cause", got, err)
			}
		})
	}
}

type topicListContextKey struct{}

type topicListDBTX struct {
	DBTX
	ctx      context.Context
	query    string
	args     []any
	rows     *topicListRows
	queryErr error
}

func (database *topicListDBTX) Query(ctx context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	if database.queryErr != nil {
		return nil, database.queryErr
	}
	return database.rows, nil
}

type topicListRows struct {
	pgx.Rows
	topics     []ListVisibleTopicsByAreaSlugRow
	index      int
	scanErr    error
	rowsErr    error
	closeCalls int
}

func (rows *topicListRows) Close() { rows.closeCalls++ }

func (rows *topicListRows) Next() bool { return rows.index < len(rows.topics) }

func (rows *topicListRows) Scan(destinations ...any) error {
	if rows.scanErr != nil {
		return rows.scanErr
	}
	topic := rows.topics[rows.index]
	rows.index++
	*(destinations[0].(*int64)) = topic.TopicID
	*(destinations[1].(*string)) = topic.Title
	*(destinations[2].(*pgtype.Text)) = topic.Slug
	*(destinations[3].(*string)) = topic.State
	*(destinations[4].(*pgtype.Timestamptz)) = topic.PinnedAt
	*(destinations[5].(*int32)) = topic.ReplyCount
	*(destinations[6].(*string)) = topic.AuthorDisplayName
	*(destinations[7].(*pgtype.Timestamptz)) = topic.LastActivityAt
	*(destinations[8].(*int64)) = topic.TotalVisibleTopics
	return nil
}

func (rows *topicListRows) Err() error { return rows.rowsErr }
