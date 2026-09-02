package forum

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestEditPostCommitsAuthorizedExpectedRevision(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 2, 6, 0, 0, 123456789, time.UTC)
	tx := &editTestTx{postID: 91, authorID: 11, revision: 3, topicID: 41, postNumber: 2, areaID: 7, visibility: "groups", postingMode: "normal", groupIDs: []int64{4, 9}}
	result, err := EditPost(context.Background(), editTestBeginner{tx: tx}, func() time.Time { return at },
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, GroupIDs: []int64{9}}, 91, 3, "Edited **body**")
	if err != nil || result != (EditResult{TopicID: 41, PostID: 91, PostNumber: 2, NodeOrdinal: 2, Revision: 4}) {
		t.Fatalf("EditPost() = (%+v, %v)", result, err)
	}
	if !tx.committed || tx.rolledBack || tx.updateCalls != 1 || tx.markdown != "Edited **body**" ||
		tx.renderedHTML != "<p>Edited <strong>body</strong></p>\n" || tx.rendererVersion != render.RendererVersion ||
		!tx.atTime.Equal(at.UTC().Truncate(time.Microsecond)) || tx.expectedRevision != 3 {
		t.Fatalf("edit transaction = %+v", tx)
	}
}

func TestEditPostDeniesBeforeConflictDisclosureOrUpdate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		actor      policy.AccessContext
		visibility string
		topicState string
		groupIDs   []int64
	}{
		{name: "foreign owner", actor: policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleAdministrator}, visibility: "public"},
		{name: "hidden area", actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, visibility: "hidden"},
		{name: "group miss", actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, GroupIDs: []int64{8}}, visibility: "groups", groupIDs: []int64{9}},
		{name: "hidden topic", actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, visibility: "public", topicState: "hidden"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &editTestTx{postID: 91, authorID: 11, revision: 3, topicID: 41, postNumber: 2, areaID: 7, visibility: test.visibility, postingMode: "normal", topicState: test.topicState, groupIDs: test.groupIDs}
			result, err := EditPost(context.Background(), editTestBeginner{tx: tx}, time.Now, test.actor, 91, 1, "body")
			if result != (EditResult{}) || !errors.Is(err, ErrPostEditDenied) || errors.Is(err, ErrPostEditConflict) || tx.updateCalls != 0 || tx.committed || !tx.rolledBack {
				t.Fatalf("denied edit = (%+v, %v, tx %+v)", result, err, tx)
			}
		})
	}
}

func TestEditPostReportsAuthorizedRevisionConflictWithoutUpdate(t *testing.T) {
	t.Parallel()

	tx := &editTestTx{postID: 91, authorID: 11, revision: 4, topicID: 41, postNumber: 2, areaID: 7, visibility: "public", postingMode: "normal"}
	result, err := EditPost(context.Background(), editTestBeginner{tx: tx}, time.Now,
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, 91, 3, "body")
	if result != (EditResult{}) || !errors.Is(err, ErrPostEditConflict) || tx.updateCalls != 0 || tx.committed || !tx.rolledBack {
		t.Fatalf("conflicting edit = (%+v, %v, tx %+v)", result, err, tx)
	}
}

func TestEditPostRejectsInvalidBoundaryBeforeTransaction(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{name: "nil context", run: func() error {
			_, err := EditPost(nil, panicPublishBeginner{}, time.Now, actor, 1, 1, "body")
			return err
		}},
		{name: "nil beginner", run: func() error { _, err := EditPost(context.Background(), nil, time.Now, actor, 1, 1, "body"); return err }},
		{name: "nil clock", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, nil, actor, 1, 1, "body")
			return err
		}},
		{name: "invalid actor", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, time.Now, policy.AccessContext{}, 1, 1, "body")
			return err
		}},
		{name: "invalid post", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, time.Now, actor, 0, 1, "body")
			return err
		}},
		{name: "invalid revision", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, time.Now, actor, 1, 0, "body")
			return err
		}},
		{name: "exhausted revision", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, time.Now, actor, 1, maximumPostRevision, "body")
			return err
		}},
		{name: "canceled", run: func() error {
			_, err := EditPost(canceled, panicPublishBeginner{}, time.Now, actor, 1, 1, "body")
			return err
		}},
		{name: "invalid body", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, time.Now, actor, 1, 1, strings.Repeat("x", render.MaximumMarkdownBytes+1))
			return err
		}},
		{name: "zero clock", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, func() time.Time { return time.Time{} }, actor, 1, 1, "body")
			return err
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.run(); err == nil {
				t.Fatal("EditPost accepted invalid boundary")
			}
		})
	}
}

