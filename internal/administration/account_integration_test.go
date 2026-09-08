//go:build integration

package administration

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	accountAdministrationTestDatabase = "gotth_bb_an04_account_administration_test"
	accountAdministrationTestRole     = "gotth_bb_an04_account_administration_runtime"
)

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

	observedAt := time.Date(2026, 9, 8, 15, 0, 0, 123456000, time.UTC)
	createdAt := observedAt.Add(-time.Hour)
	var actorID, secondAdministratorID, memberID int64
	if err := connections[0].QueryRow(ctx, `
INSERT INTO public.users (display_name, role, created_at) VALUES
    ('Governance Administrator', 'administrator', $1),
    ('Continuity Administrator', 'administrator', $1),
    ('Local Member', 'member', $1)
RETURNING id`, createdAt).Scan(&actorID); err != nil {
		t.Fatalf("insert first account: %v", err)
	}
	if err := connections[0].QueryRow(ctx, `SELECT id FROM public.users WHERE display_name = 'Continuity Administrator'`).Scan(&secondAdministratorID); err != nil {
		t.Fatalf("load second administrator: %v", err)
	}
	if err := connections[0].QueryRow(ctx, `SELECT id FROM public.users WHERE display_name = 'Local Member'`).Scan(&memberID); err != nil {
		t.Fatalf("load member: %v", err)
	}
	actor := policy.AccessContext{Authenticated: true, UserID: actorID, Role: policy.RoleAdministrator}
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
	if _, err := connections[0].Exec(ctx, `
CREATE FUNCTION public.reject_account_administration_audit()
RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reject account administration audit'; END; $$;
CREATE TRIGGER reject_account_administration_audit
BEFORE INSERT ON public.moderation_actions
FOR EACH ROW EXECUTE FUNCTION public.reject_account_administration_audit()`); err != nil {
		t.Fatalf("create rejecting account audit trigger: %v", err)
	}
	if _, err := ChangeGroupMembership(ctx, connections[0], func() time.Time { return observedAt.Add(3 * time.Second) }, actor, memberID, created.GroupID, true, "Exercise atomic audit rollback", revoked.Revision, testAdministrationRequestID(15)); err == nil {
		t.Fatal("audit-rejected membership change returned no error")
	}
	if _, err := connections[0].Exec(ctx, `DROP TRIGGER reject_account_administration_audit ON public.moderation_actions; DROP FUNCTION public.reject_account_administration_audit()`); err != nil {
		t.Fatalf("drop rejecting account audit trigger: %v", err)
	}
	var rolledBackMembership bool
	var rolledBackRevision int64
	if err := connections[0].QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.forum_group_members WHERE user_id = $1 AND group_id = $2), administration_revision FROM public.users WHERE id = $1`, memberID, created.GroupID).Scan(&rolledBackMembership, &rolledBackRevision); err != nil || rolledBackMembership || rolledBackRevision != revoked.Revision {
		t.Fatalf("audit rollback state = (member %t, revision %d, %v)", rolledBackMembership, rolledBackRevision, err)
	}
	selfGranted, err := ChangeGroupMembership(ctx, connections[0], func() time.Time { return observedAt.Add(3 * time.Second) }, actor, actorID, created.GroupID, true, "Grant administrator membership", 1, testAdministrationRequestID(12))
	if err != nil || selfGranted.UserID != actorID || selfGranted.Revision != 2 {
		t.Fatalf("self grant ChangeGroupMembership() = (%+v, %v)", selfGranted, err)
	}
	if _, err := ChangeGroupMembership(ctx, connections[0], func() time.Time { return observedAt.Add(3 * time.Second) }, actor, actorID, created.GroupID, false, "Revoke administrator membership", selfGranted.Revision, testAdministrationRequestID(13)); err != nil {
		t.Fatalf("self revoke ChangeGroupMembership() error = %v", err)
	}

	if _, err := connections[0].Exec(ctx, `INSERT INTO public.sessions (token_hash, user_id, issued_at, last_seen_at, validated_at, expires_at) VALUES (decode(repeat('11', 32), 'hex'), $1, $2, $2, $2, $3)`, memberID, observedAt, observedAt.Add(time.Hour)); err != nil {
		t.Fatalf("insert member session: %v", err)
	}
	changedRole, err := ChangeAccountRole(ctx, connections[0], func() time.Time { return observedAt.Add(4 * time.Second) }, actor, memberID, policy.RoleModerator, policy.RoleMember, "Promote the local member", revoked.Revision, testAdministrationRequestID(8))
	if err != nil || changedRole.Role != policy.RoleModerator || changedRole.Revision != 4 || changedRole.RevokedSessions != 1 {
		t.Fatalf("ChangeAccountRole() = (%+v, %v)", changedRole, err)
	}
	if _, err := ChangeAccountRole(ctx, connections[0], time.Now, actor, memberID, policy.RoleAdministrator, policy.RoleMember, "Reject stale role form", revoked.Revision, testAdministrationRequestID(16)); !errors.Is(err, ErrAccountAdministrationConflict) {
		t.Fatalf("stale ChangeAccountRole() error = %v", err)
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
	if _, err := ChangeAccountRole(ctx, connections[0], func() time.Time { return observedAt.Add(6 * time.Second) }, actor, actorID, policy.RoleMember, policy.RoleAdministrator, "Reject self role change", 1, testAdministrationRequestID(11)); !errors.Is(err, ErrAccountAdministrationDenied) {
		t.Fatalf("self role change error = %v", err)
	}
	if _, err := connections[0].Exec(ctx, `INSERT INTO public.forum_groups (name, created_by) SELECT 'Paged Group ' || value, $1 FROM generate_series(1, 50) AS value`, actorID); err != nil {
		t.Fatalf("insert paged groups: %v", err)
	}
	groupsPage, err := ListGroups(ctx, querier, actor, observedAt.Add(6*time.Second), 0)
	if err != nil || len(groupsPage.Groups) != 50 || groupsPage.NextAfter != groupsPage.Groups[49].ID {
		t.Fatalf("ListGroups(51 boundary) = (%+v, %v)", groupsPage, err)
	}
	membershipPage, err := ListAccountGroups(ctx, querier, actor, observedAt.Add(6*time.Second), memberID, 0)
	if err != nil || len(membershipPage.Groups) != 50 || membershipPage.NextAfter != membershipPage.Groups[49].ID {
		t.Fatalf("ListAccountGroups(51 boundary) = (%+v, %v)", membershipPage, err)
	}
	if _, err := connections[0].Exec(ctx, `INSERT INTO public.users (display_name) SELECT 'Paged Account ' || value FROM generate_series(1, 49) AS value`); err != nil {
		t.Fatalf("insert paged accounts: %v", err)
	}
	accountsPage, err := ListAccounts(ctx, querier, actor, observedAt.Add(6*time.Second), 0)
	if err != nil || len(accountsPage.Accounts) != 50 || accountsPage.NextAfter != accountsPage.Accounts[49].ID {
		t.Fatalf("ListAccounts(51 boundary) = (%+v, %v)", accountsPage, err)
	}
	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET muted_until = $2 WHERE id = $1`, actorID, observedAt.Add(time.Hour)); err != nil {
		t.Fatalf("mute administrator fixture: %v", err)
	}
	if _, err := CreateGroup(ctx, connections[0], func() time.Time { return observedAt.Add(7 * time.Second) }, actor, "Denied Group", "Reject muted administrator", testAdministrationRequestID(14)); !errors.Is(err, ErrAccountAdministrationDenied) {
		t.Fatalf("muted administrator CreateGroup() error = %v", err)
	}
	if _, err := ListAccounts(ctx, querier, actor, observedAt.Add(7*time.Second), 0); !errors.Is(err, ErrAccountAdministrationDenied) {
		t.Fatalf("muted administrator ListAccounts() error = %v", err)
	}
	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET muted_until = NULL WHERE id = $1`, actorID); err != nil {
		t.Fatalf("unmute restricted-runtime actor: %v", err)
	}

	roleIdentifier := pgx.Identifier{accountAdministrationTestRole}.Sanitize()
	_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+roleIdentifier)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+roleIdentifier+" NOLOGIN"); err != nil {
		t.Fatalf("create restricted runtime role: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = connections[0].Exec(cleanupContext, "DROP OWNED BY "+roleIdentifier)
		_, _ = admin.Exec(cleanupContext, "DROP ROLE IF EXISTS "+roleIdentifier)
	})
	baselineGrants := `
