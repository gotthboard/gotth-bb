//go:build integration

package moderation

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const userSuspensionTestDatabase = "gotth_bb_alpha1_user_suspension_test"

func TestUserSuspensionOnPostgreSQL17(t *testing.T) {
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+userSuspensionTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale user-suspension database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+userSuspensionTestDatabase); err != nil {
		t.Fatalf("create user-suspension database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+userSuspensionTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = userSuspensionTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect user-suspension database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	createdAt := time.Date(2026, time.September, 2, 8, 0, 0, 0, time.UTC)
	var moderatorID, memberID int64
	if err := connection.QueryRow(ctx, `
INSERT INTO public.users (display_name, role, created_at, updated_at, last_login_at)
VALUES ('Moderator', 'moderator', $1, $1, $1)
RETURNING id`, createdAt).Scan(&moderatorID); err != nil {
		t.Fatalf("insert moderator: %v", err)
	}
	if err := connection.QueryRow(ctx, `
INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Member', $1, $1, $1)
RETURNING id`, createdAt).Scan(&memberID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	tokenHash := sha256.Sum256([]byte("user-suspension-integration-session"))
	issuedAt := createdAt.Add(time.Minute)
	if _, err := connection.Exec(ctx, `
INSERT INTO public.sessions (token_hash, user_id, issued_at, last_seen_at, validated_at, expires_at)
VALUES ($1, $2, $3, $3, $3, $4)`, tokenHash[:], memberID, issuedAt, createdAt.Add(24*time.Hour)); err != nil {
		t.Fatalf("insert member session: %v", err)
	}
	moderator := policy.AccessContext{Authenticated: true, UserID: moderatorID, Role: policy.RoleModerator}
	queries := db.New(connection)
	if status, statusErr := store.GetModerationUserStatus(ctx, queries, moderator, memberID, createdAt.Add(time.Hour)); statusErr != nil || status.UserID != memberID || status.DisplayName != "Member" || status.Role != policy.RoleMember || status.Suspended {
		t.Fatalf("active moderation status = (%+v, %v)", status, statusErr)
	}
	if _, err := connection.Exec(ctx, `
CREATE FUNCTION public.reject_user_suspension_audit()
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
CREATE TRIGGER reject_user_suspension_audit
BEFORE INSERT ON public.moderation_actions
FOR EACH ROW EXECUTE FUNCTION public.reject_user_suspension_audit()`); err != nil {
		t.Fatalf("create rejecting audit trigger: %v", err)
	}
	failed, failedErr := ChangeUserSuspension(ctx, connection, func() time.Time { return createdAt.Add(time.Hour) }, moderator,
		memberID, true, "Must roll back", pgtype.UUID{Bytes: [16]byte{0x81}, Valid: true})
	if failed != (UserSuspensionResult{}) || failedErr == nil {
		t.Fatalf("ChangeUserSuspension(audit failure) = (%+v, %v), want failure", failed, failedErr)
	}
	var active bool
	var auditCount int64
	if err := connection.QueryRow(ctx, `
SELECT suspended_at IS NULL AND suspended_until IS NULL AND suspension_reason IS NULL,
       (SELECT count(*) FROM public.moderation_actions)
FROM public.users WHERE id = $1`, memberID).Scan(&active, &auditCount); err != nil || !active || auditCount != 0 {
		t.Fatalf("rolled-back suspension = (active %t, count %d, %v)", active, auditCount, err)
	}
	if _, err := connection.Exec(ctx, `DROP TRIGGER reject_user_suspension_audit ON public.moderation_actions`); err != nil {
		t.Fatalf("drop rejecting audit trigger: %v", err)
	}
	if _, err := connection.Exec(ctx, `DROP FUNCTION public.reject_user_suspension_audit()`); err != nil {
		t.Fatalf("drop rejecting audit function: %v", err)
	}

	suspendedAt := createdAt.Add(2 * time.Hour)
	suspendRequestID := pgtype.UUID{Bytes: [16]byte{0x82}, Valid: true}
	suspended, err := ChangeUserSuspension(ctx, connection, func() time.Time { return suspendedAt }, moderator,
		memberID, true, "Repeated abuse", suspendRequestID)
	if err != nil || suspended.UserID != memberID || !suspended.Suspended || suspended.AuditID <= 0 {
		t.Fatalf("ChangeUserSuspension(suspend) = (%+v, %v)", suspended, err)
	}
	var storedSuspendedAt, updatedAt, auditedAt time.Time
	var actorKind, targetType, action, reason string
	var actorID, targetID int64
	var exactPrevious, exactResulting bool
	var storedRequestID pgtype.UUID
	if err := connection.QueryRow(ctx, `
SELECT forum_user.suspended_at, forum_user.updated_at,
       action.actor_kind, action.actor_user_id, action.target_type, action.target_user_id,
       action.action_type, action.reason,
       action.previous_state = jsonb_build_object(
           'suspended_at', NULL::timestamptz,
           'suspended_until', NULL::timestamptz,
           'suspension_reason', NULL::text
       ),
       action.resulting_state = jsonb_build_object(
           'suspended_at', forum_user.suspended_at,
           'suspended_until', forum_user.suspended_until,
           'suspension_reason', forum_user.suspension_reason
       ),
       action.request_id, action.created_at,
       (SELECT count(*) FROM public.moderation_actions)
FROM public.users AS forum_user
JOIN public.moderation_actions AS action ON action.id = $2
WHERE forum_user.id = $1
  AND forum_user.suspended_until IS NULL
  AND forum_user.suspension_reason = 'Repeated abuse'`, memberID, suspended.AuditID).Scan(
		&storedSuspendedAt, &updatedAt, &actorKind, &actorID, &targetType, &targetID,
		&action, &reason, &exactPrevious, &exactResulting, &storedRequestID, &auditedAt, &auditCount,
	); err != nil || !storedSuspendedAt.Equal(suspendedAt) || !updatedAt.Equal(suspendedAt) || actorKind != "forum_user" || actorID != moderatorID ||
		targetType != "user" || targetID != memberID || action != "suspend_user" || reason != "Repeated abuse" || !exactPrevious || !exactResulting ||
		storedRequestID != suspendRequestID || !auditedAt.Equal(updatedAt) || auditCount != 1 {
		t.Fatalf("persisted suspension = (%s/%s, %q %d, %q %d, %q, %q, exact %t/%t, request %+v, audit %s, count %d, %v)",
			storedSuspendedAt, updatedAt, actorKind, actorID, targetType, targetID, action, reason,
			exactPrevious, exactResulting, storedRequestID, auditedAt, auditCount, err)
	}
	sessionParams := db.GetActiveSessionParams{
		TokenHash: tokenHash[:], ObservedAt: pgtype.Timestamptz{Time: suspendedAt.Add(time.Minute), Valid: true},
		IdleCutoff: pgtype.Timestamptz{Time: createdAt, Valid: true},
	}
	if got, sessionErr := queries.GetActiveSession(ctx, sessionParams); !errors.Is(sessionErr, pgx.ErrNoRows) || !reflect.DeepEqual(got, db.GetActiveSessionRow{}) {
		t.Fatalf("suspended GetActiveSession() = (%+v, %v), want zero/no rows", got, sessionErr)
	}
	if status, statusErr := store.GetModerationUserStatus(ctx, queries, moderator, memberID, suspendedAt.Add(time.Minute)); statusErr != nil || !status.Suspended || status.SuspensionReason.String != "Repeated abuse" {
		t.Fatalf("suspended moderation status = (%+v, %v)", status, statusErr)
	}
	if repeated, repeatedErr := ChangeUserSuspension(ctx, connection, func() time.Time { return suspendedAt.Add(time.Minute) }, moderator, memberID, true, "Repeat",
		pgtype.UUID{Bytes: [16]byte{0x83}, Valid: true}); repeated != (UserSuspensionResult{}) || !errors.Is(repeatedErr, ErrUserModerationConflict) {
		t.Fatalf("repeated suspension = (%+v, %v), want conflict", repeated, repeatedErr)
	}
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count after conflict = (%d, %v), want 1", auditCount, err)
	}

	reinstatedAt := createdAt.Add(3 * time.Hour)
	reinstateRequestID := pgtype.UUID{Bytes: [16]byte{0x84}, Valid: true}
	reinstated, err := ChangeUserSuspension(ctx, connection, func() time.Time { return reinstatedAt }, moderator,
		memberID, false, "Appeal accepted", reinstateRequestID)
	if err != nil || reinstated.UserID != memberID || reinstated.Suspended || reinstated.AuditID <= suspended.AuditID {
		t.Fatalf("ChangeUserSuspension(reinstate) = (%+v, %v)", reinstated, err)
	}
	if err := connection.QueryRow(ctx, `
SELECT forum_user.suspended_at IS NULL AND forum_user.suspended_until IS NULL AND forum_user.suspension_reason IS NULL,
       forum_user.updated_at,
       action.actor_kind, action.actor_user_id, action.target_type, action.target_user_id,
       action.action_type, action.reason,
       action.previous_state = jsonb_build_object(
           'suspended_at', $3::timestamptz,
           'suspended_until', NULL::timestamptz,
           'suspension_reason', 'Repeated abuse'::text
       ),
       action.resulting_state = jsonb_build_object(
           'suspended_at', NULL::timestamptz,
           'suspended_until', NULL::timestamptz,
           'suspension_reason', NULL::text
       ),
       action.request_id, action.created_at,
       (SELECT count(*) FROM public.moderation_actions)
FROM public.users AS forum_user
JOIN public.moderation_actions AS action ON action.id = $2
WHERE forum_user.id = $1`, memberID, reinstated.AuditID, suspendedAt).Scan(
		&active, &updatedAt, &actorKind, &actorID, &targetType, &targetID, &action, &reason,
		&exactPrevious, &exactResulting, &storedRequestID, &auditedAt, &auditCount,
	); err != nil || !active || !updatedAt.Equal(reinstatedAt) || actorKind != "forum_user" || actorID != moderatorID || targetType != "user" ||
		targetID != memberID || action != "reinstate_user" || reason != "Appeal accepted" || !exactPrevious || !exactResulting ||
		storedRequestID != reinstateRequestID || !auditedAt.Equal(updatedAt) || auditCount != 2 {
		t.Fatalf("persisted reinstatement = (active %t, %s, %q %d, %q %d, %q, %q, exact %t/%t, request %+v, audit %s, count %d, %v)",
			active, updatedAt, actorKind, actorID, targetType, targetID, action, reason,
			exactPrevious, exactResulting, storedRequestID, auditedAt, auditCount, err)
	}
	sessionParams.ObservedAt = pgtype.Timestamptz{Time: reinstatedAt.Add(time.Minute), Valid: true}
	if got, sessionErr := queries.GetActiveSession(ctx, sessionParams); sessionErr != nil || got.UserID != memberID {
		t.Fatalf("reinstated GetActiveSession() = (%+v, %v)", got, sessionErr)
	}
	if status, statusErr := store.GetModerationUserStatus(ctx, queries, moderator, memberID, reinstatedAt.Add(time.Minute)); statusErr != nil || status.Suspended {
		t.Fatalf("reinstated moderation status = (%+v, %v)", status, statusErr)
	}

	if _, err := connection.Exec(ctx, `UPDATE public.users SET role = 'member', updated_at = $1 WHERE id = $2`, reinstatedAt.Add(time.Hour), moderatorID); err != nil {
		t.Fatalf("change moderator role: %v", err)
	}
	if stale, staleErr := ChangeUserSuspension(ctx, connection, func() time.Time { return reinstatedAt.Add(2 * time.Hour) }, moderator,
		memberID, true, "Stale authority", pgtype.UUID{Bytes: [16]byte{0x85}, Valid: true}); stale != (UserSuspensionResult{}) || !errors.Is(staleErr, ErrUserModerationDenied) {
		t.Fatalf("stale moderator authority = (%+v, %v), want denied", stale, staleErr)
	}
	if err := connection.QueryRow(ctx, `
SELECT suspended_at IS NULL AND suspended_until IS NULL AND suspension_reason IS NULL,
       (SELECT count(*) FROM public.moderation_actions)
FROM public.users WHERE id = $1`, memberID).Scan(&active, &auditCount); err != nil || !active || auditCount != 2 {
		t.Fatalf("stale-authority result = (active %t, count %d, %v)", active, auditCount, err)
	}

	var firstAdminID, secondAdminID int64
	if err := connection.QueryRow(ctx, `
INSERT INTO public.users (display_name, role, created_at, updated_at, last_login_at)
VALUES ('First administrator', 'administrator', $1, $1, $1)
RETURNING id`, createdAt).Scan(&firstAdminID); err != nil {
		t.Fatalf("insert first administrator: %v", err)
	}
	if err := connection.QueryRow(ctx, `
INSERT INTO public.users (display_name, role, created_at, updated_at, last_login_at)
VALUES ('Second administrator', 'administrator', $1, $1, $1)
RETURNING id`, createdAt).Scan(&secondAdminID); err != nil {
		t.Fatalf("insert second administrator: %v", err)
	}
	administrator := policy.AccessContext{Authenticated: true, UserID: firstAdminID, Role: policy.RoleAdministrator}
	if status, statusErr := store.GetModerationUserStatus(ctx, queries, administrator, secondAdminID, reinstatedAt.Add(2*time.Hour)); statusErr != nil || status.Role != policy.RoleAdministrator || status.Suspended {
		t.Fatalf("administrator moderation status = (%+v, %v)", status, statusErr)
	}
	if status, statusErr := store.GetModerationUserStatus(ctx, queries, moderator, secondAdminID, reinstatedAt.Add(2*time.Hour)); !errors.Is(statusErr, pgx.ErrNoRows) || status != (store.ModerationUserStatus{}) {
		t.Fatalf("moderator administrator status = (%+v, %v), want zero/no rows", status, statusErr)
	}
	adminSuspendedAt := reinstatedAt.Add(3 * time.Hour)
	adminSuspended, err := ChangeUserSuspension(ctx, connection, func() time.Time { return adminSuspendedAt }, administrator,
		secondAdminID, true, "Administrator departure", pgtype.UUID{Bytes: [16]byte{0x86}, Valid: true})
	if err != nil || adminSuspended.UserID != secondAdminID || !adminSuspended.Suspended || adminSuspended.AuditID <= reinstated.AuditID {
		t.Fatalf("administrator suspension = (%+v, %v)", adminSuspended, err)
	}
	var activeAdministrators int64
	if err := connection.QueryRow(ctx, `
SELECT count(*) FROM public.users
WHERE role = 'administrator'
  AND (suspended_at IS NULL OR suspended_at > $1 OR suspended_until <= $1)`, adminSuspendedAt).Scan(&activeAdministrators); err != nil || activeAdministrators != 1 {
		t.Fatalf("active administrators after suspension = (%d, %v), want 1", activeAdministrators, err)
	}
	if status, statusErr := store.GetModerationUserStatus(ctx, queries, administrator, secondAdminID, adminSuspendedAt.Add(time.Minute)); statusErr != nil || !status.Suspended {
		t.Fatalf("suspended administrator status = (%+v, %v)", status, statusErr)
	}
}
