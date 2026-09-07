//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
)

func TestApplyReleasePreflightFailsBeforeAlpha3SchemaMutationOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	denseSource := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	denseLegacyHTML := migrationLegacyHTML(t, denseSource)
	validSource := "valid **source**"
	validCurrentHTML := migrationCurrentHTML(t, validSource)
	for index, test := range []struct {
		name       string
		postID     int64
		markdown   string
		html       string
		version    string
		wantReason string
	}{
		{name: "minimum ID whitespace source", postID: math.MinInt64, markdown: " \n ", html: "<p>schema admitted</p>\n", version: contentrender.LegacyRendererVersion, wantReason: "invalid size, encoding, or content"},
		{name: "negative ID unknown renderer overflow", postID: -1, markdown: denseSource, html: denseLegacyHTML, version: "unknown-renderer-v1", wantReason: "renderer is not the admitted p1 version"},
		{name: "zero ID mismatched p1", postID: 0, markdown: denseSource, html: denseLegacyHTML + "mismatch", version: contentrender.LegacyRendererVersion, wantReason: "does not match"},
		{name: "premature current whitespace", postID: 1, markdown: " ", html: "<p>forged</p>\n", version: contentrender.RendererVersion, wantReason: "predates Alpha.3"},
		{name: "premature current forged output", postID: 2, markdown: validSource, html: "<p>forged</p>\n", version: contentrender.RendererVersion, wantReason: "predates Alpha.3"},
		{name: "premature exact current output", postID: 3, markdown: validSource, html: validCurrentHTML, version: contentrender.RendererVersion, wantReason: "predates Alpha.3"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			databaseName := fmt.Sprintf("gotth_bb_alpha3_preflight_failure_%d", index)
			configured, connection := migrationTestDatabase(t, ctx, databaseURL, databaseName)
			if err := migration.Apply(ctx, configured, preAlpha3Migrations(t)); err != nil {
				t.Fatalf("apply pre-Alpha.3 migrations: %v", err)
			}
			insertMigrationPreflightPost(t, ctx, connection, test.postID, test.markdown, test.html, test.version)

			err := applyRelease(ctx, configured, migrations.Files())
			if err == nil || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("applyRelease() error = %v, want preflight failure containing %q", err, test.wantReason)
			}
			var alpha3LedgerCount, rendererConstraintCount int
			var rendererStateAbsent bool
			if err := connection.QueryRow(ctx, `SELECT
	(SELECT count(*) FROM public.gotth_schema_migrations WHERE version = 7),
	(SELECT count(*) FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current'),
	(pg_catalog.to_regclass('public.content_renderer_state') IS NULL)`).Scan(&alpha3LedgerCount, &rendererConstraintCount, &rendererStateAbsent); err != nil {
				t.Fatalf("inspect failed preflight state: %v", err)
			}
			if alpha3LedgerCount != 0 || rendererConstraintCount != 0 || !rendererStateAbsent {
				t.Fatalf("failed preflight state = (ledger %d, constraint %d, state absent %t), want 0/0/true", alpha3LedgerCount, rendererConstraintCount, rendererStateAbsent)
			}
		})
	}
}

