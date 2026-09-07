//go:build integration

package searchprojection

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const projectionTestDatabase = "gotth_bb_an02_projection_test"

func TestProjectionFailureRestartAndConcurrentRunnersOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configured, owner := projectionDatabase(t, ctx, databaseURL)
	if err := migration.Apply(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	seedProjectionRows(t, ctx, owner, 205)
	if _, err := owner.Exec(ctx, `CREATE FUNCTION public.reject_projection_test() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id = 50 AND NEW.search_projection_version IS NOT NULL THEN
        RAISE EXCEPTION 'forced projection failure';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER reject_projection_test BEFORE UPDATE OF search_projection_version ON public.topics
FOR EACH ROW EXECUTE FUNCTION public.reject_projection_test()`); err != nil {
		t.Fatalf("install rejecting trigger: %v", err)
	}
	if result, _, err := runBatch(ctx, owner, MaximumBatchSize); err == nil || result != (batchResult{}) {
		t.Fatalf("failing batch = (%+v, %v), want zero/error", result, err)
	}
	var topicCount int64
	var cursor *int64
	var populated int
	if err := owner.QueryRow(ctx, `SELECT topics_converted_count, last_processed_id,
       (SELECT count(*) FROM public.topics WHERE search_vector IS NOT NULL)
FROM public.search_projection_state WHERE singleton`).Scan(&topicCount, &cursor, &populated); err != nil {
		t.Fatalf("inspect rolled-back batch: %v", err)
	}
	if topicCount != 0 || cursor != nil || populated != 0 {
		t.Fatalf("rolled-back state = (count %d, cursor %v, populated %d), want 0/nil/0", topicCount, cursor, populated)
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER reject_projection_test ON public.topics; DROP FUNCTION public.reject_projection_test()`); err != nil {
		t.Fatalf("remove rejecting trigger: %v", err)
	}
	if result, _, err := runBatch(ctx, owner, MaximumBatchSize); err != nil || result != (batchResult{}) {
		t.Fatalf("first committed batch = (%+v, %v), want progress/nil", result, err)
	}
	if err := owner.QueryRow(ctx, `SELECT topics_converted_count, last_processed_id FROM public.search_projection_state WHERE singleton`).Scan(&topicCount, &cursor); err != nil {
		t.Fatalf("inspect first committed batch: %v", err)
	}
	if topicCount != 100 || cursor == nil || *cursor != 100 {
		t.Fatalf("first durable state = (count %d, cursor %v), want 100/100", topicCount, cursor)
	}

	connections := make([]*pgx.Conn, 2)
	for index := range connections {
		var err error
		connections[index], err = pgx.ConnectConfig(ctx, configured)
		if err != nil {
			t.Fatalf("connect runner %d: %v", index, err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}
	start := make(chan struct{})
	errorsByRunner := make(chan error, len(connections))
	var wait sync.WaitGroup
	for _, connection := range connections {
		connection := connection
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsByRunner <- Run(ctx, connection, MaximumBatchSize)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByRunner)
	for err := range errorsByRunner {
		if err != nil {
			t.Fatalf("concurrent runner: %v", err)
		}
	}
	var phase, target string
	var postsCount int64
	var completed time.Time
	var invalidRows int
	if err := owner.QueryRow(ctx, `SELECT phase, target_version, topics_converted_count,
       posts_converted_count, last_processed_id, completed_at,
       (SELECT count(*) FROM public.topics WHERE search_vector IS NULL OR search_projection_version IS DISTINCT FROM $1),
       (SELECT count(*) FROM public.posts WHERE search_vector IS NULL OR search_projection_version IS DISTINCT FROM $1)
FROM public.search_projection_state WHERE singleton`, contentrender.SearchProjectionVersion).Scan(
		&phase, &target, &topicCount, &postsCount, &cursor, &completed, &invalidRows, &populated,
	); err != nil {
		t.Fatalf("inspect completed projection: %v", err)
	}
	if phase != "complete" || target != contentrender.SearchProjectionVersion || topicCount != 205 || postsCount != 205 ||
		cursor == nil || *cursor != 1205 || completed.IsZero() || invalidRows != 0 || populated != 0 {
		t.Fatalf("completed state = (%s %s topics=%d posts=%d cursor=%v completed=%s invalid=%d/%d)", phase, target, topicCount, postsCount, cursor, completed, invalidRows, populated)
	}
	if err := Ready(ctx, owner); err != nil {
		t.Fatalf("Ready() rejected completed projection: %v", err)
	}
}

func projectionDatabase(t *testing.T, ctx context.Context, databaseURL string) (*pgx.ConnConfig, *pgx.Conn) {
	t.Helper()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect database administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+projectionTestDatabase+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+projectionTestDatabase); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+projectionTestDatabase+" WITH (FORCE)")
	})
	configured := adminConfig.Copy()
	configured.Database = projectionTestDatabase
	owner, err := pgx.ConnectConfig(ctx, configured)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	return configured, owner
}

func seedProjectionRows(t *testing.T, ctx context.Context, connection *pgx.Conn, count int) {
	t.Helper()
	var userID, areaID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Projection owner', 'administrator') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert projection owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('projection', 'Projection', $1, $1) RETURNING id`, userID).Scan(&areaID); err != nil {
		t.Fatalf("insert projection area: %v", err)
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin projection seed: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics
    (id, area_id, author_id, title, first_post_id, latest_post_id, reply_count, next_post_number)
SELECT value, $1, $2, 'Topic ' || value, 1000 + value, 1000 + value, 0, 2
FROM generate_series(1, $3::integer) AS value`, areaID, userID, count); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert projection topics: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.posts
    (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id, thread_path)
SELECT 1000 + value, value, $1, 1, 'Body ' || value, '<p>Body ' || value || '</p>' || chr(10), $2, NULL, ARRAY[1]::integer[]
FROM generate_series(1, $3::integer) AS value`, userID, contentrender.RendererVersion, count); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert projection posts: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit projection seed: %v", err)
	}
}
