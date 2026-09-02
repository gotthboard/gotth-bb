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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestInvalidPublishingInputExposesOnlyStableClassAndField(t *testing.T) {
	t.Parallel()

	err := InvalidPublishingInput{Field: "markdown"}
	if err.Error() != "invalid forum publishing markdown" || !errors.Is(err, ErrInvalidPublishingInput) {
		t.Fatalf("InvalidPublishingInput = (%q, class %t)", err.Error(), errors.Is(err, ErrInvalidPublishingInput))
	}
}

func TestCreateTopicCommitsAuthorizedRenderedFirstPost(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 2, 4, 30, 0, 123456789, time.UTC)
	tx := &publishTestTx{areaID: 7, visibility: "groups", postingMode: "normal", groupIDs: []int64{4, 9}, topicID: 101, postID: 201, postNumber: 1}
	result, err := CreateTopic(context.Background(), publishTestBeginner{tx: tx}, func() time.Time { return at },
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, GroupIDs: []int64{9}},
		"member-news", "A careful title", "Hello **world**")
	if err != nil || result != (PublishResult{TopicID: 101, PostID: 201, PostNumber: 1}) {
		t.Fatalf("CreateTopic() = (%+v, %v)", result, err)
	}
	if !tx.committed || tx.rolledBack || tx.createdTopic != 1 || tx.createdReply != 0 {
		t.Fatalf("transaction = (commit %t rollback %t topic %d reply %d)", tx.committed, tx.rolledBack, tx.createdTopic, tx.createdReply)
	}
	if tx.authorID != 11 || tx.areaIDArgument != 7 || tx.title != "A careful title" || tx.markdown != "Hello **world**" ||
		tx.rendererVersion != render.RendererVersion || tx.renderedHTML != "<p>Hello <strong>world</strong></p>\n" ||
		!tx.atTime.Equal(at.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("persisted topic = %+v", tx)
	}
}

func TestCreateReplyCommitsAuthorizedOrderedPost(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 2, 4, 31, 0, 999, time.FixedZone("offset", -5*60*60))
	tx := &publishTestTx{areaID: 7, visibility: "public", postingMode: "read_only", topicID: 101, topicState: "locked", postID: 202, postNumber: 2}
	result, err := CreateReply(context.Background(), publishTestBeginner{tx: tx}, func() time.Time { return at },
		policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleModerator}, 101, 201, "A `reply`")
	if err != nil || result != (PublishResult{TopicID: 101, PostID: 202, PostNumber: 2}) {
		t.Fatalf("CreateReply() = (%+v, %v)", result, err)
	}
	if !tx.committed || tx.rolledBack || tx.createdTopic != 0 || tx.createdReply != 1 || tx.topicIDArgument != 101 || tx.authorID != 12 ||
		tx.parentPostID != 201 ||
		tx.markdown != "A `reply`" || tx.renderedHTML != "<p>A <code>reply</code></p>\n" || tx.rendererVersion != render.RendererVersion ||
		!tx.atTime.Equal(at.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("persisted reply = %+v", tx)
	}
}

func TestPublishingDenialRollsBackBeforeInsert(t *testing.T) {
	t.Parallel()

	member := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, test := range []struct {
		name string
		run  func(*publishTestTx) error
	}{
		{name: "topic read only", run: func(tx *publishTestTx) error {
			_, err := CreateTopic(context.Background(), publishTestBeginner{tx: tx}, time.Now, member, "news", "Title", "body")
			return err
		}},
		{name: "reply locked", run: func(tx *publishTestTx) error {
			_, err := CreateReply(context.Background(), publishTestBeginner{tx: tx}, time.Now, member, 101, 201, "body")
			return err
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &publishTestTx{areaID: 7, visibility: "public", postingMode: "read_only", topicID: 101, topicState: "locked"}
			err := test.run(tx)
			if !errors.Is(err, ErrPublishingDenied) || tx.committed || !tx.rolledBack || tx.createdTopic != 0 || tx.createdReply != 0 {
				t.Fatalf("denied transaction = (error %v commit %t rollback %t writes %d/%d)", err, tx.committed, tx.rolledBack, tx.createdTopic, tx.createdReply)
			}
		})
	}
}

func TestPublishingRejectsInvalidInputBeforeTransaction(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{name: "nil topic context", run: func() error {
			_, err := CreateTopic(nil, panicPublishBeginner{}, time.Now, actor, "news", "Title", "body")
			return err
		}},
		{name: "invalid actor", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, time.Now, policy.AccessContext{}, "news", "Title", "body")
			return err
		}},
		{name: "invalid slug", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, time.Now, actor, "News", "Title", "body")
			return err
		}},
		{name: "invalid title", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, time.Now, actor, "news", " \n", "body")
			return err
		}},
		{name: "invalid topic body", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, time.Now, actor, "news", "Title", "<script>x</script>")
			return err
		}},
		{name: "invalid reply ID", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, time.Now, actor, 0, 1, "body")
			return err
		}},
		{name: "invalid reply parent", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, time.Now, actor, 1, 0, "body")
			return err
		}},
		{name: "invalid reply body", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, time.Now, actor, 1, 1, strings.Repeat("x", render.MaximumMarkdownBytes+1))
			return err
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.run(); err == nil {
				t.Fatal("publishing accepted invalid input")
			}
		})
	}
}

