//go:build integration

package forum

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/render"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const publishingTestDatabase = "gotth_bb_alpha1_publishing_test"

func TestPublishingTransactionsOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+publishingTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale publishing test database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+publishingTestDatabase); err != nil {
		t.Fatalf("create publishing test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+publishingTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = publishingTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}

	const concurrentReplies = 8
	connections := make([]*pgx.Conn, concurrentReplies)
	for index := range connections {
		connections[index], err = pgx.ConnectConfig(ctx, testConfig)
		if err != nil {
			t.Fatalf("connect publishing database %d: %v", index, err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}
	var ownerID, memberID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Owner', 'administrator') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Member') RETURNING id`).Scan(&memberID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	for _, area := range []struct{ slug, postingMode string }{{"normal", "normal"}, {"staff-only", "read_only"}} {
		if _, err := connections[0].Exec(ctx, `INSERT INTO public.areas (slug, name, posting_mode, created_by, updated_by) VALUES ($1, $1, $2, $3, $3)`, area.slug, area.postingMode, ownerID); err != nil {
			t.Fatalf("insert area %q: %v", area.slug, err)
		}
	}
	actor := policy.AccessContext{Authenticated: true, UserID: memberID, Role: policy.RoleMember}
	createdAt := time.Date(2026, time.September, 2, 4, 45, 0, 0, time.UTC)
	topic, err := CreateTopic(ctx, connections[0], func() time.Time { return createdAt }, actor, "normal", "Concurrent replies", "First **post**")
	if err != nil || topic.TopicID <= 0 || topic.PostID <= 0 || topic.PostNumber != 1 {
		t.Fatalf("CreateTopic() = (%+v, %v)", topic, err)
	}
	editable, err := store.GetEditablePost(ctx, db.New(connections[0]), topic.PostID, actor)
	if err != nil || editable != (store.EditablePost{PostID: topic.PostID, TopicID: topic.TopicID, PostNumber: 1, MarkdownSource: "First **post**", Revision: 1}) {
		t.Fatalf("GetEditablePost() = (%+v, %v)", editable, err)
	}
	foreignEditable, foreignEditableErr := store.GetEditablePost(ctx, db.New(connections[0]), topic.PostID,
		policy.AccessContext{Authenticated: true, UserID: ownerID, Role: policy.RoleAdministrator})
	if foreignEditable != (store.EditablePost{}) || !errors.Is(foreignEditableErr, pgx.ErrNoRows) {
		t.Fatalf("foreign GetEditablePost() = (%+v, %v), want missing", foreignEditable, foreignEditableErr)
	}
	denied, err := CreateTopic(ctx, connections[0], func() time.Time { return createdAt }, actor, "staff-only", "Denied", "must not persist")
	if !errors.Is(err, ErrPublishingDenied) || denied != (PublishResult{}) {
		t.Fatalf("read-only CreateTopic() = (%+v, %v), want denied", denied, err)
	}
	edited, err := EditPost(ctx, connections[0], func() time.Time { return createdAt.Add(-time.Hour) }, actor, topic.PostID, 1, "Edited **first post**")
	if err != nil || edited != (EditResult{TopicID: topic.TopicID, PostID: topic.PostID, PostNumber: 1, Revision: 2}) {
		t.Fatalf("EditPost() = (%+v, %v)", edited, err)
	}
	secondEdit, err := EditPost(ctx, connections[0], func() time.Time { return createdAt.Add(-2 * time.Hour) }, actor, topic.PostID, 2, "Edited **again**")
	if err != nil || secondEdit != (EditResult{TopicID: topic.TopicID, PostID: topic.PostID, PostNumber: 1, Revision: 3}) {
		t.Fatalf("second EditPost() = (%+v, %v)", secondEdit, err)
	}
	if stale, staleErr := EditPost(ctx, connections[0], func() time.Time { return createdAt.Add(time.Hour) }, actor, topic.PostID, 2, "stale overwrite"); stale != (EditResult{}) || !errors.Is(staleErr, ErrPostEditConflict) {
		t.Fatalf("stale EditPost() = (%+v, %v), want conflict", stale, staleErr)
	}
	foreignActor := policy.AccessContext{Authenticated: true, UserID: ownerID, Role: policy.RoleAdministrator}
	if foreign, foreignErr := EditPost(ctx, connections[0], func() time.Time { return createdAt.Add(time.Hour) }, foreignActor, topic.PostID, 3, "staff rewrite"); foreign != (EditResult{}) || !errors.Is(foreignErr, ErrPostEditDenied) {
		t.Fatalf("foreign EditPost() = (%+v, %v), want denied", foreign, foreignErr)
	}
	var editedSource, editedHTML, editedRenderer string
	var editedRevision int32
	var postCreatedAt, postUpdatedAt, postEditedAt time.Time
	if err := connections[0].QueryRow(ctx, `SELECT markdown_source, rendered_html, renderer_version, revision, created_at, updated_at, edited_at FROM public.posts WHERE id = $1`, topic.PostID).Scan(
		&editedSource, &editedHTML, &editedRenderer, &editedRevision, &postCreatedAt, &postUpdatedAt, &postEditedAt,
	); err != nil || editedSource != "Edited **again**" || editedHTML != "<p>Edited <strong>again</strong></p>\n" ||
		editedRenderer != render.RendererVersion || editedRevision != 3 || postUpdatedAt.Before(postCreatedAt) || postEditedAt.Before(postCreatedAt) {
		t.Fatalf("persisted edit = (%q, %q, %q, %d, %s/%s/%s, %v)", editedSource, editedHTML, editedRenderer, editedRevision, postCreatedAt, postUpdatedAt, postEditedAt, err)
	}

	start := make(chan struct{})
	results := make(chan PublishResult, concurrentReplies)
	errorsChannel := make(chan error, concurrentReplies)
	var wait sync.WaitGroup
	for index, connection := range connections {
		index, connection := index, connection
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, replyErr := CreateReply(ctx, connection, func() time.Time { return createdAt.Add(time.Duration(index+1) * time.Second) }, actor, topic.TopicID, "Reply **number**")
			results <- result
			errorsChannel <- replyErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for replyErr := range errorsChannel {
		if replyErr != nil {
			t.Fatalf("concurrent CreateReply() returned error: %v", replyErr)
		}
	}
	numbers := make([]int, 0, concurrentReplies)
	postIDs := make(map[int64]struct{}, concurrentReplies)
	for result := range results {
		if result.TopicID != topic.TopicID || result.PostID <= 0 {
			t.Fatalf("concurrent reply returned invalid result: %+v", result)
		}
		numbers = append(numbers, int(result.PostNumber))
		postIDs[result.PostID] = struct{}{}
	}
	sort.Ints(numbers)
	for index, number := range numbers {
		if number != index+2 {
			t.Fatalf("reply numbers = %v, want contiguous 2-%d", numbers, concurrentReplies+1)
		}
	}
	if len(postIDs) != concurrentReplies {
		t.Fatalf("unique reply IDs = %d, want %d", len(postIDs), concurrentReplies)
	}

	var firstPostID, latestPostID int64
	var replyCount, nextPostNumber, postCount, distinctNumbers, renderedCount int
	if err := connections[0].QueryRow(ctx, `
SELECT topic.first_post_id, topic.latest_post_id, topic.reply_count, topic.next_post_number,
       count(post.id)::integer, count(DISTINCT post.post_number)::integer,
       count(*) FILTER (WHERE post.renderer_version = $2 AND post.rendered_html <> '')::integer
FROM public.topics AS topic
JOIN public.posts AS post ON post.topic_id = topic.id
WHERE topic.id = $1
GROUP BY topic.id`, topic.TopicID, render.RendererVersion).Scan(
		&firstPostID, &latestPostID, &replyCount, &nextPostNumber, &postCount, &distinctNumbers, &renderedCount,
	); err != nil {
		t.Fatalf("inspect published topic: %v", err)
	}
	if firstPostID != topic.PostID || latestPostID == topic.PostID || replyCount != concurrentReplies || nextPostNumber != concurrentReplies+2 ||
		postCount != concurrentReplies+1 || distinctNumbers != concurrentReplies+1 || renderedCount != concurrentReplies+1 {
		t.Fatalf("published state = (first %d latest %d replies %d next %d posts %d distinct %d rendered %d)",
			firstPostID, latestPostID, replyCount, nextPostNumber, postCount, distinctNumbers, renderedCount)
	}

	timestampTopic, err := CreateTopic(ctx, connections[0], func() time.Time { return createdAt }, actor, "normal", "Monotonic timestamps", "first")
	if err != nil {
		t.Fatalf("create timestamp topic: %v", err)
	}
	if _, err := CreateReply(ctx, connections[0], func() time.Time { return createdAt.Add(2 * time.Hour) }, actor, timestampTopic.TopicID, "later clock"); err != nil {
		t.Fatalf("create later-clock reply: %v", err)
	}
	if _, err := CreateReply(ctx, connections[0], func() time.Time { return createdAt.Add(time.Hour) }, actor, timestampTopic.TopicID, "earlier clock"); err != nil {
		t.Fatalf("create earlier-clock reply: %v", err)
	}
	var timestampsMonotonic bool
	if err := connections[0].QueryRow(ctx, `
SELECT bool_and(previous_created_at IS NULL OR previous_created_at <= created_at)
FROM (
    SELECT created_at, lag(created_at) OVER (ORDER BY post_number) AS previous_created_at
    FROM public.posts
    WHERE topic_id = $1
) AS ordered_posts`, timestampTopic.TopicID).Scan(&timestampsMonotonic); err != nil || !timestampsMonotonic {
		t.Fatalf("post timestamps monotonic = (%t, %v), want true/nil", timestampsMonotonic, err)
	}

	deleteTopic, err := CreateTopic(ctx, connections[0], func() time.Time { return createdAt }, actor, "normal", "Author deletion", "retain **source**")
	if err != nil {
		t.Fatalf("create delete topic: %v", err)
	}
	if foreign, foreignErr := DeletePost(ctx, connections[0], func() time.Time { return createdAt.Add(time.Hour) }, foreignActor, deleteTopic.PostID, 1); foreign != (DeleteResult{}) || !errors.Is(foreignErr, ErrPostDeleteDenied) {
		t.Fatalf("foreign DeletePost() = (%+v, %v), want denied", foreign, foreignErr)
	}
	if stale, staleErr := DeletePost(ctx, connections[0], func() time.Time { return createdAt.Add(time.Hour) }, actor, deleteTopic.PostID, 2); stale != (DeleteResult{}) || !errors.Is(staleErr, ErrPostDeleteConflict) {
		t.Fatalf("stale DeletePost() = (%+v, %v), want conflict", stale, staleErr)
	}
	deleted, err := DeletePost(ctx, connections[0], func() time.Time { return createdAt.Add(-time.Hour) }, actor, deleteTopic.PostID, 1)
	if err != nil || deleted != (DeleteResult{TopicID: deleteTopic.TopicID, PostID: deleteTopic.PostID, PostNumber: 1, Revision: 1}) {
		t.Fatalf("DeletePost() = (%+v, %v)", deleted, err)
	}
	var deletedAt time.Time
	var deletedBy int64
	var deletionReason, retainedSource string
	if err := connections[0].QueryRow(ctx, `SELECT deleted_at, deleted_by, deletion_reason, markdown_source FROM public.posts WHERE id = $1`, deleteTopic.PostID).Scan(
		&deletedAt, &deletedBy, &deletionReason, &retainedSource,
	); err != nil || deletedAt.Before(createdAt) || deletedBy != memberID || deletionReason != "Deleted by author" || retainedSource != "retain **source**" {
		t.Fatalf("persisted delete = (%s, %d, %q, %q, %v)", deletedAt, deletedBy, deletionReason, retainedSource, err)
	}
	visibleAfterDelete, err := store.GetVisibleTopicPostPage(ctx, db.New(connections[0]), deleteTopic.TopicID, 1, actor)
	if err != nil || visibleAfterDelete.TotalPosts != 0 || len(visibleAfterDelete.Rows) != 1 || visibleAfterDelete.Rows[0].PostID.Valid {
		t.Fatalf("visible deleted topic = (%+v, %v), want empty sentinel", visibleAfterDelete, err)
	}
	if repeated, repeatedErr := DeletePost(ctx, connections[0], time.Now, actor, deleteTopic.PostID, 1); repeated != (DeleteResult{}) || !errors.Is(repeatedErr, pgx.ErrNoRows) {
		t.Fatalf("repeated DeletePost() = (%+v, %v), want missing", repeated, repeatedErr)
	}
}
