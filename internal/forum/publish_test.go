package forum

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

var testPublicationPolicy = func() abuse.PublicationPolicy {
	policy, err := abuse.NewPublicationPolicy(100_000, 100_000, 10*time.Minute, 24*time.Hour)
	if err != nil {
		panic(err)
	}
	return policy
}()

var testDestinationPolicy = abuse.NewEmptyDestinationPolicy()

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
	tx := &publishTestTx{areaID: 7, visibility: "groups", postingMode: "normal", groupIDs: []int64{4, 9}, topicID: 101, postID: 201, postNumber: 1, databaseNow: at}
	result, err := CreateTopic(context.Background(), publishTestBeginner{tx: tx}, testPublicationPolicy, testDestinationPolicy,
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, GroupIDs: []int64{9}},
		"member-news", "A careful Cafe\u0301", "Hello **world**")
	if err != nil || result != (PublishResult{TopicID: 101, PostID: 201, PostNumber: 1, NodeOrdinal: 1}) {
		t.Fatalf("CreateTopic() = (%+v, %v)", result, err)
	}
	if !tx.committed || tx.rolledBack || tx.createdTopic != 1 || tx.createdReply != 0 || tx.beginOptions != (pgx.TxOptions{IsoLevel: pgx.ReadCommitted}) {
		t.Fatalf("transaction = (commit %t rollback %t topic %d reply %d)", tx.committed, tx.rolledBack, tx.createdTopic, tx.createdReply)
	}
	wantSteps := []string{"configure", "lock-actor", "actor-groups", "lock-area", "area-groups", "database-time", "replace-window", "create-topic", "commit"}
	if strings.Join(tx.steps, ",") != strings.Join(wantSteps, ",") {
		t.Fatalf("topic transaction order = %v, want %v", tx.steps, wantSteps)
	}
	if tx.authorID != 11 || tx.areaIDArgument != 7 || tx.title != "A careful Cafe\u0301" || tx.markdown != "Hello **world**" ||
		tx.rendererVersion != render.RendererVersion || tx.renderedHTML != "<p>Hello <strong>world</strong></p>\n" ||
		tx.topicSearchText != "A careful Café" || tx.postSearchText != "Hello world" || tx.searchProjectionVersion != render.SearchProjectionVersion ||
		!tx.atTime.Equal(at.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("persisted topic = %+v", tx)
	}
}

func TestCreateReplyCommitsAuthorizedOrderedPost(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 2, 4, 31, 0, 999, time.FixedZone("offset", -5*60*60))
	tx := &publishTestTx{areaID: 7, visibility: "public", postingMode: "read_only", topicID: 101, topicState: "locked", postID: 202, postNumber: 2, actorRole: "moderator", databaseNow: at}
	result, err := CreateReply(context.Background(), publishTestBeginner{tx: tx}, testPublicationPolicy, testDestinationPolicy,
		policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleModerator}, 101, 201, "A `reply`")
	if err != nil || result != (PublishResult{TopicID: 101, PostID: 202, PostNumber: 2, NodeOrdinal: 2}) {
		t.Fatalf("CreateReply() = (%+v, %v)", result, err)
	}
	if !tx.committed || tx.rolledBack || tx.createdTopic != 0 || tx.createdReply != 1 || tx.beginOptions != (pgx.TxOptions{IsoLevel: pgx.ReadCommitted}) || tx.topicIDArgument != 101 || tx.authorID != 12 ||
		tx.parentPostID != 201 ||
		tx.markdown != "A `reply`" || tx.renderedHTML != "<p>A <code>reply</code></p>\n" || tx.rendererVersion != render.RendererVersion ||
		tx.postSearchText != "A reply" || tx.searchProjectionVersion != render.SearchProjectionVersion ||
		!tx.atTime.Equal(at.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("persisted reply = %+v", tx)
	}
	wantSteps := []string{"configure", "lock-actor", "actor-groups", "lock-topic", "area-groups", "database-time", "replace-window", "create-reply", "commit"}
	if strings.Join(tx.steps, ",") != strings.Join(wantSteps, ",") {
		t.Fatalf("reply transaction order = %v, want %v", tx.steps, wantSteps)
	}
}

