//go:build integration

package governance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const bootstrapTestDatabase = "gotth_bb_alpha1_governance_bootstrap_test"

func TestBootstrapAdministratorOnPostgreSQL17(t *testing.T) {
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+bootstrapTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale bootstrap test database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+bootstrapTestDatabase); err != nil {
		t.Fatalf("create bootstrap test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+bootstrapTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = bootstrapTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connections := make([]*pgx.Conn, 2)
	for index := range connections {
		connections[index], err = pgx.ConnectConfig(ctx, testConfig)
		if err != nil {
			t.Fatalf("connect bootstrap test database %d: %v", index, err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}

	const issuer = "https://auth.example.test/application/o/gotth-bb/"
	const subject = "first-admin-subject"
	var targetUserID int64
	var targetCreatedAt time.Time
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('First Administrator') RETURNING id, created_at`).Scan(&targetUserID, &targetCreatedAt); err != nil {
		t.Fatalf("insert bootstrap target: %v", err)
	}
	if _, err := connections[0].Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, $2, $3)`, targetUserID, issuer, subject); err != nil {
		t.Fatalf("insert bootstrap identity: %v", err)
	}
	atTime := targetCreatedAt.Add(time.Second).UTC().Truncate(time.Microsecond)
	missing, err := BootstrapAdministrator(
		ctx, connections[0], func() time.Time { return atTime }, issuer, "missing-subject", "operator-missing",
		pgtype.UUID{Bytes: [16]byte{0x01}, Valid: true},
	)
	if err == nil || missing != (BootstrapResult{}) {
		t.Fatalf("missing identity bootstrap = (%+v, %v), want zero/error", missing, err)
	}
	var role string
	var auditCount int
	if err := connections[0].QueryRow(ctx, `SELECT role, (SELECT count(*) FROM public.moderation_actions) FROM public.users WHERE id = $1`, targetUserID).Scan(&role, &auditCount); err != nil || role != "member" || auditCount != 0 {
		t.Fatalf("missing identity state = (role %q, audits %d, %v)", role, auditCount, err)
	}

	start := make(chan struct{})
	results := make(chan BootstrapResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for index, connection := range connections {
		index, connection := index, connection
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, bootstrapErr := BootstrapAdministrator(
				ctx, connection, func() time.Time { return atTime }, issuer, subject,
				"operator-concurrent-"+string(rune('a'+index)),
				pgtype.UUID{Bytes: [16]byte{byte(0x10 + index)}, Valid: true},
			)
			results <- result
			errorsChannel <- bootstrapErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	successes, failures := 0, 0
	var admitted BootstrapResult
	for result := range results {
		if result == (BootstrapResult{}) {
			failures++
		} else {
			successes++
			admitted = result
		}
	}
	transactionErrors := 0
	var concurrentErrors []error
	for bootstrapErr := range errorsChannel {
		if bootstrapErr != nil {
			transactionErrors++
			concurrentErrors = append(concurrentErrors, bootstrapErr)
			if errors.Is(bootstrapErr, context.Canceled) {
				t.Fatalf("concurrent bootstrap canceled: %v", bootstrapErr)
			}
		}
	}
	if successes != 1 || failures != 1 || transactionErrors != 1 || admitted.UserID != targetUserID || admitted.AuditID <= 0 {
		t.Fatalf("concurrent results = (successes %d, failures %d, errors %d %v, admitted %+v)",
			successes, failures, transactionErrors, concurrentErrors, admitted)
	}
	var actorKind, operatorIdentifier, actionType, previousRole, resultingRole string
	var activeAdministrators, finalAudits int
	if err := connections[0].QueryRow(ctx, `SELECT
		(SELECT count(*) FROM public.users WHERE role = 'administrator' AND suspended_at IS NULL),
		(SELECT count(*) FROM public.moderation_actions),
		a.actor_kind, a.operator_identifier, a.action_type,
		a.previous_state->>'role', a.resulting_state->>'role'
		FROM public.moderation_actions AS a WHERE a.id = $1`, admitted.AuditID).Scan(
		&activeAdministrators, &finalAudits, &actorKind, &operatorIdentifier, &actionType, &previousRole, &resultingRole,
	); err != nil || activeAdministrators != 1 || finalAudits != 1 || actorKind != "operator" ||
		(operatorIdentifier != "operator-concurrent-a" && operatorIdentifier != "operator-concurrent-b") ||
		actionType != "bootstrap_administrator" || previousRole != "member" || resultingRole != "administrator" {
		t.Fatalf("admitted bootstrap state = (admins %d, audits %d, actor %q/%q, action %q, roles %q/%q, %v)",
			activeAdministrators, finalAudits, actorKind, operatorIdentifier, actionType, previousRole, resultingRole, err)
	}

	if _, err := connections[0].Exec(ctx, `TRUNCATE public.moderation_actions, public.sessions, public.external_identities, public.users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("reset browser bootstrap scenario: %v", err)
	}
	var browserUserID, browserSessionID int64
	var browserCreatedAt time.Time
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Browser Administrator') RETURNING id, created_at`).Scan(&browserUserID, &browserCreatedAt); err != nil {
		t.Fatalf("insert browser bootstrap target: %v", err)
	}
	if _, err := connections[0].Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, $2, $3)`, browserUserID, issuer, subject); err != nil {
		t.Fatalf("insert browser bootstrap identity: %v", err)
	}
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.sessions (token_hash, user_id, issued_at, last_seen_at, validated_at, expires_at)
		VALUES ($1, $2, $3, $3, $3, $4) RETURNING id`, bytes.Repeat([]byte{0x71}, 32), browserUserID, browserCreatedAt, browserCreatedAt.Add(time.Hour)).Scan(&browserSessionID); err != nil {
		t.Fatalf("insert browser bootstrap session: %v", err)
	}
	browserAtTime := browserCreatedAt.Add(time.Second).UTC().Truncate(time.Microsecond)
	browserResult, err := ClaimInitialAdministrator(
		ctx, connections[0], func() time.Time { return browserAtTime }, browserUserID, browserSessionID, issuer, subject,
		pgtype.UUID{Bytes: [16]byte{0x50}, Valid: true},
	)
	if err != nil || browserResult.UserID != browserUserID || browserResult.AuditID <= 0 || browserResult.RevokedSessionID != browserSessionID {
		t.Fatalf("browser administrator claim = (%+v, %v)", browserResult, err)
	}
	var browserRole, browserActorKind, browserAction, browserPreviousRole, browserResultingRole string
	var browserAuditActor, browserAuditTarget int64
	var revokedAt *time.Time
	if err := connections[0].QueryRow(ctx, `SELECT u.role, s.revoked_at, a.actor_kind, a.actor_user_id,
		a.target_user_id, a.action_type, a.previous_state->>'role', a.resulting_state->>'role'
		FROM public.users AS u
		JOIN public.sessions AS s ON s.user_id = u.id
		JOIN public.moderation_actions AS a ON a.target_user_id = u.id
		WHERE u.id = $1`, browserUserID).Scan(
		&browserRole, &revokedAt, &browserActorKind, &browserAuditActor, &browserAuditTarget,
		&browserAction, &browserPreviousRole, &browserResultingRole,
	); err != nil || browserRole != "administrator" || revokedAt == nil || !revokedAt.Equal(browserAtTime) ||
		browserActorKind != "forum_user" || browserAuditActor != browserUserID || browserAuditTarget != browserUserID ||
		browserAction != "bootstrap_administrator" || browserPreviousRole != "member" || browserResultingRole != "administrator" {
		t.Fatalf("browser bootstrap state = (role %q, revoked %v, actor %q/%d, target %d, action %q, roles %q/%q, %v)",
			browserRole, revokedAt, browserActorKind, browserAuditActor, browserAuditTarget, browserAction, browserPreviousRole, browserResultingRole, err)
	}
	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET role = 'member' WHERE id = $1`, browserUserID); err != nil {
		t.Fatalf("simulate administrator-loss repair state: %v", err)
	}
	second, err := BootstrapAdministrator(
		ctx, connections[0], func() time.Time { return browserAtTime.Add(time.Second) }, issuer, subject, "operator-after-browser",
		pgtype.UUID{Bytes: [16]byte{0x51}, Valid: true},
	)
	if !errors.Is(err, ErrAdministratorSetupClosed) || second != (BootstrapResult{}) {
		t.Fatalf("bootstrap after historical browser claim = (%+v, %v), want permanently closed", second, err)
	}
}
