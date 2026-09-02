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

func TestPublishingRowQueriesBindAndScanExactValues(t *testing.T) {
	t.Parallel()

	atTime := pgtype.Timestamptz{Valid: true}
	for _, test := range []struct {
		name       string
		rowValues  []any
		wantArgs   []any
		required   []string
		invoke     func(*Queries) (any, error)
		wantResult any
	}{
		{
			name: "lock area", rowValues: []any{int64(7), "groups", "normal"}, wantArgs: []any{"members"},
			required:   []string{"WHERE area.slug = $1", "FOR SHARE OF area"},
			invoke:     func(q *Queries) (any, error) { return q.LockAreaForTopicCreation(context.Background(), "members") },
			wantResult: LockAreaForTopicCreationRow{ID: 7, Visibility: "groups", PostingMode: "normal"},
		},
		{
			name: "lock topic", rowValues: []any{int64(9), "locked", int64(7), "public", "read_only", int64(17), int32(2)}, wantArgs: []any{int64(17), int64(9)},
			required: []string{"parent.id = $1", "topic.id = $2", "parent.deleted_at IS NULL", "FOR UPDATE OF topic", "FOR SHARE OF area, parent"},
			invoke: func(q *Queries) (any, error) {
				return q.LockTopicForReply(context.Background(), LockTopicForReplyParams{TopicID: 9, ParentPostID: 17})
			},
			wantResult: LockTopicForReplyRow{TopicID: 9, TopicState: "locked", AreaID: 7, Visibility: "public", PostingMode: "read_only", ParentPostID: 17, ParentDepth: 2},
		},
		{
			name: "create topic", rowValues: []any{int64(9), int64(17), int32(1), int64(1)},
			wantArgs: []any{int64(7), int64(11), "Title", atTime, "source", "<p>source</p>", "renderer-v1"},
			required: []string{"pg_get_serial_sequence('public.topics', 'id')", "inserted_topic AS", "inserted_post AS"},
			invoke: func(q *Queries) (any, error) {
				return q.CreateTopicAndFirstPost(context.Background(), CreateTopicAndFirstPostParams{AreaID: 7, AuthorID: 11, Title: "Title", AtTime: atTime, MarkdownSource: "source", RenderedHtml: "<p>source</p>", RendererVersion: "renderer-v1"})
			},
			wantResult: CreateTopicAndFirstPostRow{TopicID: 9, PostID: 17, PostNumber: 1, NodeOrdinal: 1},
		},
		{
			name: "create reply", rowValues: []any{int64(9), int64(18), int32(2), int64(2)},
			wantArgs: []any{int64(11), "reply", "<p>reply</p>", "renderer-v1", pgtype.Int8{Int64: 17, Valid: true}, atTime, int64(9)},
			required: []string{"topic.next_post_number", "reply_count = inserted_post.post_number - 1", "next_post_number = inserted_post.post_number + 1"},
			invoke: func(q *Queries) (any, error) {
				return q.CreateReplyAndAdvanceTopic(context.Background(), CreateReplyAndAdvanceTopicParams{AuthorID: 11, MarkdownSource: "reply", RenderedHtml: "<p>reply</p>", RendererVersion: "renderer-v1", ParentPostID: pgtype.Int8{Int64: 17, Valid: true}, AtTime: atTime, TopicID: 9})
			},
			wantResult: CreateReplyAndAdvanceTopicRow{TopicID: 9, PostID: 18, PostNumber: 2, NodeOrdinal: 2},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &publishingDBTX{row: publishingRow{values: test.rowValues}}
			got, err := test.invoke(New(database))
			if err != nil || !reflect.DeepEqual(got, test.wantResult) || !reflect.DeepEqual(database.args, test.wantArgs) {
				t.Fatalf("publishing query = (result %+v, error %v, args %#v)", got, err, database.args)
			}
			for _, required := range test.required {
				if !strings.Contains(database.query, required) {
					t.Fatalf("publishing query lacks %q", required)
				}
			}
		})
	}
}

func TestPublishingRowQueriesPreserveScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	database := &publishingDBTX{row: publishingRow{err: cause}}
	if got, err := New(database).LockAreaForTopicCreation(context.Background(), "news"); !errors.Is(err, cause) || got != (LockAreaForTopicCreationRow{}) {
		t.Fatalf("LockAreaForTopicCreation() = (%+v, %v), want zero/cause", got, err)
	}
}

func TestLockAreaGroupIDsBindsScansClosesAndPreservesFailures(t *testing.T) {
	t.Parallel()

	rows := &publishingRows{values: [][]any{{int64(3)}, {int64(5)}}}
	database := &publishingDBTX{rows: rows}
	got, err := New(database).LockAreaGroupIDs(context.Background(), 7)
	if err != nil || !reflect.DeepEqual(got, []int64{3, 5}) || !reflect.DeepEqual(database.args, []any{int64(7)}) || rows.closeCalls != 1 ||
		!strings.Contains(database.query, "ORDER BY mapping.group_id") || !strings.Contains(database.query, "FOR SHARE OF mapping") {
		t.Fatalf("LockAreaGroupIDs() = (%v, %v), query %q args %#v closes %d", got, err, database.query, database.args, rows.closeCalls)
	}
	cause := errors.New("rows failed")
	for _, failing := range []*publishingDBTX{
		{queryErr: cause},
		{rows: &publishingRows{values: [][]any{{int64(3)}}, scanErr: cause}},
		{rows: &publishingRows{rowsErr: cause}},
	} {
		if got, err := New(failing).LockAreaGroupIDs(context.Background(), 7); !errors.Is(err, cause) || len(got) != 0 {
			t.Fatalf("failing LockAreaGroupIDs() = (%v, %v), want empty/cause", got, err)
		}
	}
}

type publishingDBTX struct {
	DBTX
	query    string
	args     []any
	row      pgx.Row
	rows     pgx.Rows
	queryErr error
}

func (database *publishingDBTX) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	database.query, database.args = query, append([]any(nil), args...)
	return database.row
}

func (database *publishingDBTX) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	database.query, database.args = query, append([]any(nil), args...)
	if database.queryErr != nil {
		return nil, database.queryErr
	}
	return database.rows, nil
}

type publishingRow struct {
	values []any
	err    error
}

func (row publishingRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *int64:
			*destination = value.(int64)
		case *int32:
			*destination = value.(int32)
		case *string:
			*destination = value.(string)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		case *pgtype.Text:
			*destination = value.(pgtype.Text)
		default:
			panic("unexpected publishing row destination")
		}
	}
	return nil
}

type publishingRows struct {
	pgx.Rows
	values     [][]any
	index      int
	scanErr    error
	rowsErr    error
	closeCalls int
}

func (rows *publishingRows) Close()     { rows.closeCalls++ }
func (rows *publishingRows) Next() bool { return rows.index < len(rows.values) }
func (rows *publishingRows) Scan(destinations ...any) error {
	if rows.scanErr != nil {
		return rows.scanErr
	}
	*(destinations[0].(*int64)) = rows.values[rows.index][0].(int64)
	rows.index++
	return nil
}
func (rows *publishingRows) Err() error { return rows.rowsErr }