func TestPublishingRejectsInvalidConfigurationCancellationAndClock(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{name: "nil topic beginner", run: func() error {
			_, err := CreateTopic(context.Background(), nil, time.Now, actor, "news", "Title", "body")
			return err
		}},
		{name: "nil topic clock", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, nil, actor, "news", "Title", "body")
			return err
		}},
		{name: "canceled topic", run: func() error {
			_, err := CreateTopic(canceled, panicPublishBeginner{}, time.Now, actor, "news", "Title", "body")
			return err
		}},
		{name: "zero topic clock", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, func() time.Time { return time.Time{} }, actor, "news", "Title", "body")
			return err
		}},
		{name: "nil reply context", run: func() error {
			_, err := CreateReply(nil, panicPublishBeginner{}, time.Now, actor, 1, 1, "body")
			return err
		}},
		{name: "nil reply beginner", run: func() error {
			_, err := CreateReply(context.Background(), nil, time.Now, actor, 1, 1, "body")
			return err
		}},
		{name: "nil reply clock", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, nil, actor, 1, 1, "body")
			return err
		}},
		{name: "invalid reply actor", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, time.Now, policy.AccessContext{}, 1, 1, "body")
			return err
		}},
		{name: "canceled reply", run: func() error {
			_, err := CreateReply(canceled, panicPublishBeginner{}, time.Now, actor, 1, 1, "body")
			return err
		}},
		{name: "zero reply clock", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, func() time.Time { return time.Time{} }, actor, 1, 1, "body")
			return err
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.run(); err == nil {
				t.Fatal("publishing accepted invalid boundary state")
			}
		})
	}
}

func TestCreateTopicPreservesFieldAndCancellationOrdering(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, fieldErr := CreateTopic(canceled, panicPublishBeginner{}, time.Now, actor, "bad area", "Title", "body")
	var invalid InvalidPublishingInput
	if !errors.As(fieldErr, &invalid) || invalid.Field != "area" {
		t.Fatalf("invalid field before cancellation = (%v, %+v)", fieldErr, invalid)
	}
	_, cancellationErr := CreateTopic(canceled, panicPublishBeginner{}, time.Now, actor, "news", "Title", " ")
	if !errors.Is(cancellationErr, context.Canceled) || errors.As(cancellationErr, &invalid) {
		t.Fatalf("cancellation before Markdown render = %v", cancellationErr)
	}
}

func TestCreateTopicFailsClosedAtTransactionStages(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, failure := range []string{"begin", "lock-area", "invalid-area", "groups", "create-topic", "invalid-topic", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &publishTestTx{failure: failure, areaID: 7, visibility: "public", postingMode: "normal", topicID: 101, postID: 201, postNumber: 1}
			beginner := publishTestBeginner{tx: tx}
			if failure == "begin" {
				beginner.err = errPublishTest
			}
			result, err := CreateTopic(context.Background(), beginner, time.Now, actor, "news", "Title", "body")
			if err == nil || result != (PublishResult{}) || tx.committed || failure != "begin" && !tx.rolledBack {
				t.Fatalf("CreateTopic(%q) = (%+v, %v), transaction %+v", failure, result, err, tx)
			}
		})
	}
}

func TestCreateReplyFailsClosedAtTransactionStages(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, failure := range []string{"begin", "lock-topic", "invalid-topic-lock", "groups", "create-reply", "invalid-reply", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &publishTestTx{failure: failure, areaID: 7, visibility: "public", postingMode: "normal", topicID: 101, topicState: "open", postID: 202, postNumber: 2}
			beginner := publishTestBeginner{tx: tx}
			if failure == "begin" {
				beginner.err = errPublishTest
			}
			result, err := CreateReply(context.Background(), beginner, time.Now, actor, 101, 201, "body")
			if err == nil || result != (PublishResult{}) || tx.committed || failure != "begin" && !tx.rolledBack {
				t.Fatalf("CreateReply(%q) = (%+v, %v), transaction %+v", failure, result, err, tx)
			}
		})
	}
}

func TestCreateReplyRejectsMaximumDepthBeforeInsert(t *testing.T) {
	t.Parallel()

	tx := &publishTestTx{
		areaID: 7, visibility: "public", postingMode: "normal", topicID: 101,
		topicState: "open", parentDepth: MaximumReplyDepth,
	}
	result, err := CreateReply(
		context.Background(), publishTestBeginner{tx: tx}, time.Now,
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember},
		101, 201, "body",
	)
	if err == nil || result != (PublishResult{}) || tx.createdReply != 0 || tx.committed || !tx.rolledBack {
		t.Fatalf("maximum-depth reply = (%+v, %v), transaction %+v", result, err, tx)
	}
}

type panicPublishBeginner struct{}

