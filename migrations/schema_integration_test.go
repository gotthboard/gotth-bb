//go:build integration

package migrations

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const schemaTestDatabase = "gotth_bb_alpha1_schema_test"

func expectExecutionFailure(t *testing.T, conn *pgx.Conn, ctx context.Context, statement string, arguments ...any) {
	t.Helper()
	if _, err := conn.Exec(ctx, statement, arguments...); err == nil {
		t.Fatalf("invalid statement succeeded: %s", statement)
	}
}

func TestInitialSchemaOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+schemaTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale schema test database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+schemaTestDatabase); err != nil {
		t.Fatalf("create schema test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+schemaTestDatabase+" WITH (FORCE)"); err != nil {
			t.Errorf("drop schema test database: %v", err)
		}
	})

	testConfig := adminConfig.Copy()
	testConfig.Database = schemaTestDatabase
	if err := migration.Apply(ctx, testConfig, Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	conn, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect migrated database: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("close migrated database connection: %v", err)
		}
	})

	var serverVersion int
	var migrationCount int
	var governanceCount int
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer,
       (SELECT count(*) FROM public.gotth_schema_migrations),
       (SELECT count(*) FROM public.governance_state WHERE singleton)`).Scan(&serverVersion, &migrationCount, &governanceCount); err != nil {
		t.Fatalf("inspect migrated database: %v", err)
	}
	if serverVersion != 170010 || migrationCount != 4 || governanceCount != 1 {
		t.Fatalf("schema state = (version %d, migrations %d, governance %d), want (170010, 4, 1)", serverVersion, migrationCount, governanceCount)
	}

	var administratorID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.users (display_name, role)
VALUES ('Administrator', 'administrator') RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	var memberID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.users (display_name, role)
VALUES ('Member', 'member') RETURNING id`).Scan(&memberID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, "INSERT INTO public.users (display_name, role) VALUES ('Bad', 'owner')")
	expectExecutionFailure(t, conn, ctx, "INSERT INTO public.governance_state (singleton) VALUES (true)")

	if _, err := conn.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject)
VALUES ($1, 'https://auth.example.test/application/o/forum/', 'admin-subject')`, administratorID); err != nil {
		t.Fatalf("insert external identity: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.external_identities (user_id, issuer, subject)
VALUES ($1, 'https://auth.example.test/application/o/forum/', 'admin-subject')`, memberID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.external_identities (user_id, issuer, subject)
VALUES ($1, 'https://other.example.test/', 'other-subject')`, administratorID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.sessions (token_hash, user_id, expires_at)
VALUES (decode('00', 'hex'), $1, clock_timestamp() + interval '1 hour')`, memberID)
	for index, returnPath := range []string{"/", "/bb/", "/community/board/topics?sort=new"} {
		if _, err := conn.Exec(ctx, `INSERT INTO public.oidc_login_attempts
    (state_hash, nonce_ciphertext, pkce_verifier_ciphertext, purpose, return_path, expires_at)
VALUES (decode(repeat($1, 32), 'hex'), decode('01', 'hex'), decode('02', 'hex'),
        'login', $2, clock_timestamp() + interval '5 minutes')`,
			[]string{"11", "22", "33"}[index], returnPath); err != nil {
			t.Fatalf("insert login attempt with return path %q: %v", returnPath, err)
		}
	}
	for index, returnPath := range []string{"", "relative", "//evil.example/path", "https://evil.example/path", `/bb\\escape`, "/bb/path#fragment", "/bb/path\nheader", "/" + strings.Repeat("a", 2048)} {
		if _, err := conn.Exec(ctx, `INSERT INTO public.oidc_login_attempts
    (state_hash, nonce_ciphertext, pkce_verifier_ciphertext, purpose, return_path, expires_at)
VALUES (decode(repeat($1, 32), 'hex'), decode('01', 'hex'), decode('02', 'hex'),
        'login', $2, clock_timestamp() + interval '5 minutes')`,
			[]string{"44", "55", "66", "77", "88", "99", "aa", "bb"}[index], returnPath); err == nil {
			t.Fatalf("unsafe login-attempt return path succeeded: %q", returnPath)
		}
	}
	queries := db.New(conn)
	if governanceRows, err := queries.CountGovernanceRows(ctx); err != nil || governanceRows != 1 {
		t.Fatalf("CountGovernanceRows() = (%d, %v), want (1, nil)", governanceRows, err)
	}
	if activeAdministrators, err := queries.CountActiveAdministrators(ctx, pgtype.Timestamptz{Time: time.Now(), Valid: true}); err != nil || activeAdministrators != 1 {
		t.Fatalf("CountActiveAdministrators() = (%d, %v), want (1, nil)", activeAdministrators, err)
	}
	identityUser, err := queries.GetUserByExternalIdentity(ctx, db.GetUserByExternalIdentityParams{
		Issuer:  "https://auth.example.test/application/o/forum/",
		Subject: "admin-subject",
	})
	if err != nil || identityUser.ID != administratorID {
		t.Fatalf("GetUserByExternalIdentity() = (id %d, %v), want (%d, nil)", identityUser.ID, err, administratorID)
	}
	transactionTime := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	var transactionUserID int64
	if err := store.WithinTx(ctx, conn, func(transactionQueries *db.Queries) error {
		created, err := transactionQueries.InsertUser(ctx, db.InsertUserParams{
			DisplayName: "Transaction User",
			LoginAt:     transactionTime,
		})
		if err != nil {
			return err
		}
		transactionUserID = created.ID
		return transactionQueries.InsertExternalIdentity(ctx, db.InsertExternalIdentityParams{
			UserID:     created.ID,
			Issuer:     "https://auth.example.test/application/o/forum/",
			Subject:    "transaction-subject",
			VerifiedAt: transactionTime,
		})
	}); err != nil {
		t.Fatalf("WithinTx() successful identity creation: %v", err)
	}
	transactionUser, err := queries.GetUserByExternalIdentity(ctx, db.GetUserByExternalIdentityParams{
		Issuer:  "https://auth.example.test/application/o/forum/",
		Subject: "transaction-subject",
	})
	if err != nil || transactionUser.ID != transactionUserID {
		t.Fatalf("transaction identity = (id %d, %v), want (%d, nil)", transactionUser.ID, err, transactionUserID)
	}
	rollbackMarker := errors.New("rollback marker")
	if err := store.WithinTx(ctx, conn, func(transactionQueries *db.Queries) error {
		if _, err := transactionQueries.InsertUser(ctx, db.InsertUserParams{
			DisplayName: "Rolled Back User",
			LoginAt:     transactionTime,
		}); err != nil {
			return err
		}
		return rollbackMarker
	}); err == nil || !errors.Is(err, rollbackMarker) {
		t.Fatalf("WithinTx() rollback error = %v, want rollback marker", err)
	}
	var rolledBackCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.users WHERE display_name = 'Rolled Back User'").Scan(&rolledBackCount); err != nil || rolledBackCount != 0 {
		t.Fatalf("rolled-back user count = (%d, %v), want (0, nil)", rolledBackCount, err)
	}

	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by)
