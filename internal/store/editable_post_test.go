package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

func TestGetEditablePostDerivesClosedOwnerAuthority(t *testing.T) {
	t.Parallel()

	querier := &editablePostTestQuerier{row: db.GetEditablePostRow{PostID: 91, TopicID: 41, PostNumber: 2, MarkdownSource: "source", Revision: 3, NodeOrdinal: 7}}
	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator, GroupIDs: []int64{7, 9}}
	got, err := GetEditablePost(context.Background(), querier, 91, actor)
	want := EditablePost{PostID: 91, TopicID: 41, PostNumber: 2, NodeOrdinal: 7, MarkdownSource: "source", Revision: 3}
	if err != nil || got != want || querier.calls != 1 || querier.params.PostID != 91 || querier.params.AuthorID != 11 ||
		!querier.params.IsStaff || !reflect.DeepEqual(querier.params.GroupIds, []int64{7, 9}) {
		t.Fatalf("GetEditablePost() = (%+v, %v, calls %d, params %+v)", got, err, querier.calls, querier.params)
	}
}

func TestGetEditablePostTreatsIneligibleActorsAndIdentifiersAsMissing(t *testing.T) {
	t.Parallel()

	mute := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	mutedUntil := mute.ValidatedAt
	mute.MutedUntil = &mutedUntil
	for _, test := range []struct {
		postID int64
		actor  policy.AccessContext
	}{
		{postID: 0, actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}},
		{postID: 91, actor: policy.AccessContext{}},
		{postID: 91, actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, Suspended: true}},
		{postID: 91, actor: mute},
	} {
		got, err := GetEditablePost(context.Background(), panicEditablePostQuerier{}, test.postID, test.actor)
		if got != (EditablePost{}) || !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetEditablePost(%d, %+v) = (%+v, %v), want missing", test.postID, test.actor, got, err)
		}
	}
}

func TestGetEditablePostRejectsDependenciesFailuresAndMalformedRows(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	if got, err := GetEditablePost(nil, panicEditablePostQuerier{}, 91, actor); err == nil || got != (EditablePost{}) {
		t.Fatalf("nil context = (%+v, %v)", got, err)
	}
	if got, err := GetEditablePost(context.Background(), nil, 91, actor); err == nil || got != (EditablePost{}) {
		t.Fatalf("nil querier = (%+v, %v)", got, err)
	}
	if got, err := GetEditablePost(context.Background(), panicEditablePostQuerier{}, 91, policy.AccessContext{Authenticated: true, Role: policy.RoleMember}); err == nil || got != (EditablePost{}) {
		t.Fatalf("invalid actor = (%+v, %v)", got, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := GetEditablePost(canceled, panicEditablePostQuerier{}, 91, actor); !errors.Is(err, context.Canceled) || got != (EditablePost{}) {
		t.Fatalf("canceled = (%+v, %v)", got, err)
	}
	cause := errors.New("query failed")
	if got, err := GetEditablePost(context.Background(), &editablePostTestQuerier{err: cause}, 91, actor); !errors.Is(err, cause) || got != (EditablePost{}) {
		t.Fatalf("query failure = (%+v, %v)", got, err)
	}
	valid := db.GetEditablePostRow{PostID: 91, TopicID: 41, PostNumber: 2, MarkdownSource: "source", Revision: 3, NodeOrdinal: 7}
	for _, mutate := range []func(*db.GetEditablePostRow){
		func(row *db.GetEditablePostRow) { row.PostID = 0 },
		func(row *db.GetEditablePostRow) { row.TopicID = 0 },
		func(row *db.GetEditablePostRow) { row.PostNumber = 0 },
		func(row *db.GetEditablePostRow) { row.NodeOrdinal = 0 },
		func(row *db.GetEditablePostRow) { row.MarkdownSource = "" },
		func(row *db.GetEditablePostRow) { row.MarkdownSource = strings.Repeat("x", 65_537) },
		func(row *db.GetEditablePostRow) { row.MarkdownSource = string([]byte{0xff}) },
		func(row *db.GetEditablePostRow) { row.Revision = 0 },
		func(row *db.GetEditablePostRow) { row.Revision = maximumEditablePostRevision },
	} {
		row := valid
		mutate(&row)
		if got, err := GetEditablePost(context.Background(), &editablePostTestQuerier{row: row}, 91, actor); err == nil || got != (EditablePost{}) {
			t.Fatalf("malformed row %+v = (%+v, %v)", row, got, err)
		}
	}
}

type editablePostTestQuerier struct {
	row    db.GetEditablePostRow
	err    error
	params db.GetEditablePostParams
	calls  int
}

func (querier *editablePostTestQuerier) GetEditablePost(_ context.Context, params db.GetEditablePostParams) (db.GetEditablePostRow, error) {
	querier.calls++
	querier.params = params
	return querier.row, querier.err
}

type panicEditablePostQuerier struct{}

func (panicEditablePostQuerier) GetEditablePost(context.Context, db.GetEditablePostParams) (db.GetEditablePostRow, error) {
	panic("editable post query called")
}