func TestApplyReleasePreflightRechecksCurrentOutputOnIdempotentRunOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configured, connection := migrationTestDatabase(t, ctx, databaseURL, "gotth_bb_alpha3_preflight_current")
	if err := applyRelease(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("fresh applyRelease() returned error: %v", err)
	}
	const source = "valid **source**"
	insertMigrationPreflightPost(t, ctx, connection, 9_000_003, source, migrationCurrentHTML(t, source), contentrender.RendererVersion)
	if err := applyRelease(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("exact current applyRelease() returned error: %v", err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET rendered_html = '<p>forged</p>' WHERE id = 9000003`); err != nil {
		t.Fatalf("forge current renderer output: %v", err)
	}
	if err := applyRelease(ctx, configured, migrations.Files()); err == nil || !strings.Contains(err.Error(), "does not match canonical Markdown") {
		t.Fatalf("forged current applyRelease() error = %v, want exact-output rejection", err)
	}
}

func TestApplyReleasePreflightHandlesFreshAndIdempotentRunOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configured, connection := migrationTestDatabase(t, ctx, databaseURL, "gotth_bb_alpha3_preflight_fresh")
	if err := applyRelease(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("fresh applyRelease() returned error: %v", err)
	}
	var ledgerCount int
	var completedAt time.Time
	var rendererValidated bool
	var lastProcessedPostID *int64
	var convertedCount int64
	if err := connection.QueryRow(ctx, `SELECT
	(SELECT count(*) FROM public.gotth_schema_migrations WHERE version = 7),
	(SELECT completed_at FROM public.content_renderer_state WHERE singleton),
	(SELECT last_processed_post_id FROM public.content_renderer_state WHERE singleton),
	(SELECT converted_count FROM public.content_renderer_state WHERE singleton),
	(SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current')`).Scan(&ledgerCount, &completedAt, &lastProcessedPostID, &convertedCount, &rendererValidated); err != nil {
		t.Fatalf("inspect fresh Alpha.3 state: %v", err)
	}
	if ledgerCount != 1 || completedAt.IsZero() || lastProcessedPostID != nil || convertedCount != 0 || !rendererValidated {
		t.Fatalf("fresh Alpha.3 state = (ledger %d, completed %s, cursor %v, converted %d, validated %t), want 1/nonzero/nil/0/true", ledgerCount, completedAt, lastProcessedPostID, convertedCount, rendererValidated)
	}
	if err := applyRelease(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("idempotent applyRelease() returned error: %v", err)
	}
	var repeatedCompletedAt time.Time
	if err := connection.QueryRow(ctx, `SELECT completed_at FROM public.content_renderer_state WHERE singleton`).Scan(&repeatedCompletedAt); err != nil || !repeatedCompletedAt.Equal(completedAt) {
		t.Fatalf("idempotent completion = (%s, %v), want %s/nil", repeatedCompletedAt, err, completedAt)
	}
}

func TestApplyReleasePreflightMigratesAndRechecksValidP1CompatibilityOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configured, connection := migrationTestDatabase(t, ctx, databaseURL, "gotth_bb_alpha3_preflight_compatibility")
	if err := migration.Apply(ctx, configured, preAlpha3Migrations(t)); err != nil {
		t.Fatalf("apply pre-Alpha.3 migrations: %v", err)
	}
	denseSource := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	denseLegacyHTML := migrationLegacyHTML(t, denseSource)
	insertMigrationPreflightPost(t, ctx, connection, 9_000_002, denseSource, denseLegacyHTML, contentrender.LegacyRendererVersion)
	if err := applyRelease(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("compatibility applyRelease() returned error: %v", err)
	}
	var html, version string
	if err := connection.QueryRow(ctx, `SELECT rendered_html, renderer_version FROM public.posts WHERE id = 9000002`).Scan(&html, &version); err != nil {
		t.Fatalf("inspect compatibility row: %v", err)
	}
	if html != denseLegacyHTML || version != "goldmark-v1.8.5-bluemonday-v1.0.27-p1-preserved" {
		t.Fatalf("compatibility row = (%d bytes, %q), want exact p1 HTML and marker", len(html), version)
	}
	if err := applyRelease(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("compatibility idempotent applyRelease() returned error: %v", err)
	}
}

func migrationTestDatabase(t *testing.T, ctx context.Context, databaseURL, name string) (*pgx.ConnConfig, *pgx.Conn) {
	t.Helper()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create integration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	configured := adminConfig.Copy()
	configured.Database = name
	connection, err := pgx.ConnectConfig(ctx, configured)
	if err != nil {
		t.Fatalf("connect integration database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	return configured, connection
}

func preAlpha3Migrations(t *testing.T) fs.FS {
	t.Helper()
	legacy := fstest.MapFS{}
	for _, name := range []string{
		"000001_identity_and_sessions.sql", "000002_groups_and_areas.sql",
		"000003_topics_posts_and_reads.sql", "000004_reports_and_audit.sql",
		"000005_threaded_posts.sql", "000006_reports_moderation_completion.sql",
	} {
		body, err := fs.ReadFile(migrations.Files(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		legacy[name] = &fstest.MapFile{Data: body}
	}
	return legacy
}

func insertMigrationPreflightPost(t *testing.T, ctx context.Context, connection *pgx.Conn, postID int64, markdown, html, version string) {
	t.Helper()
	var userID, areaID, topicID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Preflight owner', 'administrator') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert preflight owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('preflight', 'Preflight', $1, $1) RETURNING id`, userID).Scan(&areaID); err != nil {
		t.Fatalf("insert preflight area: %v", err)
	}
	if err := connection.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.topics', 'id'))`).Scan(&topicID); err != nil {
		t.Fatalf("allocate preflight topic: %v", err)
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin preflight fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics (id, area_id, author_id, title, first_post_id, latest_post_id) VALUES ($1, $2, $3, 'Preflight topic', $4, $4)`, topicID, areaID, userID, postID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert preflight topic: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.posts (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id, thread_path) VALUES ($1, $2, $3, 1, $4, $5, $6, NULL, ARRAY[1])`, postID, topicID, userID, markdown, html, version); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert preflight post: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit preflight fixture: %v", err)
	}
}

func migrationLegacyHTML(t *testing.T, source string) string {
	t.Helper()
	var rendered bytes.Buffer
	if err := goldmark.New().Convert([]byte(source), &rendered); err != nil {
		t.Fatalf("render legacy fixture: %v", err)
	}
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "em", "strong", "ul", "ol", "li", "a", "blockquote", "pre", "code", "br")
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy.Sanitize(rendered.String())
}

func migrationCurrentHTML(t *testing.T, source string) string {
	t.Helper()
	rendered, err := contentrender.RenderMarkdown(source)
	if err != nil {
		t.Fatalf("render current fixture: %v", err)
	}
	html, _, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("read current fixture: %v", err)
	}
	return html
}
