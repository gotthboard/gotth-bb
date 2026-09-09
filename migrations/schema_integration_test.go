//go:build integration

package migrations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
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
	if serverVersion != 170010 || migrationCount != 13 || governanceCount != 1 {
		t.Fatalf("schema state = (version %d, migrations %d, governance %d), want (170010, 13, 1)", serverVersion, migrationCount, governanceCount)
	}
	var finiteConstraintValidated bool
	var finiteConstraintDefinition string
	if err := conn.QueryRow(ctx, `SELECT constraint_row.convalidated, pg_get_constraintdef(constraint_row.oid, true)
FROM pg_catalog.pg_constraint AS constraint_row
WHERE constraint_row.conrelid = 'public.topic_reads'::regclass
  AND constraint_row.conname = 'topic_reads_read_at_finite'`).Scan(&finiteConstraintValidated, &finiteConstraintDefinition); err != nil {
		t.Fatalf("inspect unread finite-time constraint: %v", err)
	}
	if !finiteConstraintValidated || finiteConstraintDefinition != "CHECK (isfinite(read_at))" {
		t.Fatalf("unread finite-time constraint = (%t, %q)", finiteConstraintValidated, finiteConstraintDefinition)
	}
	var unreadIndexValid, unreadIndexReady, unreadIndexUnique bool
	var unreadIndexDefinition, unreadIndexPredicate string
	if err := conn.QueryRow(ctx, `SELECT index_row.indisvalid, index_row.indisready, index_row.indisunique,
       pg_get_indexdef(index_row.indexrelid), pg_get_expr(index_row.indpred, index_row.indrelid)
FROM pg_catalog.pg_index AS index_row
WHERE index_row.indexrelid = 'public.posts_topic_unread_visible_idx'::regclass`).Scan(
		&unreadIndexValid, &unreadIndexReady, &unreadIndexUnique, &unreadIndexDefinition, &unreadIndexPredicate,
	); err != nil {
		t.Fatalf("inspect unread visible-post index: %v", err)
	}
	if !unreadIndexValid || !unreadIndexReady || unreadIndexUnique ||
		unreadIndexDefinition != "CREATE INDEX posts_topic_unread_visible_idx ON public.posts USING btree (topic_id, post_number) INCLUDE (author_id) WHERE ((deleted_at IS NULL) AND (redacted_at IS NULL))" ||
		unreadIndexPredicate != "((deleted_at IS NULL) AND (redacted_at IS NULL))" {
		t.Fatalf("unread visible-post index = (%t, %t, %t, %q, %q)", unreadIndexValid, unreadIndexReady, unreadIndexUnique, unreadIndexDefinition, unreadIndexPredicate)
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
	var registrationMode, maintenanceMessage string
	var maintenanceEnabled bool
	var publishLimit, newAccountLimit, publishWindow, newAccountPeriod, sessionIdle, authRevalidate int32
	if err := conn.QueryRow(ctx, `SELECT registration_mode, maintenance_enabled,
       maintenance_message, publish_rate_limit, new_account_publish_rate_limit,
       publish_window_seconds, new_account_period_seconds, session_idle_seconds,
       auth_revalidate_seconds
FROM public.site_settings WHERE singleton`).Scan(
		&registrationMode, &maintenanceEnabled, &maintenanceMessage,
		&publishLimit, &newAccountLimit, &publishWindow, &newAccountPeriod,
		&sessionIdle, &authRevalidate,
	); err != nil {
		t.Fatalf("load control-setting defaults: %v", err)
	}
	if registrationMode != "closed" || maintenanceEnabled || maintenanceMessage != "" ||
		publishLimit != 10 || newAccountLimit != 3 || publishWindow != 600 ||
		newAccountPeriod != 86400 || sessionIdle != 28800 || authRevalidate != 1800 {
		t.Fatalf("control-setting defaults = (%q, %t, %q, %d, %d, %d, %d, %d, %d)",
			registrationMode, maintenanceEnabled, maintenanceMessage, publishLimit,
			newAccountLimit, publishWindow, newAccountPeriod, sessionIdle, authRevalidate)
	}
	var administratorSync, memberSync string
	if err := conn.QueryRow(ctx, `SELECT
    (SELECT authentik_sync_state FROM public.users WHERE id = $1),
    (SELECT authentik_sync_state FROM public.users WHERE id = $2)`, administratorID, memberID).Scan(&administratorSync, &memberSync); err != nil {
		t.Fatalf("load fresh JIT sync states: %v", err)
	}
	if administratorSync != "unknown" || memberSync != "unknown" {
		t.Fatalf("fresh JIT sync states = (%q, %q), want unknown/unknown", administratorSync, memberSync)
	}
	expectExecutionFailure(t, conn, ctx, `UPDATE public.site_settings SET registration_mode = 'open' WHERE singleton`)
	expectExecutionFailure(t, conn, ctx, `UPDATE public.site_settings SET new_account_publish_rate_limit = publish_rate_limit + 1 WHERE singleton`)
	expectExecutionFailure(t, conn, ctx, `UPDATE public.site_settings SET maintenance_message = E'bad\nmessage' WHERE singleton`)
	if _, err := conn.Exec(ctx, `INSERT INTO public.pending_registrations
    (authentik_user_id, authentik_subject, display_name, verified_email)
VALUES (17, '00000000-0000-0000-0000-000000000017', 'Pending user', 'pending@example.test')`); err != nil {
		t.Fatalf("insert pending registration: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.pending_registrations
    (authentik_user_id, authentik_subject, display_name, verified_email)
VALUES (17, '00000000-0000-0000-0000-000000000018', 'Duplicate numeric ID', 'duplicate@example.test')`)
	if _, err := conn.Exec(ctx, `INSERT INTO public.registration_invitations
    (idempotency_key, authentik_invitation_name, flow_identity, expires_at,
     created_by, request_fingerprint)
VALUES ('00000000-0000-0000-0000-000000000019', 'board-19', 'board-invitation',
        clock_timestamp() + interval '1 day', $1, decode(repeat('19', 32), 'hex'))`, administratorID); err != nil {
		t.Fatalf("insert registration invitation: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.registration_invitations
    (idempotency_key, authentik_invitation_name, flow_identity, expires_at,
     created_by, request_fingerprint)
VALUES ('00000000-0000-0000-0000-000000000020', 'board-20', 'board-invitation',
        clock_timestamp() + interval '1 day', $1, decode('20', 'hex'))`, administratorID)
	if _, err := conn.Exec(ctx, `INSERT INTO public.email_test_state
    (administrator_id, idempotency_key, status, requested_at, next_allowed_at)
VALUES ($1, '00000000-0000-0000-0000-000000000021', 'requested',
        '2026-09-09T12:00:00Z', '2026-09-09T12:05:00Z')`, administratorID); err != nil {
		t.Fatalf("insert email test reservation: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `UPDATE public.email_test_state
SET next_allowed_at = requested_at + interval '4 minutes'
WHERE administrator_id = $1`, administratorID)
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
	attemptNow := time.Now().UTC().Truncate(time.Microsecond)
	insertAttempt := func(stateByte byte, createdAt, expiresAt time.Time) {
		t.Helper()
		if err := queries.InsertOIDCLoginAttempt(ctx, db.InsertOIDCLoginAttemptParams{
			StateHash:              bytes.Repeat([]byte{stateByte}, 32),
			NonceCiphertext:        bytes.Repeat([]byte{stateByte + 1}, 72),
			PkceVerifierCiphertext: bytes.Repeat([]byte{stateByte + 2}, 72),
			Purpose:                "login",
			ReturnPath:             "/community/board/",
			CreatedAt:              pgtype.Timestamptz{Time: createdAt, Valid: true},
			ExpiresAt:              pgtype.Timestamptz{Time: expiresAt, Valid: true},
		}); err != nil {
			t.Fatalf("InsertOIDCLoginAttempt(%x) returned error: %v", stateByte, err)
		}
	}
	insertAttempt(0xcc, attemptNow, attemptNow.Add(5*time.Minute))
	consumers := make([]*pgx.Conn, 2)
	for index := range consumers {
		consumers[index], err = pgx.ConnectConfig(ctx, testConfig)
		if err != nil {
			t.Fatalf("connect login-attempt consumer %d: %v", index, err)
		}
		consumer := consumers[index]
		t.Cleanup(func() {
			if err := consumer.Close(context.Background()); err != nil {
				t.Errorf("close login-attempt consumer: %v", err)
			}
		})
	}
	startConsume := make(chan struct{})
	type consumeResult struct {
		attempt db.OidcLoginAttempt
		err     error
	}
	consumeResults := make(chan consumeResult, len(consumers))
	var consumeWait sync.WaitGroup
	for _, consumer := range consumers {
		consumer := consumer
		consumeWait.Add(1)
		go func() {
			defer consumeWait.Done()
			<-startConsume
			attempt, consumeErr := db.New(consumer).ConsumeOIDCLoginAttempt(ctx, db.ConsumeOIDCLoginAttemptParams{
				StateHash:  bytes.Repeat([]byte{0xcc}, 32),
				ConsumedAt: pgtype.Timestamptz{Time: attemptNow.Add(time.Second), Valid: true},
			})
			consumeResults <- consumeResult{attempt: attempt, err: consumeErr}
		}()
	}
	close(startConsume)
	consumeWait.Wait()
	close(consumeResults)
	consumeSuccesses := 0
	consumeMisses := 0
	for result := range consumeResults {
		switch {
		case result.err == nil:
			consumeSuccesses++
			if !bytes.Equal(result.attempt.StateHash, bytes.Repeat([]byte{0xcc}, 32)) ||
				!bytes.Equal(result.attempt.NonceCiphertext, bytes.Repeat([]byte{0xcd}, 72)) ||
				!bytes.Equal(result.attempt.PkceVerifierCiphertext, bytes.Repeat([]byte{0xce}, 72)) ||
				result.attempt.Purpose != "login" || result.attempt.SessionID.Valid ||
				result.attempt.ReturnPath != "/community/board/" ||
				!result.attempt.CreatedAt.Valid || !result.attempt.CreatedAt.Time.Equal(attemptNow) ||
				!result.attempt.ExpiresAt.Valid || !result.attempt.ExpiresAt.Time.Equal(attemptNow.Add(5*time.Minute)) ||
				!result.attempt.ConsumedAt.Valid || !result.attempt.ConsumedAt.Time.Equal(attemptNow.Add(time.Second)) {
				t.Fatal("successful consume returned the wrong login-attempt row")
			}
		case errors.Is(result.err, pgx.ErrNoRows):
			consumeMisses++
		default:
			t.Fatalf("ConsumeOIDCLoginAttempt() returned unexpected error: %v", result.err)
		}
	}
	if consumeSuccesses != 1 || consumeMisses != 1 {
		t.Fatalf("concurrent consume results = (%d success, %d miss), want (1, 1)", consumeSuccesses, consumeMisses)
	}
	insertAttempt(0xdd, attemptNow.Add(-10*time.Minute), attemptNow.Add(-5*time.Minute))
	insertAttempt(0xee, attemptNow.Add(5*time.Minute), attemptNow.Add(10*time.Minute))
	for _, stateByte := range []byte{0xcc, 0xdd, 0xee} {
		if _, err := queries.ConsumeOIDCLoginAttempt(ctx, db.ConsumeOIDCLoginAttemptParams{
			StateHash:  bytes.Repeat([]byte{stateByte}, 32),
			ConsumedAt: pgtype.Timestamptz{Time: attemptNow.Add(2 * time.Second), Valid: true},
		}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("replayed, expired, or future consume %x error = %v, want no rows", stateByte, err)
		}
	}
	var consumedAttempts int
	var unconsumedAttempts int
	if err := conn.QueryRow(ctx, `SELECT
    count(*) FILTER (WHERE consumed_at IS NOT NULL),
    count(*) FILTER (WHERE consumed_at IS NULL)
FROM public.oidc_login_attempts
WHERE get_byte(state_hash, 0) IN (204, 221, 238)`).Scan(&consumedAttempts, &unconsumedAttempts); err != nil {
		t.Fatalf("inspect login-attempt consumption: %v", err)
	}
	if consumedAttempts != 1 || unconsumedAttempts != 2 {
		t.Fatalf("login-attempt state = (%d consumed, %d unconsumed), want (1, 2)", consumedAttempts, unconsumedAttempts)
	}
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
	if _, err := conn.Exec(ctx, "UPDATE public.users SET role = 'moderator' WHERE id = $1", transactionUserID); err != nil {
		t.Fatalf("set local role before OIDC refresh: %v", err)
	}
	refreshTime := pgtype.Timestamptz{Time: transactionTime.Time.Add(time.Minute).UTC().Truncate(time.Microsecond), Valid: true}
	refreshEmail := pgtype.Text{String: "member@example.test", Valid: true}
	refreshAvatar := pgtype.Text{String: "https://auth.example.test/avatar.png", Valid: true}
	sessionTokenHash := bytes.Repeat([]byte{0xf1}, 32)
	sessionUserAgentHash := bytes.Repeat([]byte{0xf2}, 32)
	sessionIP := netip.MustParseAddr("192.0.2.42")
	var insertedSession db.InsertSessionRow
	if err := store.WithinTx(ctx, conn, func(transactionQueries *db.Queries) error {
		locked, err := transactionQueries.LockExternalIdentity(ctx, db.LockExternalIdentityParams{
			Issuer: "https://auth.example.test/application/o/forum/", Subject: "transaction-subject",
		})
		if err != nil || !locked {
			return fmt.Errorf("lock external identity: locked=%t: %w", locked, err)
		}
		updated, err := transactionQueries.UpdateUserFromOIDC(ctx, db.UpdateUserFromOIDCParams{
			DisplayName: "Refreshed Member", Email: refreshEmail, AvatarUrl: refreshAvatar,
			LoginAt: refreshTime, UserID: transactionUserID,
		})
		if err != nil {
			return err
		}
		if updated.Role != "moderator" || updated.DisplayName != "Refreshed Member" {
			return fmt.Errorf("OIDC refresh changed local role or missed profile")
		}
		if err := transactionQueries.UpdateExternalIdentityVerification(ctx, db.UpdateExternalIdentityVerificationParams{
			VerifiedAt: refreshTime, UserID: transactionUserID,
		}); err != nil {
			return err
		}
		insertedSession, err = transactionQueries.InsertSession(ctx, db.InsertSessionParams{
			TokenHash: sessionTokenHash, UserID: transactionUserID, IssuedAt: refreshTime,
			ExpiresAt:     pgtype.Timestamptz{Time: refreshTime.Time.Add(24 * time.Hour), Valid: true},
			UserAgentHash: sessionUserAgentHash, IpPrefix: &sessionIP,
		})
		return err
	}); err != nil {
		t.Fatalf("WithinTx() OIDC refresh/session: %v", err)
	}
	if insertedSession.ID == 0 || insertedSession.UserID != transactionUserID {
		t.Fatal("InsertSession() returned incorrect session state")
	}
	var storedSessionValid bool
	if err := conn.QueryRow(ctx, `SELECT
		token_hash = $2 AND user_agent_hash = $3 AND ip_prefix = $4
		AND issued_at = $5 AND last_seen_at = $5 AND validated_at = $5
		AND expires_at = $6 AND revoked_at IS NULL
		FROM public.sessions WHERE id = $1`, insertedSession.ID, sessionTokenHash,
		sessionUserAgentHash, sessionIP, refreshTime, refreshTime.Time.Add(24*time.Hour)).Scan(&storedSessionValid); err != nil || !storedSessionValid {
		t.Fatalf("stored InsertSession() state = (%t, %v)", storedSessionValid, err)
	}
	var verifiedAt time.Time
	if err := conn.QueryRow(ctx, "SELECT last_verified_at FROM public.external_identities WHERE user_id = $1", transactionUserID).Scan(&verifiedAt); err != nil || !verifiedAt.Equal(refreshTime.Time) {
		t.Fatalf("external identity verification time = (%s, %v)", verifiedAt, err)
	}
	lockTx, err := consumers[0].Begin(ctx)
	if err != nil {
		t.Fatalf("begin identity lock holder: %v", err)
	}
	lockParams := db.LockExternalIdentityParams{Issuer: "https://auth.example.test/application/o/forum/", Subject: "contended-subject"}
	if locked, err := db.New(lockTx).LockExternalIdentity(ctx, lockParams); err != nil || !locked {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("acquire identity lock holder = (%t, %v)", locked, err)
	}
	contenderTx, err := consumers[1].Begin(ctx)
	if err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("begin identity lock contender: %v", err)
	}
	if _, err := contenderTx.Exec(ctx, "SET LOCAL lock_timeout = '100ms'"); err != nil {
		_ = contenderTx.Rollback(ctx)
		_ = lockTx.Rollback(ctx)
		t.Fatalf("set identity lock timeout: %v", err)
	}
	if _, err := db.New(contenderTx).LockExternalIdentity(ctx, lockParams); err == nil {
		_ = contenderTx.Rollback(ctx)
		_ = lockTx.Rollback(ctx)
		t.Fatal("same external identity lock did not contend")
	}
	if err := contenderTx.Rollback(ctx); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("rollback identity lock contender: %v", err)
	}
	independentTx, err := consumers[1].Begin(ctx)
	if err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("begin independent identity lock: %v", err)
	}
	if locked, err := db.New(independentTx).LockExternalIdentity(ctx, db.LockExternalIdentityParams{
		Issuer: lockParams.Issuer, Subject: "independent-subject",
	}); err != nil || !locked {
		_ = independentTx.Rollback(ctx)
		_ = lockTx.Rollback(ctx)
		t.Fatalf("independent identity lock = (%t, %v)", locked, err)
	}
	if err := independentTx.Commit(ctx); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("commit independent identity lock: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("commit identity lock holder: %v", err)
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
VALUES ($1, $2, $3, 1, 'First post', '<p>First post</p>', $4)`, firstPostID, topicID, memberID, contentrender.RendererVersion); err != nil {
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
VALUES ($1, $2, 2, 'Reply', '<p>Reply</p>', $3) RETURNING id`, topicID, memberID, contentrender.RendererVersion).Scan(&replyID); err != nil {
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
	var rootParent *int64
	var rootPath []int32
	var replyParent int64
	var replyPath []int32
	if err := conn.QueryRow(ctx, `SELECT parent_post_id, thread_path FROM public.posts WHERE id = $1`, firstPostID).Scan(&rootParent, &rootPath); err != nil {
		t.Fatalf("inspect root thread metadata: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT parent_post_id, thread_path FROM public.posts WHERE id = $1`, replyID).Scan(&replyParent, &replyPath); err != nil {
		t.Fatalf("inspect alpha.1-compatible reply metadata: %v", err)
	}
	if rootParent != nil || !reflect.DeepEqual(rootPath, []int32{1}) || replyParent != firstPostID || !reflect.DeepEqual(replyPath, []int32{1, 2}) {
		t.Fatalf("thread metadata = (root parent %v path %v, reply parent %d path %v)", rootParent, rootPath, replyParent, replyPath)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.posts
    (topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version)
VALUES ($1, $2, 2, 'Duplicate', '<p>Duplicate</p>', $3)`, topicID, memberID, contentrender.RendererVersion)
	expectExecutionFailure(t, conn, ctx, "UPDATE public.posts SET post_number = 3 WHERE id = $1", replyID)
	expectExecutionFailure(t, conn, ctx, "UPDATE public.posts SET parent_post_id = NULL WHERE id = $1", replyID)
	expectExecutionFailure(t, conn, ctx, "UPDATE public.posts SET thread_path = ARRAY[1, 99] WHERE id = $1", replyID)

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
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.topic_reads
    (user_id, topic_id, last_read_post_number, read_at) VALUES ($1, $2, 1, 'infinity')`, memberID, topicID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.topic_reads
    (user_id, topic_id, last_read_post_number, read_at) VALUES ($1, $2, 1, '-infinity')`, memberID, topicID)
	if _, err := conn.Exec(ctx, `INSERT INTO public.topic_reads
    (user_id, topic_id, last_read_post_number, read_at) VALUES ($1, $2, 1, $3)`, memberID, topicID, time.Now().UTC()); err != nil {
		t.Fatalf("insert finite topic read: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.reports
    (reported_by, topic_id, user_id, reason) VALUES ($1, $2, $1, 'two targets')`, memberID, topicID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.reports
    (reported_by, topic_id, reason, status, assigned_to)
VALUES ($1, $2, 'assigned open report', 'open', $1)`, memberID, topicID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.reports
    (reported_by, topic_id, reason, status)
VALUES ($1, $2, 'unassigned review report', 'in_review')`, memberID, topicID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.moderation_actions
    (actor_kind, target_type, target_user_id, action_type, request_id)
VALUES ('forum_user', 'user', $1, 'warn_user', '00000000-0000-0000-0000-000000000001')`, memberID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.moderation_actions
    (actor_kind, actor_user_id, target_type, target_site, action_type, request_id)
VALUES ('forum_user', $1, 'site', true, 'update_control_settings',
        '00000000-0000-0000-0000-000000000022')`, administratorID)
	if _, err := conn.Exec(ctx, `INSERT INTO public.moderation_actions
    (actor_kind, actor_user_id, target_type, target_site, action_type, reason, request_id)
VALUES ('forum_user', $1, 'site', true, 'update_control_settings', 'test controls',
        '00000000-0000-0000-0000-000000000023')`, administratorID); err != nil {
		t.Fatalf("insert control-setting audit: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.moderation_actions
    (actor_kind, actor_user_id, target_type, target_topic_id, action_type, reason, request_id)
VALUES ('forum_user', $1, 'topic', $2, 'lock_topic', NULL,
        '00000000-0000-0000-0000-000000000002')`, administratorID, topicID); err != nil {
		t.Fatalf("insert valid moderation action: %v", err)
	}
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.report_notes (report_id, author_id, body) VALUES (999, $1, '')`, administratorID)
	expectExecutionFailure(t, conn, ctx, `INSERT INTO public.user_warnings (user_id, warned_by, reason) VALUES ($1, $1, '')`, memberID)
	expectExecutionFailure(t, conn, ctx, `UPDATE public.posts SET redacted_at = clock_timestamp(), redacted_by = $1, redaction_reason = 'test' WHERE id = $2`, administratorID, replyID)
}
