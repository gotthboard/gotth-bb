//go:build integration

package governance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	runtimePrivilegeTestDatabase = "gotth_bb_alpha1_runtime_privilege_test"
	runtimePrivilegeTestRole     = "gotth_bb_alpha1_runtime_privilege"
)

func TestInitialAdministratorClaimWithRestrictedRuntimeRoleOnPostgreSQL17(t *testing.T) {
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
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+runtimePrivilegeTestDatabase+" WITH (FORCE)")
	_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+runtimePrivilegeTestRole)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+runtimePrivilegeTestRole+" NOLOGIN"); err != nil {
		t.Fatalf("create restricted runtime role: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP ROLE IF EXISTS "+runtimePrivilegeTestRole)
	})
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+runtimePrivilegeTestDatabase); err != nil {
		t.Fatalf("create restricted runtime database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+runtimePrivilegeTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = runtimePrivilegeTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect restricted runtime database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	roleIdentifier := pgx.Identifier{runtimePrivilegeTestRole}.Sanitize()
	baselineGrants := `
GRANT USAGE ON SCHEMA public TO ` + roleIdentifier + `;
GRANT SELECT ON public.governance_state TO ` + roleIdentifier + `;
GRANT SELECT, UPDATE ON public.users TO ` + roleIdentifier + `;
GRANT SELECT, UPDATE ON public.external_identities TO ` + roleIdentifier + `;
GRANT SELECT, UPDATE ON public.sessions TO ` + roleIdentifier + `;
GRANT SELECT, INSERT ON public.moderation_actions TO ` + roleIdentifier + `;
GRANT USAGE, SELECT ON SEQUENCE public.moderation_actions_id_seq TO ` + roleIdentifier + `;`
	if _, err := connection.Exec(ctx, baselineGrants); err != nil {
		t.Fatalf("grant baseline runtime privileges: %v", err)
	}

	const issuer = "https://auth.example.test/application/o/gotth-bb/"
	const subject = "restricted-runtime-subject"
	var userID, sessionID int64
	var createdAt time.Time
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Restricted Runtime Administrator') RETURNING id, created_at`).Scan(&userID, &createdAt); err != nil {
		t.Fatalf("insert restricted runtime target: %v", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.external_identities (user_id, issuer, subject) VALUES ($1, $2, $3)`, userID, issuer, subject); err != nil {
		t.Fatalf("insert restricted runtime identity: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.sessions (token_hash, user_id, issued_at, last_seen_at, validated_at, expires_at)
		VALUES ($1, $2, $3, $3, $3, $4) RETURNING id`, bytes.Repeat([]byte{0x72}, 32), userID, createdAt, createdAt.Add(time.Hour)).Scan(&sessionID); err != nil {
		t.Fatalf("insert restricted runtime session: %v", err)
	}
	atTime := createdAt.Add(time.Second).UTC().Truncate(time.Microsecond)

	if _, err := connection.Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("assume restricted runtime role: %v", err)
	}
	denied, claimErr := ClaimInitialAdministrator(
		ctx, connection, func() time.Time { return atTime }, userID, sessionID, issuer, subject,
		pgtype.UUID{Bytes: [16]byte{0x61}, Valid: true},
	)
	if _, err := connection.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("reset restricted runtime role: %v", err)
	}
	var postgresError *pgconn.PgError
	if denied != (InitialAdministratorClaimResult{}) || !errors.As(claimErr, &postgresError) || postgresError.Code != "42501" {
		t.Fatalf("pre-grant claim = (%+v, %v), want zero/SQLSTATE 42501", denied, claimErr)
	}
	assertAdministratorClaimState(t, ctx, connection, userID, sessionID, "member", false, 0)

	grantTemplate, err := os.ReadFile("../../deploy/postgresql/runtime-grants.sql")
	if err != nil {
		t.Fatalf("read runtime grant contract: %v", err)
	}
	const rolePlaceholder = `:"runtime_role"`
	if strings.Count(string(grantTemplate), rolePlaceholder) != 1 {
		t.Fatalf("runtime grant role placeholder count = %d, want 1", strings.Count(string(grantTemplate), rolePlaceholder))
	}
	grantSQL := strings.ReplaceAll(string(grantTemplate), rolePlaceholder, roleIdentifier)
	if _, err := connection.Exec(ctx, grantSQL); err != nil {
		t.Fatalf("apply runtime grant contract: %v", err)
	}

	var tableUpdate, singletonUpdate, createdAtUpdate, tableDelete bool
	if err := connection.QueryRow(ctx, `SELECT
		pg_catalog.has_table_privilege($1, 'public.governance_state', 'UPDATE'),
		pg_catalog.has_column_privilege($1, 'public.governance_state', 'singleton', 'UPDATE'),
		pg_catalog.has_column_privilege($1, 'public.governance_state', 'created_at', 'UPDATE'),
		pg_catalog.has_table_privilege($1, 'public.governance_state', 'DELETE')`, runtimePrivilegeTestRole).Scan(
		&tableUpdate, &singletonUpdate, &createdAtUpdate, &tableDelete,
	); err != nil {
		t.Fatalf("inspect runtime governance privileges: %v", err)
	}
	if tableUpdate || !singletonUpdate || createdAtUpdate || tableDelete {
		t.Fatalf("runtime governance privileges = (table update %t, singleton update %t, created_at update %t, delete %t)", tableUpdate, singletonUpdate, createdAtUpdate, tableDelete)
	}

	if _, err := connection.Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("reassume restricted runtime role: %v", err)
	}
	claimed, claimErr := ClaimInitialAdministrator(
		ctx, connection, func() time.Time { return atTime }, userID, sessionID, issuer, subject,
		pgtype.UUID{Bytes: [16]byte{0x62}, Valid: true},
	)
	if _, err := connection.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("reset admitted runtime role: %v", err)
	}
	if claimErr != nil || claimed.UserID != userID || claimed.AuditID <= 0 || claimed.RevokedSessionID != sessionID {
		t.Fatalf("post-grant claim = (%+v, %v)", claimed, claimErr)
	}
	assertAdministratorClaimState(t, ctx, connection, userID, sessionID, "administrator", true, 1)
}

func assertAdministratorClaimState(t *testing.T, ctx context.Context, connection *pgx.Conn, userID, sessionID int64, wantRole string, wantRevoked bool, wantAudits int) {
	t.Helper()
	var role string
	var revokedAt *time.Time
	var audits int
	if err := connection.QueryRow(ctx, `SELECT u.role, s.revoked_at,
		(SELECT count(*) FROM public.moderation_actions WHERE action_type = 'bootstrap_administrator')
		FROM public.users AS u JOIN public.sessions AS s ON s.user_id = u.id
		WHERE u.id = $1 AND s.id = $2`, userID, sessionID).Scan(&role, &revokedAt, &audits); err != nil {
		t.Fatalf("load administrator claim state: %v", err)
	}
	if role != wantRole || (revokedAt != nil) != wantRevoked || audits != wantAudits {
		t.Fatalf("administrator claim state = (role %q, revoked %t, audits %d), want (%q, %t, %d)", role, revokedAt != nil, audits, wantRole, wantRevoked, wantAudits)
	}
}