VALUES ('Members', $1) RETURNING id`, administratorID).Scan(&groupID); err != nil {
		t.Fatalf("insert forum group: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.forum_group_members (group_id, user_id, granted_by)
VALUES ($1, $2, $3)`, groupID, memberID, administratorID); err != nil {
		t.Fatalf("insert forum group member: %v", err)
	}
	var areaID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by)
VALUES ('general', 'General', $1, $1) RETURNING id`, administratorID).Scan(&areaID); err != nil {
		t.Fatalf("insert area: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by)
VALUES ($1, $2, $3)`, areaID, groupID, administratorID)
	if _, err := conn.Exec(ctx, "UPDATE public.areas SET visibility = 'groups' WHERE id = $1", areaID); err != nil {
		t.Fatalf("make area group-visible: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by)
VALUES ($1, $2, $3)`, areaID, groupID, administratorID); err != nil {
		t.Fatalf("insert area group mapping: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, "UPDATE public.areas SET visibility = 'public' WHERE id = $1", areaID)
	expectExecutionFailure(t, conn, ctx, "UPDATE public.areas SET slug = 'renamed' WHERE id = $1", areaID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.areas (slug, name, visibility, created_by, updated_by)
VALUES ('bad-visibility', 'Bad', 'private', $1, $1)`, administratorID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.areas (slug, name, posting_mode, created_by, updated_by)
VALUES ('bad-posting', 'Bad', 'closed', $1, $1)`, administratorID)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin topic transaction: %v", err)
	}
	var topicID int64
	var firstPostID int64
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.topics', 'id')),
       nextval(pg_get_serial_sequence('public.posts', 'id'))`).Scan(&topicID, &firstPostID); err != nil {
		t.Fatalf("allocate topic identifiers: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics
    (id, area_id, author_id, title, first_post_id, latest_post_id)
VALUES ($1, $2, $3, 'First topic', $4, $4)`, topicID, areaID, memberID, firstPostID); err != nil {
		t.Fatalf("insert topic: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.posts
    (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version)
VALUES ($1, $2, $3, 1, 'First post', '<p>First post</p>', 'test-v1')`, firstPostID, topicID, memberID); err != nil {
		t.Fatalf("insert first post: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit topic transaction: %v", err)
	}

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reply transaction: %v", err)
	}
	var replyID int64
	if err := tx.QueryRow(ctx, `INSERT INTO public.posts
    (topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version)
VALUES ($1, $2, 2, 'Reply', '<p>Reply</p>', 'test-v1') RETURNING id`, topicID, memberID).Scan(&replyID); err != nil {
		t.Fatalf("insert reply: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE public.topics
SET latest_post_id = $2, reply_count = 1, next_post_number = 3,
    updated_at = clock_timestamp(), last_activity_at = clock_timestamp()
WHERE id = $1`, topicID, replyID); err != nil {
		t.Fatalf("update topic counters: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit reply transaction: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.posts
    (topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version)
VALUES ($1, $2, 2, 'Duplicate', '<p>Duplicate</p>', 'test-v1')`, topicID, memberID)
	expectExecutionFailure(t, conn, ctx, "UPDATE public.posts SET post_number = 3 WHERE id = $1", replyID)

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin inconsistent-counter transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE public.topics SET reply_count = 99 WHERE id = $1", topicID); err != nil {
		t.Fatalf("stage inconsistent counter: %v", err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("inconsistent topic counters committed")
	}

	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.topic_reads
    (user_id, topic_id, last_read_post_number) VALUES ($1, $2, 0)`, memberID, topicID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.reports
    (reported_by, topic_id, user_id, reason) VALUES ($1, $2, $1, 'two targets')`, memberID, topicID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.moderation_actions
    (actor_kind, target_type, target_user_id, action_type, request_id)
VALUES ('forum_user', 'user', $1, 'warn_user', '00000000-0000-0000-0000-000000000001')`, memberID)
	if _, err := conn.Exec(ctx, `INSERT INTO public.moderation_actions
    (actor_kind, actor_user_id, target_type, target_topic_id, action_type, reason, request_id)
VALUES ('forum_user', $1, 'topic', $2, 'lock_topic', NULL,
        '00000000-0000-0000-0000-000000000002')`, administratorID, topicID); err != nil {
		t.Fatalf("insert valid moderation action: %v", err)
	}
}
