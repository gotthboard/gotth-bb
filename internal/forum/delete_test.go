package forum

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
)

func TestDeletePostCommitsAuthorizedExpectedRevision(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 2, 7, 0, 0, 0, time.UTC)
	tx := &editTestTx{postID: 91, authorID: 11, revision: 3, topicID: 41, postNumber: 2, areaID: 7, visibility: "groups", postingMode: "archived", topicState: "archived", groupIDs: []int64{9}}
	result, err := DeletePost(context.Background(), editTestBeginner{tx: tx}, func() time.Time { return at },
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, GroupIDs: []int64{9}}, 91, 3)
	if err != nil || result != (DeleteResult{TopicID: 41, PostID: 91, PostNumber: 2, Revision: 3}) {
		t.Fatalf("DeletePost() = (%+v, %v)", result, err)
	}
	if !tx.committed || tx.rolledBack || tx.deleteCalls != 1 || tx.deletedBy != 11 || tx.expectedRevision != 3 || !tx.atTime.Equal(at) {
		t.Fatalf("delete transaction = %+v", tx)
	}
}

func TestDeletePostDeniesBeforeConflictDisclosureOrMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		actor      policy.AccessContext
		visibility string
		topicState string
		groupIDs   []int64
	}{
		{name: "foreign staff", actor: policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleAdministrator}, visibility: "public"},
		{name: "hidden area", actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, visibility: "hidden"},
		{name: "group miss", actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, GroupIDs: []int64{8}}, visibility: "groups", groupIDs: []int64{9}},
		{name: "hidden topic", actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, visibility: "public", topicState: "hidden"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &editTestTx{postID: 91, authorID: 11, revision: 3, topicID: 41, postNumber: 2, areaID: 7, visibility: test.visibility, postingMode: "normal", topicState: test.topicState, groupIDs: test.groupIDs}
			result, err := DeletePost(context.Background(), editTestBeginner{tx: tx}, time.Now, test.actor, 91, 1)
			if result != (DeleteResult{}) || !errors.Is(err, ErrPostDeleteDenied) || errors.Is(err, ErrPostDeleteConflict) || tx.deleteCalls != 0 || tx.committed || !tx.rolledBack {
				t.Fatalf("denied delete = (%+v, %v, tx %+v)", result, err, tx)
			}
		})
	}
}

func TestDeletePostReportsAuthorizedRevisionConflictWithoutMutation(t *testing.T) {
	t.Parallel()

	tx := &editTestTx{postID: 91, authorID: 11, revision: 4, topicID: 41, postNumber: 2, areaID: 7, visibility: "public", postingMode: "normal"}
	result, err := DeletePost(context.Background(), editTestBeginner{tx: tx}, time.Now,
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, 91, 3)
	if result != (DeleteResult{}) || !errors.Is(err, ErrPostDeleteConflict) || tx.deleteCalls != 0 || tx.committed || !tx.rolledBack {
		t.Fatalf("conflicting delete = (%+v, %v, tx %+v)", result, err, tx)
	}
}

func TestDeletePostRejectsInvalidBoundaryBeforeTransaction(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, run := range []func() error{
		func() error { _, err := DeletePost(nil, panicPublishBeginner{}, time.Now, actor, 1, 1); return err },
		func() error { _, err := DeletePost(context.Background(), nil, time.Now, actor, 1, 1); return err },
		func() error {
			_, err := DeletePost(context.Background(), panicPublishBeginner{}, nil, actor, 1, 1)
			return err
		},
		func() error {
			_, err := DeletePost(context.Background(), panicPublishBeginner{}, time.Now, policy.AccessContext{}, 1, 1)
			return err
		},
		func() error {
			_, err := DeletePost(context.Background(), panicPublishBeginner{}, time.Now, actor, 0, 1)
			return err
		},
		func() error {
			_, err := DeletePost(context.Background(), panicPublishBeginner{}, time.Now, actor, 1, 0)
			return err
		},
		func() error {
			_, err := DeletePost(canceled, panicPublishBeginner{}, time.Now, actor, 1, 1)
			return err
		},
		func() error {
			_, err := DeletePost(context.Background(), panicPublishBeginner{}, func() time.Time { return time.Time{} }, actor, 1, 1)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatal("DeletePost accepted invalid boundary")
		}
	}
}

func TestDeletePostFailsClosedAtTransactionStages(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, failure := range []string{"begin", "lock", "invalid-lock", "groups", "delete", "invalid-delete", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &editTestTx{postID: 91, authorID: 11, revision: 3, topicID: 41, postNumber: 2, areaID: 7, visibility: "public", postingMode: "normal", failure: failure}
			beginner := editTestBeginner{tx: tx}
			if failure == "begin" {
				beginner.err = errPublishTest
			}
			result, err := DeletePost(context.Background(), beginner, time.Now, actor, 91, 3)
			if err == nil || result != (DeleteResult{}) || tx.committed || failure != "begin" && !tx.rolledBack {
				t.Fatalf("DeletePost(%q) = (%+v, %v), transaction %+v", failure, result, err, tx)
			}
		})
	}
}
