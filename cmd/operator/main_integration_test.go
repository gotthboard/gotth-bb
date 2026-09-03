//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/governance"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const operatorTestDatabase = "gotth_bb_alpha1_operator_command_test"

func TestOperatorBootstrapCommandOnPostgreSQL17(t *testing.T) {
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
}
