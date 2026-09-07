//go:build integration

package rerender

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const rerenderTestDatabase = "gotth_bb_alpha3_rerender_test"

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRendererMigrationOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+rerenderTestDatabase+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+rerenderTestDatabase); err != nil {
		t.Fatalf("create renderer test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+rerenderTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = rerenderTestDatabase
	legacy := fstest.MapFS{}
	for _, name := range []string{
		"000001_identity_and_sessions.sql", "000002_groups_and_areas.sql",
		"000003_topics_posts_and_reads.sql", "000004_reports_and_audit.sql",
		"000005_threaded_posts.sql", "000006_reports_moderation_completion.sql",
	} {
		body, readErr := fs.ReadFile(migrations.Files(), name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		legacy[name] = &fstest.MapFile{Data: body}
	}
	if err := migration.Apply(ctx, testConfig, legacy); err != nil {
		t.Fatalf("apply pre-alpha.3 schema: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer test database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	runnerConnection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer runner: %v", err)
	}
	t.Cleanup(func() { _ = runnerConnection.Close(context.Background()) })
	contentionConnection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer contention probe: %v", err)
	}
	t.Cleanup(func() { _ = contentionConnection.Close(context.Background()) })
	var userID, areaID, topicID, rootID, replyID, currentID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Renderer owner', 'administrator') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert renderer owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('renderer', 'Renderer', $1, $1) RETURNING id`, userID).Scan(&areaID); err != nil {
		t.Fatalf("insert renderer area: %v", err)
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin renderer fixture: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.topics', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id'))`).Scan(&topicID, &rootID, &replyID, &currentID); err != nil {
		t.Fatalf("allocate renderer fixture identifiers: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics (id, area_id, author_id, title, first_post_id, latest_post_id, reply_count, next_post_number) VALUES ($1, $2, $3, 'Renderer topic', $4, $5, 2, 4)`, topicID, areaID, userID, rootID, currentID); err != nil {
		t.Fatalf("insert renderer topic: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.posts (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id, thread_path) VALUES
        ($1, $2, $3, 1, '~~root~~', '<p>~~root~~</p>', 'legacy-p1', NULL, ARRAY[1]),
		($4, $2, $3, 2, '- [x] reply', '<p>- [x] reply</p>', 'legacy-p1', $1, ARRAY[1,2]),
		($5, $2, $3, 3, 'current', '<p>current</p>', $6, $1, ARRAY[1,3])`, rootID, topicID, userID, replyID, currentID, contentrender.RendererVersion); err != nil {
		t.Fatalf("insert renderer posts: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit renderer fixture: %v", err)
	}
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("apply alpha.3 schema: %v", err)
	}

	var validated bool
	if err := connection.QueryRow(ctx, `SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current'`).Scan(&validated); err != nil || validated {
		t.Fatalf("new writer constraint = (validated %t, %v), want false/nil", validated, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET rendered_html = rendered_html WHERE id = $1`, rootID); err == nil {
		t.Fatal("NOT VALID constraint accepted an old-version update")
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET rendered_html = rendered_html WHERE topic_id = $1 AND post_number = 3`, topicID); err != nil {
		t.Fatalf("NOT VALID constraint rejected a current-version update: %v", err)
	}
	if _, err := connection.Exec(ctx, `WITH effective AS (SELECT clock_timestamp() AS at_time)
UPDATE public.posts
SET markdown_source = '[Content removed by moderation]',
    rendered_html = '<p>Content removed by moderation.</p>',
    renderer_version = 'moderation-redaction-v1',
    deleted_at = effective.at_time,
    deleted_by = $2,
    deletion_reason = 'redacted',
    redacted_at = effective.at_time,
    redacted_by = $2,
    redaction_reason = 'redacted'
FROM effective
WHERE topic_id = $1 AND post_number = 3`, topicID, userID); err != nil {
		t.Fatalf("NOT VALID constraint rejected exact moderation redaction: %v", err)
	}
	statementTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin writer constraint probe: %v", err)
	}
	if _, err := statementTx.Exec(ctx, `INSERT INTO public.posts (topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id) VALUES ($1, $2, 4, 'old', '<p>old</p>', 'legacy-p1', $3)`, topicID, userID, rootID); err == nil {
		t.Fatal("NOT VALID constraint accepted an old-version insert")
	}
	_ = statementTx.Rollback(ctx)

	editTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent edit probe: %v", err)
	}
	if _, err := editTx.Exec(ctx, `UPDATE public.posts SET markdown_source = '~~edited~~', rendered_html = '<p><del>edited</del></p>', renderer_version = $2 WHERE id = $1`, rootID, contentrender.RendererVersion); err != nil {
		t.Fatalf("lock stale row with concurrent edit: %v", err)
	}
	blockedEditContext, blockedEditCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err := Run(blockedEditContext, contentionConnection, io.Discard, 1); !errors.Is(err, context.DeadlineExceeded) {
		blockedEditCancel()
		_ = editTx.Rollback(ctx)
		t.Fatalf("edit-versus-rerender error = %v, want deadline", err)
	}
	blockedEditCancel()
	if err := editTx.Rollback(ctx); err != nil {
		t.Fatalf("release concurrent edit row: %v", err)
	}
	if !contentionConnection.IsClosed() {
		contentionConnection.Close(context.Background())
	}

	result, _, err := runBatch(ctx, runnerConnection, 1)
	if err != nil || result.Converted != 1 || result.Complete {
		t.Fatalf("first batch = (%+v, %v), want one/incomplete", result, err)
	}
	if err := Run(ctx, runnerConnection, failingWriter{}, 1); err == nil {
		t.Fatal("Run() accepted a failed progress write")
	}
	var staleCount int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.posts WHERE redacted_at IS NULL AND renderer_version <> $1`, contentrender.RendererVersion).Scan(&staleCount); err != nil || staleCount != 0 {
		t.Fatalf("post-output-failure stale rows = (%d, %v), want zero/nil", staleCount, err)
	}
	var output bytes.Buffer
	if err := Run(ctx, runnerConnection, &output, 1); err != nil {
		t.Fatalf("restart Run() returned error: %v", err)
	}
	if !strings.Contains(output.String(), "converted=0 complete=true") {
		t.Fatalf("restart progress = %q", output.String())
	}
	var completedAt time.Time
	if err := connection.QueryRow(ctx, `SELECT completed_at FROM public.content_renderer_state WHERE singleton`).Scan(&completedAt); err != nil {
		t.Fatalf("read renderer completion time: %v", err)
	}
	output.Reset()
	if err := Run(ctx, runnerConnection, &output, 1); err != nil || output.String() != progressLine(BatchResult{Complete: true}) {
		t.Fatalf("idempotent Run() = (%q, %v)", output.String(), err)
	}
	var repeatedCompletedAt time.Time
	if err := connection.QueryRow(ctx, `SELECT completed_at FROM public.content_renderer_state WHERE singleton`).Scan(&repeatedCompletedAt); err != nil || !repeatedCompletedAt.Equal(completedAt) {
		t.Fatalf("idempotent completion time = (%s, %v), want %s/nil", repeatedCompletedAt, err, completedAt)
	}
	if err := connection.QueryRow(ctx, `SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current'`).Scan(&validated); err != nil || !validated {
		t.Fatalf("completed writer constraint = (validated %t, %v), want true/nil", validated, err)
	}
	var rootHTML string
	if err := connection.QueryRow(ctx, `SELECT rendered_html FROM public.posts WHERE id = $1`, rootID).Scan(&rootHTML); err != nil || rootHTML != "<p><del>root</del></p>\n" {
		t.Fatalf("rerendered root = (%q, %v)", rootHTML, err)
	}

	lockTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin singleton lock probe: %v", err)
	}
	if _, err := lockTx.Exec(ctx, `SELECT target_version FROM public.content_renderer_state WHERE singleton FOR UPDATE`); err != nil {
		t.Fatalf("lock renderer singleton: %v", err)
	}
	singletonConnection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("connect singleton contention probe: %v", err)
	}
	blockedContext, blockedCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer blockedCancel()
	if err := Run(blockedContext, singletonConnection, io.Discard, 1); !errors.Is(err, context.DeadlineExceeded) {
		_ = singletonConnection.Close(context.Background())
		t.Fatalf("concurrent runner error = %v, want deadline", err)
	}
	_ = singletonConnection.Close(context.Background())
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("release renderer singleton: %v", err)
	}

	if _, err := connection.Exec(ctx, `ALTER TABLE public.posts DROP CONSTRAINT posts_renderer_version_current`); err != nil {
		t.Fatalf("drop renderer constraint for validation failure probe: %v", err)
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.posts ADD CONSTRAINT posts_renderer_version_current CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("install invalid renderer constraint probe: %v", err)
	}
	if err := Run(ctx, runnerConnection, io.Discard, MaximumBatchSize); err == nil || !strings.Contains(err.Error(), "validate current renderer constraint") {
		t.Fatalf("constraint validation failure = %v", err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM public.content_renderer_state WHERE singleton`); err != nil {
		t.Fatalf("delete renderer state probe: %v", err)
	}
	if err := Run(ctx, runnerConnection, io.Discard, MaximumBatchSize); err == nil || !strings.Contains(err.Error(), "lock renderer migration state") {
		t.Fatalf("missing renderer state failure = %v", err)
	}
}