func TestPublicationLimitRollsBackWithoutTargetWrite(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
	started := now.Add(-time.Minute)
	tx := &publishTestTx{
		areaID: 7, visibility: "public", postingMode: "normal", topicID: 101, postID: 201, postNumber: 1,
		databaseNow: now, publicationStarted: pgtype.Timestamptz{Time: started, Valid: true}, publicationCount: 100_000,
	}
	result, err := CreateTopic(context.Background(), publishTestBeginner{tx: tx}, testPublicationPolicy, testDestinationPolicy,
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, "news", "Limited", "body")
	var limited PublicationRateLimitError
	if result != (PublishResult{}) || !errors.Is(err, ErrPublicationRateLimited) || !errors.As(err, &limited) ||
		limited.RetryAfterSeconds <= 0 || limited.RetryAfterSeconds > 600 || tx.createdTopic != 0 || tx.committed || !tx.rolledBack {
		t.Fatalf("limited topic = (%+v, %v, retry %d, transaction %+v)", result, err, limited.RetryAfterSeconds, tx)
	}
	if strings.Contains(strings.Join(tx.steps, ","), "replace-window") || strings.Contains(strings.Join(tx.steps, ","), "create-topic") {
		t.Fatalf("limited transaction performed a write: %v", tx.steps)
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
			_, err := CreateTopic(context.Background(), publishTestBeginner{tx: tx}, testPublicationPolicy, testDestinationPolicy, member, "news", "Title", "body")
			return err
		}},
		{name: "reply locked", run: func(tx *publishTestTx) error {
			_, err := CreateReply(context.Background(), publishTestBeginner{tx: tx}, testPublicationPolicy, testDestinationPolicy, member, 101, 201, "body")
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
			_, err := CreateTopic(nil, panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, "news", "Title", "body")
			return err
		}},
		{name: "invalid actor", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, policy.AccessContext{}, "news", "Title", "body")
			return err
		}},
		{name: "invalid slug", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, "News", "Title", "body")
			return err
		}},
		{name: "invalid title", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, "news", " \n", "body")
			return err
		}},
		{name: "invalid topic body", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, "news", "Title", "<script>x</script>")
			return err
		}},
		{name: "invalid reply ID", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, 0, 1, "body")
			return err
		}},
		{name: "invalid reply parent", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, 1, 0, "body")
			return err
		}},
		{name: "invalid reply body", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, 1, 1, strings.Repeat("x", render.MaximumMarkdownBytes+1))
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
			_, err := CreateTopic(context.Background(), nil, testPublicationPolicy, testDestinationPolicy, actor, "news", "Title", "body")
			return err
		}},
		{name: "invalid topic publication policy", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, abuse.PublicationPolicy{}, testDestinationPolicy, actor, "news", "Title", "body")
			return err
		}},
		{name: "canceled topic", run: func() error {
			_, err := CreateTopic(canceled, panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, "news", "Title", "body")
			return err
		}},
		{name: "nil reply context", run: func() error {
			_, err := CreateReply(nil, panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, 1, 1, "body")
			return err
		}},
		{name: "nil reply beginner", run: func() error {
			_, err := CreateReply(context.Background(), nil, testPublicationPolicy, testDestinationPolicy, actor, 1, 1, "body")
			return err
		}},
		{name: "invalid reply publication policy", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, abuse.PublicationPolicy{}, testDestinationPolicy, actor, 1, 1, "body")
			return err
		}},
		{name: "invalid reply actor", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, policy.AccessContext{}, 1, 1, "body")
			return err
		}},
		{name: "canceled reply", run: func() error {
			_, err := CreateReply(canceled, panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, 1, 1, "body")
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
	_, fieldErr := CreateTopic(canceled, panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, "bad area", "Title", "body")
	var invalid InvalidPublishingInput
	if !errors.As(fieldErr, &invalid) || invalid.Field != "area" {
		t.Fatalf("invalid field before cancellation = (%v, %+v)", fieldErr, invalid)
	}
	_, cancellationErr := CreateTopic(canceled, panicPublishBeginner{}, testPublicationPolicy, testDestinationPolicy, actor, "news", "Title", " ")
	if !errors.Is(cancellationErr, context.Canceled) || errors.As(cancellationErr, &invalid) {
		t.Fatalf("cancellation before Markdown render = %v", cancellationErr)
	}
}

func TestCreateTopicFailsClosedAtTransactionStages(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, failure := range []string{"begin", "configure", "lock-actor", "missing-actor", "invalid-actor", "changed-role", "malformed-suspension", "malformed-suspension-order", "malformed-mute", "observed-before-created", "actor-groups", "lock-area", "invalid-area", "groups", "database-time", "replace-window", "create-topic", "invalid-topic", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &publishTestTx{failure: failure, areaID: 7, visibility: "public", postingMode: "normal", topicID: 101, postID: 201, postNumber: 1}
			beginner := publishTestBeginner{tx: tx}
			if failure == "begin" {
				beginner.err = errPublishTest
			}
			result, err := CreateTopic(context.Background(), beginner, testPublicationPolicy, testDestinationPolicy, actor, "news", "Title", "body")
			if err == nil || result != (PublishResult{}) || tx.committed || failure != "begin" && !tx.rolledBack {
				t.Fatalf("CreateTopic(%q) = (%+v, %v), transaction %+v", failure, result, err, tx)
			}
			if failure == "missing-actor" && !errors.Is(err, ErrPublishingDenied) {
				t.Fatalf("missing actor error = %v, want publishing denied", err)
			}
			if failure == "commit" && (!strings.Contains(err.Error(), "outcome unknown") || tx.createdTopic != 1 || tx.publicationCount != 1) {
				t.Fatalf("unknown commit evidence = (error %v, topic writes %d, publication count %d)", err, tx.createdTopic, tx.publicationCount)
			}
		})
	}
}

