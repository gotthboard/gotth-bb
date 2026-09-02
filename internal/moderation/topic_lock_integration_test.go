//go:build integration

package moderation

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/forum"
	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const topicModerationTestDatabase = "gotth_bb_alpha1_topic_moderation_test"

func TestTopicLockModerationOnPostgreSQL17(t *testing.T) {
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
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+topicModerationTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale moderation database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+topicModerationTestDatabase); err != nil {
		t.Fatalf("create moderation database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+topicModerationTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = topicModerationTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect moderation database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	var moderatorID, memberID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Moderator', 'moderator') RETURNING id`).Scan(&moderatorID); err != nil {
		t.Fatalf("insert moderator: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Member') RETURNING id`).Scan(&memberID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('moderation', 'Moderation', $1, $1)`, moderatorID); err != nil {
		t.Fatalf("insert area: %v", err)
	}
	createdAt := time.Date(2026, time.September, 2, 8, 0, 0, 0, time.UTC)
	moderator := policy.AccessContext{Authenticated: true, UserID: moderatorID, Role: policy.RoleModerator}
	topic, err := forum.CreateTopic(ctx, connection, func() time.Time { return createdAt }, moderator, "moderation", "Audited topic", "body")
	if err != nil {
		t.Fatalf("forum.CreateTopic() returned error: %v", err)
	}
	if _, err := connection.Exec(ctx, `
CREATE FUNCTION public.reject_topic_moderation_audit()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'test audit rejection';
END;
$$`); err != nil {
		t.Fatalf("create rejecting audit function: %v", err)
	}
	if _, err := connection.Exec(ctx, `
CREATE TRIGGER reject_topic_moderation_audit
BEFORE INSERT ON public.moderation_actions
FOR EACH ROW EXECUTE FUNCTION public.reject_topic_moderation_audit()`); err != nil {
		t.Fatalf("create rejecting audit trigger: %v", err)
	}
	failed, failedErr := ChangeTopicLock(ctx, connection, time.Now, moderator, topic.TopicID, true, "Must roll back", pgtype.UUID{Bytes: [16]byte{0x50}, Valid: true})
	if failed != (TopicLockResult{}) || failedErr == nil {
		t.Fatalf("ChangeTopicLock(audit failure) = (%+v, %v), want failure", failed, failedErr)
	}
	var state string
	var auditCount int64
	if err := connection.QueryRow(ctx, `SELECT state, (SELECT count(*) FROM public.moderation_actions) FROM public.topics WHERE id = $1`, topic.TopicID).Scan(&state, &auditCount); err != nil || state != "open" || auditCount != 0 {
		t.Fatalf("rolled-back lock = (%q, count %d, %v)", state, auditCount, err)
	}
	if _, err := connection.Exec(ctx, `DROP TRIGGER reject_topic_moderation_audit ON public.moderation_actions`); err != nil {
		t.Fatalf("drop rejecting audit trigger: %v", err)
	}
	if _, err := connection.Exec(ctx, `DROP FUNCTION public.reject_topic_moderation_audit()`); err != nil {
		t.Fatalf("drop rejecting audit function: %v", err)
	}
	requestID := pgtype.UUID{Bytes: [16]byte{0x51}, Valid: true}
	locked, err := ChangeTopicLock(ctx, connection, func() time.Time { return createdAt.Add(-time.Hour) }, moderator, topic.TopicID, true, "Lock for review", requestID)
	if err != nil || locked.TopicID != topic.TopicID || locked.State != policy.TopicLocked || locked.AuditID <= 0 {
		t.Fatalf("ChangeTopicLock(lock) = (%+v, %v)", locked, err)
	}
	var action, reason, previous, resulting string
	var actorID, targetID int64
	var updatedAt, auditedAt time.Time
	if err := connection.QueryRow(ctx, `
SELECT topic.state, topic.updated_at, action.actor_user_id, action.target_topic_id,
       action.action_type, action.reason, action.previous_state->>'state',
       action.resulting_state->>'state', action.created_at,
       (SELECT count(*) FROM public.moderation_actions)
FROM public.topics AS topic
JOIN public.moderation_actions AS action ON action.id = $2
WHERE topic.id = $1`, topic.TopicID, locked.AuditID).Scan(
		&state, &updatedAt, &actorID, &targetID, &action, &reason, &previous, &resulting, &auditedAt, &auditCount,
	); err != nil || state != "locked" || updatedAt.Before(createdAt) || actorID != moderatorID || targetID != topic.TopicID ||
		action != "lock_topic" || reason != "Lock for review" || previous != "open" || resulting != "locked" || !auditedAt.Equal(updatedAt) || auditCount != 1 {
		t.Fatalf("persisted lock = (%q, %s, %d/%d, %q, %q, %q->%q, %s, count %d, %v)", state, updatedAt, actorID, targetID, action, reason, previous, resulting, auditedAt, auditCount, err)
	}
	if repeated, repeatedErr := ChangeTopicLock(ctx, connection, time.Now, moderator, topic.TopicID, true, "repeat", pgtype.UUID{Bytes: [16]byte{0x52}, Valid: true}); repeated != (TopicLockResult{}) || !errors.Is(repeatedErr, ErrTopicModerationConflict) {
		t.Fatalf("repeated lock = (%+v, %v), want conflict", repeated, repeatedErr)
	}
	member := policy.AccessContext{Authenticated: true, UserID: memberID, Role: policy.RoleMember}
	if denied, deniedErr := ChangeTopicLock(ctx, connection, time.Now, member, topic.TopicID, false, "not allowed", pgtype.UUID{Bytes: [16]byte{0x53}, Valid: true}); denied != (TopicLockResult{}) || !errors.Is(deniedErr, ErrTopicModerationDenied) {
		t.Fatalf("member unlock = (%+v, %v), want denied", denied, deniedErr)
	}
	unlocked, err := ChangeTopicLock(ctx, connection, func() time.Time { return createdAt.Add(time.Hour) }, moderator, topic.TopicID, false, "Review complete", pgtype.UUID{Bytes: [16]byte{0x54}, Valid: true})
	if err != nil || unlocked.State != policy.TopicOpen || unlocked.AuditID <= locked.AuditID {
		t.Fatalf("ChangeTopicLock(unlock) = (%+v, %v)", unlocked, err)
	}
	if err := connection.QueryRow(ctx, `
SELECT topic.state, topic.updated_at, action.actor_user_id, action.target_topic_id,
       action.action_type, action.reason, action.previous_state->>'state',
       action.resulting_state->>'state', action.created_at,
       (SELECT count(*) FROM public.moderation_actions)
FROM public.topics AS topic
JOIN public.moderation_actions AS action ON action.id = $2
WHERE topic.id = $1`, topic.TopicID, unlocked.AuditID).Scan(
		&state, &updatedAt, &actorID, &targetID, &action, &reason, &previous, &resulting, &auditedAt, &auditCount,
	); err != nil || state != "open" || !updatedAt.Equal(createdAt.Add(time.Hour)) || actorID != moderatorID || targetID != topic.TopicID ||
		action != "unlock_topic" || reason != "Review complete" || previous != "locked" || resulting != "open" || !auditedAt.Equal(updatedAt) || auditCount != 2 {
		t.Fatalf("persisted unlock = (%q, %s, %d/%d, %q, %q, %q->%q, %s, count %d, %v)", state, updatedAt, actorID, targetID, action, reason, previous, resulting, auditedAt, auditCount, err)
	}
}