GRANT USAGE ON SCHEMA public TO ` + roleIdentifier + `;
GRANT SELECT ON public.governance_state TO ` + roleIdentifier + `;
GRANT SELECT, UPDATE ON public.users TO ` + roleIdentifier + `;
GRANT SELECT, UPDATE ON public.sessions TO ` + roleIdentifier + `;
GRANT SELECT, INSERT ON public.moderation_actions TO ` + roleIdentifier + `;
GRANT USAGE, SELECT ON SEQUENCE public.moderation_actions_id_seq TO ` + roleIdentifier + `;`
	if _, err := connections[0].Exec(ctx, baselineGrants); err != nil {
		t.Fatalf("grant baseline runtime privileges: %v", err)
	}
	grantTemplate, err := os.ReadFile("../../deploy/postgresql/runtime-grants.sql")
	if err != nil {
		t.Fatalf("read runtime grant contract: %v", err)
	}
	const rolePlaceholder = `:"runtime_role"`
	if strings.Count(string(grantTemplate), rolePlaceholder) != 14 {
		t.Fatalf("runtime grant role placeholder count = %d, want 14", strings.Count(string(grantTemplate), rolePlaceholder))
	}
	if _, err := connections[0].Exec(ctx, strings.ReplaceAll(string(grantTemplate), rolePlaceholder, roleIdentifier)); err != nil {
		t.Fatalf("apply runtime grant contract: %v", err)
	}
	var runtimeTargetID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Restricted Runtime Member') RETURNING id`).Scan(&runtimeTargetID); err != nil {
		t.Fatalf("insert restricted runtime target: %v", err)
	}
	if _, err := connections[0].Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("assume restricted runtime role: %v", err)
	}
	runtimeAt := observedAt.Add(8 * time.Second)
	runtimeGroup, runtimeErr := CreateGroup(ctx, connections[0], func() time.Time { return runtimeAt }, actor, "Runtime Members", "Create through the packaged runtime grant", testAdministrationRequestID(20))
	if runtimeErr != nil || runtimeGroup.GroupID <= 0 {
		t.Fatalf("restricted runtime CreateGroup() = (%+v, %v)", runtimeGroup, runtimeErr)
	}
	runtimeRename, runtimeErr := RenameGroup(ctx, connections[0], func() time.Time { return runtimeAt.Add(time.Second) }, actor, runtimeGroup.GroupID, "Runtime Accounts", "Rename through the packaged runtime grant", runtimeGroup.Revision, testAdministrationRequestID(21))
	if runtimeErr != nil || runtimeRename.Revision != 2 {
		t.Fatalf("restricted runtime RenameGroup() = (%+v, %v)", runtimeRename, runtimeErr)
	}
	runtimeMembership, runtimeErr := ChangeGroupMembership(ctx, connections[0], func() time.Time { return runtimeAt.Add(2 * time.Second) }, actor, runtimeTargetID, runtimeGroup.GroupID, true, "Grant through the packaged runtime grant", 1, testAdministrationRequestID(22))
	if runtimeErr != nil || runtimeMembership.Revision != 2 {
		t.Fatalf("restricted runtime ChangeGroupMembership() = (%+v, %v)", runtimeMembership, runtimeErr)
	}
	runtimeRole, runtimeErr := ChangeAccountRole(ctx, connections[0], func() time.Time { return runtimeAt.Add(3 * time.Second) }, actor, runtimeTargetID, policy.RoleModerator, policy.RoleMember, "Change role through the packaged runtime grant", runtimeMembership.Revision, testAdministrationRequestID(23))
	if runtimeErr != nil || runtimeRole.Role != policy.RoleModerator || runtimeRole.Revision != 3 {
		t.Fatalf("restricted runtime ChangeAccountRole() = (%+v, %v)", runtimeRole, runtimeErr)
	}
	if _, err := connections[0].Exec(ctx, `DELETE FROM public.forum_groups WHERE id = $1`, runtimeGroup.GroupID); err == nil {
		t.Fatal("restricted runtime role deleted a forum group")
	}
	if _, err := connections[0].Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("reset restricted runtime role: %v", err)
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
