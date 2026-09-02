package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestEditingQueriesBindScanAndPreserveGuards(t *testing.T) {
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
			name: "editable read", rowValues: []any{int64(91), int64(41), int32(2), "source", int32(3)},
			wantArgs: []any{int64(91), int64(11), true, []int64{7, 9}},
			required: []string{"post.author_id = $2", "post.deleted_at IS NULL", "topic.deleted_at IS NULL", "$3::boolean OR topic.state <> 'hidden'", "area.visibility = 'groups'", "membership.group_id = ANY($4::bigint[])"},
			invoke: func(q *Queries) (any, error) {
				return q.GetEditablePost(context.Background(), GetEditablePostParams{PostID: 91, AuthorID: 11, IsStaff: true, GroupIds: []int64{7, 9}})
			},
			wantResult: GetEditablePostRow{PostID: 91, TopicID: 41, PostNumber: 2, MarkdownSource: "source", Revision: 3},
		},
		{
			name: "lock", rowValues: []any{int64(91), int64(11), int32(3), int64(41), int32(2), "locked", int64(7), "groups", "normal"}, wantArgs: []any{int64(91)},
			required:   []string{"post.deleted_at IS NULL", "topic.deleted_at IS NULL", "FOR UPDATE OF post", "FOR SHARE OF topic, area"},
			invoke:     func(q *Queries) (any, error) { return q.LockPostForEdit(context.Background(), 91) },
			wantResult: LockPostForEditRow{PostID: 91, AuthorID: 11, Revision: 3, TopicID: 41, PostNumber: 2, TopicState: "locked", AreaID: 7, Visibility: "groups", PostingMode: "normal"},
		},
		{
			name: "update", rowValues: []any{int64(91), int64(41), int32(2), int32(4)},
			wantArgs: []any{"edited", "<p>edited</p>", "renderer-v1", atTime, int64(91), int32(3)},
			required: []string{"revision = post.revision + 1", "post.revision = $6", "GREATEST($4::timestamptz, post.updated_at, COALESCE(post.edited_at, '-infinity'::timestamptz))", "post.deleted_at IS NULL"},
			invoke: func(q *Queries) (any, error) {
				return q.UpdatePostRevision(context.Background(), UpdatePostRevisionParams{MarkdownSource: "edited", RenderedHtml: "<p>edited</p>", RendererVersion: "renderer-v1", AtTime: atTime, PostID: 91, ExpectedRevision: 3})
			},
			wantResult: UpdatePostRevisionRow{PostID: 91, TopicID: 41, PostNumber: 2, Revision: 4},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &publishingDBTX{row: publishingRow{values: test.rowValues}}
			got, err := test.invoke(New(database))
			if err != nil || !reflect.DeepEqual(got, test.wantResult) || !reflect.DeepEqual(database.args, test.wantArgs) {
				t.Fatalf("editing query = (result %+v, error %v, args %#v)", got, err, database.args)
			}
			for _, required := range test.required {
				if !strings.Contains(database.query, required) {
					t.Fatalf("editing query lacks %q", required)
				}
			}
		})
	}
}

func TestEditingQueriesPreserveScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	for _, invoke := range []func(*Queries) (any, error){
		func(q *Queries) (any, error) { return q.GetEditablePost(context.Background(), GetEditablePostParams{}) },
		func(q *Queries) (any, error) { return q.LockPostForEdit(context.Background(), 91) },
		func(q *Queries) (any, error) {
			return q.UpdatePostRevision(context.Background(), UpdatePostRevisionParams{})
		},
	} {
		database := &publishingDBTX{row: publishingRow{err: cause}}
		if _, err := invoke(New(database)); !errors.Is(err, cause) {
			t.Fatalf("editing scan error = %v, want cause", err)
		}
	}
}