func TestCreateReplyFailsClosedAtTransactionStages(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	for _, failure := range []string{"begin", "configure", "lock-actor", "invalid-actor", "actor-groups", "lock-topic", "invalid-topic-lock", "groups", "database-time", "replace-window", "create-reply", "invalid-reply", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &publishTestTx{failure: failure, areaID: 7, visibility: "public", postingMode: "normal", topicID: 101, topicState: "open", postID: 202, postNumber: 2}
			beginner := publishTestBeginner{tx: tx}
			if failure == "begin" {
				beginner.err = errPublishTest
			}
			result, err := CreateReply(context.Background(), beginner, testPublicationPolicy, testDestinationPolicy, actor, 101, 201, "body")
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
		context.Background(), publishTestBeginner{tx: tx}, testPublicationPolicy, testDestinationPolicy,
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

func (panicPublishBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	panic("transaction must not begin")
}

var errPublishTest = errors.New("forced publishing failure")

type publishTestBeginner struct {
	tx  *publishTestTx
	err error
}

func (beginner publishTestBeginner) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if beginner.err != nil {
		return nil, beginner.err
	}
	beginner.tx.beginOptions = options
	return beginner.tx, nil
}

type publishTestTx struct {
	pgx.Tx
	areaID, areaIDArgument, topicID, topicIDArgument, postID, parentPostID, authorID int64
	visibility, postingMode, topicState, title, markdown                             string
	renderedHTML, rendererVersion, topicSearchText, postSearchText                   string
	searchProjectionVersion                                                          string
	groupIDs                                                                         []int64
	postNumber                                                                       int32
	parentDepth                                                                      int32
	atTime                                                                           time.Time
	databaseNow                                                                      time.Time
	actorRole                                                                        string
	publicationStarted                                                               pgtype.Timestamptz
	publicationCount                                                                 int32
	steps                                                                            []string
	failure                                                                          string
	createdTopic, createdReply                                                       int
	committed, rolledBack                                                            bool
	beginOptions                                                                     pgx.TxOptions
}

func (tx *publishTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	switch {
	case strings.Contains(query, "LockPublicationActor"):
		tx.steps = append(tx.steps, "lock-actor")
		if tx.failure == "lock-actor" {
			return publishTestRow{err: errPublishTest}
		}
		if tx.failure == "missing-actor" {
			return publishTestRow{err: pgx.ErrNoRows}
		}
		now := tx.databaseNow
		if now.IsZero() {
			now = time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
		}
		role := tx.actorRole
		if role == "" {
			role = "member"
		}
		if tx.failure == "changed-role" {
			role = "moderator"
		}
		actorID := arguments[0].(int64)
		if tx.failure == "invalid-actor" {
			actorID++
		}
		createdAt := pgtype.Timestamptz{Time: now.Add(-48 * time.Hour), Valid: true}
		suspendedAt, suspendedUntil, mutedUntil := pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{}
		switch tx.failure {
		case "malformed-suspension":
			suspendedUntil = pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}
		case "malformed-suspension-order":
			suspendedAt = pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}
			suspendedUntil = suspendedAt
		case "malformed-mute":
			mutedUntil = createdAt
		case "observed-before-created":
			createdAt = pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}
		}
		return publishTestRow{values: []any{
			actorID, role, suspendedAt, suspendedUntil, mutedUntil,
			createdAt, tx.publicationStarted, tx.publicationCount,
			pgtype.Timestamptz{Time: now, Valid: true},
		}}
	case strings.Contains(query, "LockAreaForTopicCreation"):
		tx.steps = append(tx.steps, "lock-area")
		if tx.failure == "lock-area" {
			return publishTestRow{err: errPublishTest}
		}
		if tx.failure == "invalid-area" {
			return publishTestRow{values: []any{int64(0), tx.visibility, tx.postingMode}}
		}
		return publishTestRow{values: []any{tx.areaID, tx.visibility, tx.postingMode}}
	case strings.Contains(query, "LockTopicForReply"):
		tx.steps = append(tx.steps, "lock-topic")
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
		tx.steps = append(tx.steps, "create-topic")
		if tx.failure == "create-topic" {
			return publishTestRow{err: errPublishTest}
		}
		tx.createdTopic++
		tx.areaIDArgument, tx.authorID, tx.title = arguments[0].(int64), arguments[1].(int64), arguments[2].(string)
		tx.atTime = arguments[3].(pgtype.Timestamptz).Time
		tx.topicSearchText = arguments[4].(string)
		tx.searchProjectionVersion = arguments[5].(pgtype.Text).String
		tx.markdown, tx.renderedHTML, tx.rendererVersion = arguments[6].(string), arguments[7].(string), arguments[8].(string)
		tx.postSearchText = arguments[9].(string)
		if tx.failure == "invalid-topic" {
			return publishTestRow{values: []any{int64(0), tx.postID, tx.postNumber, int64(1)}}
		}
		return publishTestRow{values: []any{tx.topicID, tx.postID, tx.postNumber, int64(1)}}
	case strings.Contains(query, "PublicationDatabaseTime"):
		tx.steps = append(tx.steps, "database-time")
		if tx.failure == "database-time" {
			return publishTestRow{err: errPublishTest}
		}
		now := tx.databaseNow
		if now.IsZero() {
			now = time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
		}
		return publishTestRow{values: []any{pgtype.Timestamptz{Time: now, Valid: true}}}
	case strings.Contains(query, "ReplacePublicationWindow"):
		tx.steps = append(tx.steps, "replace-window")
		if tx.failure == "replace-window" {
			return publishTestRow{err: errPublishTest}
		}
		started := arguments[0].(pgtype.Timestamptz)
		tx.publicationCount = arguments[1].(int32)
		return publishTestRow{values: []any{started, tx.publicationCount}}
	case strings.Contains(query, "CreateReplyAndAdvanceTopic"):
		tx.steps = append(tx.steps, "create-reply")
		if tx.failure == "create-reply" {
			return publishTestRow{err: errPublishTest}
		}
		tx.createdReply++
		tx.authorID = arguments[0].(int64)
		tx.parentPostID = arguments[4].(pgtype.Int8).Int64
		tx.atTime = arguments[5].(pgtype.Timestamptz).Time
		tx.markdown, tx.renderedHTML, tx.rendererVersion = arguments[1].(string), arguments[2].(string), arguments[3].(string)
		tx.postSearchText = arguments[6].(string)
		tx.searchProjectionVersion = arguments[7].(pgtype.Text).String
		tx.topicIDArgument = arguments[8].(int64)
		if tx.failure == "invalid-reply" {
			return publishTestRow{values: []any{tx.topicID, int64(0), tx.postNumber, int64(tx.postNumber)}}
		}
		return publishTestRow{values: []any{tx.topicID, tx.postID, tx.postNumber, int64(tx.postNumber)}}
	default:
		panic("unexpected publishing query")
	}
}

