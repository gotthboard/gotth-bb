//go:build integration

package migrations

import (
	"context"
	"io/fs"
	"os"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/jackc/pgx/v5"
)

const upgradeTestDatabase = "gotth_bb_alpha2_upgrade_test"

func TestPopulatedAlphaOneUpgradeOnPostgreSQL17(t *testing.T) {
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+upgradeTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale upgrade database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+upgradeTestDatabase); err != nil {
		t.Fatalf("create upgrade database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+upgradeTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = upgradeTestDatabase
	alphaOne := fstest.MapFS{}
	for _, name := range []string{
		"000001_identity_and_sessions.sql",
		"000002_groups_and_areas.sql",
		"000003_topics_posts_and_reads.sql",
		"000004_reports_and_audit.sql",
	} {
		body, readErr := fs.ReadFile(Files(), name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		alphaOne[name] = &fstest.MapFile{Data: body}
	}
	if err := migration.Apply(ctx, testConfig, alphaOne); err != nil {
		t.Fatalf("apply alpha.1 schema: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect alpha.1 database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	var userID, areaID, topicID, rootID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Upgrade owner', 'administrator') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert upgrade user: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('upgrade', 'Upgrade', $1, $1) RETURNING id`, userID).Scan(&areaID); err != nil {
		t.Fatalf("insert upgrade area: %v", err)
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin root transaction: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.topics', 'id')), nextval(pg_get_serial_sequence('public.posts', 'id'))`).Scan(&topicID, &rootID); err != nil {
		t.Fatalf("allocate upgrade identifiers: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics (id, area_id, author_id, title, first_post_id, latest_post_id) VALUES ($1, $2, $3, 'Upgrade topic', $4, $4)`, topicID, areaID, userID, rootID); err != nil {
		t.Fatalf("insert upgrade topic: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.posts (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version) VALUES ($1, $2, $3, 1, 'root', '<p>root</p>', 'alpha1')`, rootID, topicID, userID); err != nil {
		t.Fatalf("insert upgrade root: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit upgrade root: %v", err)
	}
	tx, err = connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reply transaction: %v", err)
	}
	var replyID int64
	if err := tx.QueryRow(ctx, `INSERT INTO public.posts (topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version) VALUES ($1, $2, 2, 'reply', '<p>reply</p>', 'alpha1') RETURNING id`, topicID, userID).Scan(&replyID); err != nil {
		t.Fatalf("insert upgrade reply: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE public.topics SET latest_post_id = $2, reply_count = 1, next_post_number = 3, updated_at = clock_timestamp(), last_activity_at = clock_timestamp() WHERE id = $1`, topicID, replyID); err != nil {
		t.Fatalf("advance upgrade topic: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit upgrade reply: %v", err)
	}
	if err := migration.Apply(ctx, testConfig, Files()); err != nil {
		t.Fatalf("upgrade populated alpha.1 database: %v", err)
	}
	var migrationCount int
	var rootParent, replyParent *int64
	var rootPath, replyPath []int32
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.gotth_schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("inspect upgrade migration count: %v", err)
	}
	if err := connection.QueryRow(ctx, `SELECT parent_post_id, thread_path FROM public.posts WHERE id = $1`, rootID).Scan(&rootParent, &rootPath); err != nil {
		t.Fatalf("inspect upgraded root: %v", err)
	}
	if err := connection.QueryRow(ctx, `SELECT parent_post_id, thread_path FROM public.posts WHERE id = $1`, replyID).Scan(&replyParent, &replyPath); err != nil {
		t.Fatalf("inspect upgraded reply: %v", err)
	}
	if migrationCount != 5 || rootParent != nil || !reflect.DeepEqual(rootPath, []int32{1}) || replyParent == nil || *replyParent != rootID || !reflect.DeepEqual(replyPath, []int32{1, 2}) {
		t.Fatalf("upgraded state = (migrations %d, root %v/%v, reply %v/%v)", migrationCount, rootParent, rootPath, replyParent, replyPath)
	}
}
