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
	denied, err := CreateTopic(ctx, connections[0], func() time.Time { return createdAt }, actor, "staff-only", "Denied", "must not persist")
	if !errors.Is(err, ErrPublishingDenied) || denied != (PublishResult{}) {
		t.Fatalf("read-only CreateTopic() = (%+v, %v), want denied", denied, err)
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
}