func (tx *publishTestTx) Query(_ context.Context, query string, arguments ...any) (pgx.Rows, error) {
	if strings.Contains(query, "ListLockedPublicationActorGroupIDs") {
		tx.steps = append(tx.steps, "actor-groups")
		if tx.failure == "actor-groups" {
			return nil, errPublishTest
		}
	} else if !strings.Contains(query, "LockAreaGroupIDs") || arguments[0].(int64) != tx.areaID {
		panic("unexpected publishing rows query")
	} else if tx.failure == "groups" {
		return nil, errPublishTest
	} else {
		tx.steps = append(tx.steps, "area-groups")
	}
	values := make([][]any, len(tx.groupIDs))
	for index, groupID := range tx.groupIDs {
		values[index] = []any{groupID}
	}
	return &publishTestRows{values: values}, nil
}

func (tx *publishTestTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "ConfigurePublicationTransaction") {
		panic("unexpected publishing exec")
	}
	tx.steps = append(tx.steps, "configure")
	if tx.failure == "configure" {
		return pgconn.CommandTag{}, errPublishTest
	}
	return pgconn.CommandTag{}, nil
}

func (tx *publishTestTx) Commit(context.Context) error {
	tx.steps = append(tx.steps, "commit")
	if tx.failure == "commit" {
		return errPublishTest
	}
	tx.committed = true
	return nil
}
func (tx *publishTestTx) Rollback(context.Context) error {
	tx.steps = append(tx.steps, "rollback")
	tx.rolledBack = true
	return nil
}

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
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
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
