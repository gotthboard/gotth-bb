//go:build integration

package administration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const accountAdministrationTestDatabase = "gotth_bb_an04_account_administration_test"

func TestAccountAdministrationGovernanceOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	databaseIdentifier := pgx.Identifier{accountAdministrationTestDatabase}.Sanitize()
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+databaseIdentifier+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+databaseIdentifier); err != nil {
		t.Fatalf("create account administration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+databaseIdentifier+" WITH (FORCE)")
	})
	ownerConfig := adminConfig.Copy()
	ownerConfig.Database = accountAdministrationTestDatabase
	if err := migration.Apply(ctx, ownerConfig, migrations.Files()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	connections := make([]*pgx.Conn, 2)
	for index := range connections {
		connections[index], err = pgx.ConnectConfig(ctx, ownerConfig)
		if err != nil {
			t.Fatalf("connect account administration database: %v", err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}

	var actorID, secondAdministratorID, memberID int64
	if err := connections[0].QueryRow(ctx, `
INSERT INTO public.users (display_name, role) VALUES
    ('Governance Administrator', 'administrator'),
    ('Continuity Administrator', 'administrator'),
    ('Local Member', 'member')
RETURNING id`).Scan(&actorID); err != nil {
		t.Fatalf("insert first account: %v", err)
	}
	if err := connections[0].QueryRow(ctx, `SELECT id FROM public.users WHERE display_name = 'Continuity Administrator'`).Scan(&secondAdministratorID); err != nil {
		t.Fatalf("load second administrator: %v", err)
	}
	if err := connections[0].QueryRow(ctx, `SELECT id FROM public.users WHERE display_name = 'Local Member'`).Scan(&memberID); err != nil {
		t.Fatalf("load member: %v", err)
	}
	actor := policy.AccessContext{Authenticated: true, UserID: actorID, Role: policy.RoleAdministrator}
	observedAt := time.Date(2026, 9, 8, 15, 0, 0, 123456000, time.UTC)
	querier := accountRuntimeQuerier{connection: connections[0]}

	accounts, err := ListAccounts(ctx, querier, actor, observedAt, 0)
	if err != nil || len(accounts.Accounts) != 3 || accounts.Accounts[0].ID != actorID || accounts.NextAfter != 0 {
		t.Fatalf("ListAccounts() = (%+v, %v)", accounts, err)
	}
	detail, err := LoadAccount(ctx, querier, actor, observedAt, memberID)
	if err != nil || detail.ID != memberID || detail.Role != policy.RoleMember || detail.Revision != 1 {
		t.Fatalf("LoadAccount() = (%+v, %v)", detail, err)
	}

	created, err := CreateGroup(ctx, connections[0], func() time.Time { return observedAt }, actor, "Members", "Create the member access group", testAdministrationRequestID(1))
	if err != nil || created.GroupID <= 0 || created.Revision != 1 || created.AuditID <= 0 {
		t.Fatalf("CreateGroup() = (%+v, %v)", created, err)
	}
	if _, duplicateErr := CreateGroup(ctx, connections[0], func() time.Time { return observedAt }, actor, "members", "Reject a duplicate group", testAdministrationRequestID(2)); !errors.Is(duplicateErr, ErrAccountAdministrationConflict) {
		t.Fatalf("duplicate CreateGroup() error = %v", duplicateErr)
	}
	renamed, err := RenameGroup(ctx, connections[0], func() time.Time { return observedAt.Add(time.Second) }, actor, created.GroupID, "Registered Members", "Clarify the group name", created.Revision, testAdministrationRequestID(3))
	if err != nil || renamed.Revision != 2 || renamed.AuditID <= created.AuditID {
		t.Fatalf("RenameGroup() = (%+v, %v)", renamed, err)
	}
	if _, staleErr := RenameGroup(ctx, connections[0], time.Now, actor, created.GroupID, "Stale", "Reject a stale rename", created.Revision, testAdministrationRequestID(4)); !errors.Is(staleErr, ErrAccountAdministrationConflict) {
		t.Fatalf("stale RenameGroup() error = %v", staleErr)
	}

	granted, err := ChangeGroupMembership(ctx, connections[0], func() time.Time { return observedAt.Add(2 * time.Second) }, actor, memberID, created.GroupID, true, "Grant member access", detail.Revision, testAdministrationRequestID(5))
	if err != nil || granted.UserID != memberID || granted.Revision != 2 || granted.AuditID <= 0 {
		t.Fatalf("grant ChangeGroupMembership() = (%+v, %v)", granted, err)
	}
	if _, noopErr := ChangeGroupMembership(ctx, connections[0], time.Now, actor, memberID, created.GroupID, true, "Reject duplicate grant", granted.Revision, testAdministrationRequestID(6)); !errors.Is(noopErr, ErrAccountAdministrationConflict) {
		t.Fatalf("duplicate grant error = %v", noopErr)
	}
	memberships, err := ListAccountGroups(ctx, querier, actor, observedAt.Add(3*time.Second), memberID, 0)
	if err != nil || len(memberships.Groups) != 1 || !memberships.Groups[0].Member {
		t.Fatalf("ListAccountGroups(granted) = (%+v, %v)", memberships, err)
	}
	revoked, err := ChangeGroupMembership(ctx, connections[0], func() time.Time { return observedAt.Add(3 * time.Second) }, actor, memberID, created.GroupID, false, "Revoke member access", granted.Revision, testAdministrationRequestID(7))
	if err != nil || revoked.Revision != 3 {
		t.Fatalf("revoke ChangeGroupMembership() = (%+v, %v)", revoked, err)
	}

	if _, err := connections[0].Exec(ctx, `INSERT INTO public.sessions (session_id_hash, user_id, issued_at, expires_at, idle_expires_at, oidc_validated_at) VALUES (decode(repeat('11', 32), 'hex'), $1, $2, $3, $3, $2)`, memberID, observedAt, observedAt.Add(time.Hour)); err != nil {
		t.Fatalf("insert member session: %v", err)
	}
	changedRole, err := ChangeAccountRole(ctx, connections[0], func() time.Time { return observedAt.Add(4 * time.Second) }, actor, memberID, policy.RoleModerator, policy.RoleMember, "Promote the local member", revoked.Revision, testAdministrationRequestID(8))
	if err != nil || changedRole.Role != policy.RoleModerator || changedRole.Revision != 4 || changedRole.RevokedSessions != 1 {
		t.Fatalf("ChangeAccountRole() = (%+v, %v)", changedRole, err)
	}
	var role string
	var activeSessions, auditCount int64
	if err := connections[0].QueryRow(ctx, `SELECT role, (SELECT count(*) FROM public.sessions WHERE user_id = $1 AND revoked_at IS NULL), (SELECT count(*) FROM public.moderation_actions WHERE target_user_id = $1) FROM public.users WHERE id = $1`, memberID).Scan(&role, &activeSessions, &auditCount); err != nil || role != "moderator" || activeSessions != 0 || auditCount != 3 {
		t.Fatalf("persisted account governance = (%q, sessions %d, audits %d, %v)", role, activeSessions, auditCount, err)
	}

	secondActor := policy.AccessContext{Authenticated: true, UserID: secondAdministratorID, Role: policy.RoleAdministrator}
	if _, err := ChangeAccountRole(ctx, connections[0], func() time.Time { return observedAt.Add(5 * time.Second) }, actor, secondAdministratorID, policy.RoleMember, policy.RoleAdministrator, "Exercise administrator continuity", 1, testAdministrationRequestID(9)); err != nil {
		t.Fatalf("demote second administrator: %v", err)
	}
	if _, err := ChangeAccountRole(ctx, connections[1], func() time.Time { return observedAt.Add(6 * time.Second) }, secondActor, actorID, policy.RoleMember, policy.RoleAdministrator, "Reject final administrator removal", 1, testAdministrationRequestID(10)); !errors.Is(err, ErrAccountAdministrationDenied) {
		t.Fatalf("stale demoted actor role change error = %v", err)
	}
	if _, err := ChangeAccountRole(ctx, connections[0], func() time.Time { return observedAt.Add(6 * time.Second) }, actor, actorID, policy.RoleMember, policy.RoleAdministrator, "Reject self role change", 1, testAdministrationRequestID(11)); !errors.Is(err, ErrAccountAdministrationInput) {
		t.Fatalf("self role change error = %v", err)
	}
}

func testAdministrationRequestID(first byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{first}, Valid: true}
}

type accountRuntimeQuerier struct{ connection *pgx.Conn }

func (querier accountRuntimeQuerier) ListAccountsForAdministration(ctx context.Context, parameters db.ListAccountsForAdministrationParams) ([]db.ListAccountsForAdministrationRow, error) {
	return db.New(querier.connection).ListAccountsForAdministration(ctx, parameters)
}

func (querier accountRuntimeQuerier) LoadAccountForAdministration(ctx context.Context, parameters db.LoadAccountForAdministrationParams) (db.LoadAccountForAdministrationRow, error) {
	return db.New(querier.connection).LoadAccountForAdministration(ctx, parameters)
}

func (querier accountRuntimeQuerier) ListAccountGroupsForAdministration(ctx context.Context, parameters db.ListAccountGroupsForAdministrationParams) ([]db.ListAccountGroupsForAdministrationRow, error) {
	return db.New(querier.connection).ListAccountGroupsForAdministration(ctx, parameters)
}

func (querier accountRuntimeQuerier) ListGroupsForAdministration(ctx context.Context, parameters db.ListGroupsForAdministrationParams) ([]db.ListGroupsForAdministrationRow, error) {
	return db.New(querier.connection).ListGroupsForAdministration(ctx, parameters)
}