func TestEditPostFailsClosedAtTransactionStages(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, failure := range []string{"begin", "lock", "invalid-lock", "groups", "update", "invalid-update", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &editTestTx{postID: 91, authorID: 11, revision: 3, topicID: 41, postNumber: 2, areaID: 7, visibility: "public", postingMode: "normal", failure: failure}
			beginner := editTestBeginner{tx: tx}
			if failure == "begin" {
				beginner.err = errPublishTest
			}
			result, err := EditPost(context.Background(), beginner, time.Now, actor, 91, 3, "body")
			if err == nil || result != (EditResult{}) || tx.committed || failure != "begin" && !tx.rolledBack {
				t.Fatalf("EditPost(%q) = (%+v, %v), transaction %+v", failure, result, err, tx)
			}
		})
	}
}

type editTestBeginner struct {
	tx  *editTestTx
	err error
}

func (beginner editTestBeginner) Begin(context.Context) (pgx.Tx, error) {
	if beginner.err != nil {
		return nil, beginner.err
	}
	return beginner.tx, nil
}

type editTestTx struct {
	pgx.Tx
	postID, authorID, topicID, areaID int64
	revision, postNumber              int32
	visibility, postingMode           string
	topicState                        string
	groupIDs                          []int64
	markdown, renderedHTML            string
	rendererVersion                   string
	atTime                            time.Time
	expectedRevision                  int32
	updateCalls                       int
	deleteCalls                       int
	deletedBy                         int64
	failure                           string
	committed, rolledBack             bool
}

func (tx *editTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	switch {
	case strings.Contains(query, "LockPostForEdit"):
		topicState := tx.topicState
		if topicState == "" {
			topicState = "open"
		}
		if tx.failure == "lock" {
			return publishTestRow{err: errPublishTest}
		}
		if tx.failure == "invalid-lock" {
			return publishTestRow{values: []any{int64(0), tx.authorID, tx.revision, tx.topicID, tx.postNumber, topicState, tx.areaID, tx.visibility, tx.postingMode}}
		}
		return publishTestRow{values: []any{tx.postID, tx.authorID, tx.revision, tx.topicID, tx.postNumber, topicState, tx.areaID, tx.visibility, tx.postingMode}}
	case strings.Contains(query, "UpdatePostRevision"):
		if tx.failure == "update" {
			return publishTestRow{err: errPublishTest}
		}
		tx.updateCalls++
		tx.markdown, tx.renderedHTML, tx.rendererVersion = arguments[0].(string), arguments[1].(string), arguments[2].(string)
		tx.atTime = arguments[3].(pgtype.Timestamptz).Time
		tx.expectedRevision = arguments[5].(int32)
		if tx.failure == "invalid-update" {
			return publishTestRow{values: []any{tx.postID, tx.topicID, tx.postNumber, tx.revision + 2, int64(tx.postNumber)}}
		}
		return publishTestRow{values: []any{tx.postID, tx.topicID, tx.postNumber, tx.revision + 1, int64(tx.postNumber)}}
	case strings.Contains(query, "SoftDeletePost"):
		if tx.failure == "delete" {
			return publishTestRow{err: errPublishTest}
		}
		tx.deleteCalls++
		tx.atTime = arguments[0].(pgtype.Timestamptz).Time
		tx.deletedBy = arguments[1].(int64)
		tx.expectedRevision = arguments[3].(int32)
		if tx.failure == "invalid-delete" {
			return publishTestRow{values: []any{tx.postID, tx.topicID, tx.postNumber, tx.revision + 1}}
		}
		return publishTestRow{values: []any{tx.postID, tx.topicID, tx.postNumber, tx.revision}}
	default:
		panic("unexpected edit query")
	}
}

func (tx *editTestTx) Query(_ context.Context, query string, arguments ...any) (pgx.Rows, error) {
	if !strings.Contains(query, "LockAreaGroupIDs") || arguments[0].(int64) != tx.areaID {
		panic("unexpected edit rows query")
	}
	if tx.failure == "groups" {
		return nil, errPublishTest
	}
	values := make([][]any, len(tx.groupIDs))
	for index, groupID := range tx.groupIDs {
		values[index] = []any{groupID}
	}
	return &publishTestRows{values: values}, nil
}

func (tx *editTestTx) Commit(context.Context) error {
	if tx.failure == "commit" {
		return errPublishTest
	}
	tx.committed = true
	return nil
}
func (tx *editTestTx) Rollback(context.Context) error { tx.rolledBack = true; return nil }