func (panicPublishBeginner) Begin(context.Context) (pgx.Tx, error) {
	panic("transaction must not begin")
}

var errPublishTest = errors.New("forced publishing failure")

type publishTestBeginner struct {
	tx  *publishTestTx
	err error
}

func (beginner publishTestBeginner) Begin(context.Context) (pgx.Tx, error) {
	if beginner.err != nil {
		return nil, beginner.err
	}
	return beginner.tx, nil
}

type publishTestTx struct {
	pgx.Tx
	areaID, areaIDArgument, topicID, topicIDArgument, postID, parentPostID, authorID int64
	visibility, postingMode, topicState, title, markdown                             string
	renderedHTML, rendererVersion                                                    string
	groupIDs                                                                         []int64
	postNumber                                                                       int32
	parentDepth                                                                      int32
	atTime                                                                           time.Time
	failure                                                                          string
	createdTopic, createdReply                                                       int
	committed, rolledBack                                                            bool
}

func (tx *publishTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	switch {
	case strings.Contains(query, "LockAreaForTopicCreation"):
		if tx.failure == "lock-area" {
			return publishTestRow{err: errPublishTest}
		}
		if tx.failure == "invalid-area" {
			return publishTestRow{values: []any{int64(0), tx.visibility, tx.postingMode}}
		}
		return publishTestRow{values: []any{tx.areaID, tx.visibility, tx.postingMode}}
	case strings.Contains(query, "LockTopicForReply"):
		if tx.failure == "lock-topic" {
			return publishTestRow{err: errPublishTest}
		}
		tx.parentPostID = arguments[0].(int64)
		tx.topicIDArgument = arguments[1].(int64)
		if tx.failure == "invalid-topic-lock" {
			return publishTestRow{values: []any{tx.topicID + 1, tx.topicState, tx.areaID, tx.visibility, tx.postingMode, tx.parentPostID, int32(1)}}
		}
		depth := tx.parentDepth
		if depth == 0 {
			depth = 1
		}
		return publishTestRow{values: []any{tx.topicID, tx.topicState, tx.areaID, tx.visibility, tx.postingMode, tx.parentPostID, depth}}
	case strings.Contains(query, "CreateTopicAndFirstPost"):
		if tx.failure == "create-topic" {
			return publishTestRow{err: errPublishTest}
		}
		tx.createdTopic++
		tx.areaIDArgument, tx.authorID, tx.title = arguments[0].(int64), arguments[1].(int64), arguments[2].(string)
		tx.captureBody(arguments[3:])
		if tx.failure == "invalid-topic" {
			return publishTestRow{values: []any{int64(0), tx.postID, tx.postNumber}}
		}
		return publishTestRow{values: []any{tx.topicID, tx.postID, tx.postNumber}}
	case strings.Contains(query, "CreateReplyAndAdvanceTopic"):
		if tx.failure == "create-reply" {
			return publishTestRow{err: errPublishTest}
		}
		tx.createdReply++
		tx.authorID = arguments[0].(int64)
		tx.parentPostID = arguments[4].(pgtype.Int8).Int64
		tx.captureBody([]any{arguments[5], arguments[1], arguments[2], arguments[3]})
		tx.topicIDArgument = arguments[6].(int64)
		if tx.failure == "invalid-reply" {
			return publishTestRow{values: []any{tx.topicID, int64(0), tx.postNumber}}
		}
		return publishTestRow{values: []any{tx.topicID, tx.postID, tx.postNumber}}
	default:
		panic("unexpected publishing query")
	}
}

func (tx *publishTestTx) Query(_ context.Context, query string, arguments ...any) (pgx.Rows, error) {
	if !strings.Contains(query, "LockAreaGroupIDs") || arguments[0].(int64) != tx.areaID {
		panic("unexpected publishing rows query")
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

func (tx *publishTestTx) captureBody(arguments []any) {
	tx.atTime = arguments[0].(pgtype.Timestamptz).Time
	tx.markdown, tx.renderedHTML, tx.rendererVersion = arguments[1].(string), arguments[2].(string), arguments[3].(string)
}

func (tx *publishTestTx) Commit(context.Context) error {
	if tx.failure == "commit" {
		return errPublishTest
	}
	tx.committed = true
	return nil
}
func (tx *publishTestTx) Rollback(context.Context) error { tx.rolledBack = true; return nil }

type publishTestRow struct {
	values []any
	err    error
}

func (row publishTestRow) Scan(destinations ...any) error {
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
		default:
			panic("unexpected publishing scan destination")
		}
	}
	return nil
}

type publishTestRows struct {
	pgx.Rows
	values [][]any
	index  int
}

func (rows *publishTestRows) Close()                        {}
func (rows *publishTestRows) Err() error                    { return nil }
func (rows *publishTestRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (rows *publishTestRows) Next() bool                    { return rows.index < len(rows.values) }
func (rows *publishTestRows) Scan(destinations ...any) error {
	*(destinations[0].(*int64)) = rows.values[rows.index][0].(int64)
	rows.index++
	return nil
}
