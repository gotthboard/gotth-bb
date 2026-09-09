//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/governance"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const operatorTestDatabase = "gotth_bb_alpha1_operator_command_test"

func TestOperatorGovernanceCommandsOnPostgreSQL17(t *testing.T) {
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+operatorTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale operator test database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+operatorTestDatabase); err != nil {
		t.Fatalf("create operator test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+operatorTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = operatorTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	setup, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect operator test database: %v", err)
	}
	t.Cleanup(func() { _ = setup.Close(context.Background()) })

	const issuer = "https://auth.example.test/application/o/gotth-bb/"
	const subject = "operator-command-subject"
	var userID int64
	var createdAt time.Time
	if err := setup.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Operator Command Administrator') RETURNING id, created_at`).Scan(&userID, &createdAt); err != nil {
		t.Fatalf("insert operator target: %v", err)
	}
	if _, err := setup.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, $2, $3)`, userID, issuer, subject); err != nil {
		t.Fatalf("insert operator identity: %v", err)
	}

	testURL, err := url.Parse(databaseURL)
	if err != nil || testURL.Scheme != "postgres" && testURL.Scheme != "postgresql" {
		t.Fatalf("integration DATABASE_URL must use PostgreSQL URL syntax: %v", err)
	}
	testURL.Path = "/" + operatorTestDatabase
	args := []string{"bootstrap-administrator", "--issuer", issuer, "--subject", subject, "--operator", "integration-operator"}
	lookup := operatorMapLookup(map[string]string{"DATABASE_URL": testURL.String()})
	connect := func(connectContext context.Context, configured *pgx.ConnConfig) (operatorConnection, error) {
		return pgx.ConnectConfig(connectContext, configured)
	}
	bootstrap := func(bootstrapContext context.Context, database operatorConnection, clock func() time.Time, exactIssuer, exactSubject, operatorIdentifier string, requestID pgtype.UUID) (governance.BootstrapResult, error) {
		return governance.BootstrapAdministrator(bootstrapContext, database, clock, exactIssuer, exactSubject, operatorIdentifier, requestID)
	}
	clock := func() time.Time { return createdAt.Add(time.Second) }
	var output bytes.Buffer
	if err := run(ctx, lookup, args, &output, bytes.NewReader(bytes.Repeat([]byte{0x27}, 16)), clock, connect, bootstrap); err != nil {
		t.Fatalf("run() returned error: %v", err)
	}
	var role, actorKind, operatorIdentifier, actionType string
	var auditID int64
	var auditCount int
	if err := setup.QueryRow(ctx, `SELECT u.role,
		(SELECT count(*) FROM public.moderation_actions),
		a.id, a.actor_kind, a.operator_identifier, a.action_type
		FROM public.users AS u
		JOIN public.moderation_actions AS a ON a.target_user_id = u.id
		WHERE u.id = $1`, userID).Scan(&role, &auditCount, &auditID, &actorKind, &operatorIdentifier, &actionType); err != nil ||
		role != "administrator" || auditCount != 1 || actorKind != "operator" || operatorIdentifier != "integration-operator" || actionType != "bootstrap_administrator" {
		t.Fatalf("operator state = (role %q, audits %d/%d, actor %q/%q, action %q, %v)", role, auditCount, auditID, actorKind, operatorIdentifier, actionType, err)
	}
	wantOutput := fmt.Sprintf("administrator bootstrap committed: user_id=%d audit_id=%d\n", userID, auditID)
	if output.String() != wantOutput {
		t.Fatalf("run() output = %q, want %q", output.String(), wantOutput)
	}

	output.Reset()
	err = run(ctx, lookup, args, &output, bytes.NewReader(bytes.Repeat([]byte{0x28}, 16)), clock, connect, bootstrap)
	if err == nil || output.Len() != 0 {
		t.Fatalf("second run = (output %q, error %v), want no output/error", output.String(), err)
	}
	if err := setup.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("second run audit count = (%d, %v), want one", auditCount, err)
	}

	if _, err := setup.Exec(ctx, `INSERT INTO public.sessions
		(token_hash, user_id, issued_at, last_seen_at, validated_at, expires_at)
		VALUES ($1, $3, $4, $4, $4, $5), ($2, $3, $4, $4, $4, $5)`,
		bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x42}, 32), userID, createdAt, createdAt.Add(time.Hour)); err != nil {
		t.Fatalf("insert sessions before identity rebind: %v", err)
	}
	if _, err := setup.Exec(ctx, `INSERT INTO public.oidc_login_attempts
		(state_hash, nonce_ciphertext, pkce_verifier_ciphertext, purpose, return_path, created_at, expires_at)
		VALUES ($1, $2, $3, 'login', '/', $4, $5)`,
		bytes.Repeat([]byte{0x43}, 32), []byte{0x44}, []byte{0x45}, createdAt, createdAt.Add(time.Hour)); err != nil {
		t.Fatalf("insert pending login before identity rebind: %v", err)
	}
	const replacementIssuer = "https://auth.board.example.test/application/o/gotth-bb/"
	const replacementSubject = "replacement-user-uuid"
	rebind := func(rebindContext context.Context, database operatorConnection, exactClock func() time.Time, oldIssuer, oldSubject, newIssuer, newSubject, operatorIdentifier string, requestID pgtype.UUID) (governance.IdentityRebindResult, error) {
		return governance.RebindExternalIdentity(
			rebindContext, database, exactClock,
			oldIssuer, oldSubject, newIssuer, newSubject, operatorIdentifier, requestID,
		)
	}
	rebindArgs := []string{
		"rebind-external-identity",
		"--old-issuer", issuer, "--old-subject", subject,
		"--new-issuer", replacementIssuer, "--new-subject", replacementSubject,
		"--operator", "integration-operator",
	}
	clock = func() time.Time { return createdAt.Add(2 * time.Second) }
	output.Reset()
	if err := runOperator(
		ctx, lookup, rebindArgs, &output, bytes.NewReader(bytes.Repeat([]byte{0x29}, 16)),
		clock, connect, bootstrap, rebind,
	); err != nil {
		t.Fatalf("runOperator(rebind) returned error: %v", err)
	}
	var reboundIssuer, reboundSubject, rebindAction, previousState, resultingState string
	var revokedSessions, pendingAttempts int
	if err := setup.QueryRow(ctx, `SELECT identity.issuer, identity.subject,
		(SELECT count(*) FROM public.sessions WHERE user_id = $1 AND revoked_at IS NOT NULL),
		(SELECT count(*) FROM public.oidc_login_attempts WHERE consumed_at IS NULL),
		action.action_type, action.previous_state::text, action.resulting_state::text
		FROM public.external_identities AS identity
		JOIN public.moderation_actions AS action ON action.target_user_id = identity.user_id
		WHERE identity.user_id = $1 AND action.action_type = 'rebind_external_identity'`, userID).Scan(
		&reboundIssuer, &reboundSubject, &revokedSessions, &pendingAttempts,
		&rebindAction, &previousState, &resultingState,
	); err != nil || reboundIssuer != replacementIssuer || reboundSubject != replacementSubject ||
		revokedSessions != 2 || pendingAttempts != 0 || rebindAction != "rebind_external_identity" ||
		strings.Contains(previousState, issuer) || strings.Contains(previousState, subject) ||
		strings.Contains(resultingState, replacementIssuer) || strings.Contains(resultingState, replacementSubject) {
		t.Fatalf("rebind state = (%q, %q, sessions %d, attempts %d, action %q, previous %q, resulting %q, %v)",
			reboundIssuer, reboundSubject, revokedSessions, pendingAttempts, rebindAction, previousState, resultingState, err)
	}
	if !strings.Contains(output.String(), fmt.Sprintf("user_id=%d", userID)) ||
		!strings.Contains(output.String(), "revoked_sessions=2 discarded_login_attempts=1") {
		t.Fatalf("runOperator(rebind) output = %q", output.String())
	}
	output.Reset()
	if err := runOperator(
		ctx, lookup, rebindArgs, &output, bytes.NewReader(bytes.Repeat([]byte{0x30}, 16)),
		clock, connect, bootstrap, rebind,
	); err == nil || output.Len() != 0 {
		t.Fatalf("second rebind = (output %q, error %v), want no output/error", output.String(), err)
	}
	if err := setup.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions`).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("second rebind audit count = (%d, %v), want two", auditCount, err)
	}
}
