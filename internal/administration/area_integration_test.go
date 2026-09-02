//go:build integration

package administration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const areaAdministrationTestDatabase = "gotth_bb_alpha1_area_administration_test"
const areaAdministrationTestRole = "gotth_bb_area_runtime_test"

func TestAreaAdministrationOnRestrictedPostgreSQL17Role(t *testing.T) {
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
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+areaAdministrationTestDatabase+" WITH (FORCE)")
	_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+areaAdministrationTestRole)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+areaAdministrationTestDatabase); err != nil {
		t.Fatalf("create area administration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+areaAdministrationTestDatabase+" WITH (FORCE)")
		_, _ = admin.Exec(cleanupContext, "DROP ROLE IF EXISTS "+areaAdministrationTestRole)
	})
	ownerConfig := adminConfig.Copy()
	ownerConfig.Database = areaAdministrationTestDatabase
	if err := migration.Apply(ctx, ownerConfig, migrations.Files()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	owner, err := pgx.ConnectConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatalf("connect area administration owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	const rolePassword = "area-runtime-test-password"
	if _, err := admin.Exec(ctx, "CREATE ROLE "+areaAdministrationTestRole+" LOGIN PASSWORD '"+rolePassword+"'"); err != nil {
		t.Fatalf("create restricted runtime role: %v", err)
	}
	if _, err := owner.Exec(ctx, `
GRANT USAGE ON SCHEMA public TO `+areaAdministrationTestRole+`;
GRANT SELECT, INSERT, UPDATE ON public.areas TO `+areaAdministrationTestRole+`;
GRANT USAGE, SELECT ON SEQUENCE public.areas_id_seq TO `+areaAdministrationTestRole+`;
GRANT SELECT ON public.forum_groups TO `+areaAdministrationTestRole+`;
GRANT SELECT, INSERT, DELETE ON public.area_groups TO `+areaAdministrationTestRole+`;
GRANT SELECT, INSERT ON public.moderation_actions TO `+areaAdministrationTestRole+`;
GRANT USAGE, SELECT ON SEQUENCE public.moderation_actions_id_seq TO `+areaAdministrationTestRole+`;`); err != nil {
		t.Fatalf("grant restricted runtime privileges: %v", err)
	}
	var administratorID, groupID int64
	if err := owner.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Administrator', 'administrator') RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Members', $1) RETURNING id`, administratorID).Scan(&groupID); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	runtimeConfig := ownerConfig.Copy()
	runtimeConfig.User = areaAdministrationTestRole
	runtimeConfig.Password = rolePassword
	runtime, err := pgx.ConnectConfig(ctx, runtimeConfig)
	if err != nil {
		t.Fatalf("connect restricted runtime role: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	actor := policy.AccessContext{Authenticated: true, UserID: administratorID, Role: policy.RoleAdministrator}
	createdAt := time.Date(2026, 9, 2, 17, 0, 0, 123456000, time.UTC)
	created, err := CreateArea(ctx, runtime, func() time.Time { return createdAt }, actor, AreaInput{
		Slug: "general", Name: "General", Description: "General discussion", Visibility: policy.VisibilityPublic,
		PostingMode: policy.PostingNormal, Reason: "Create the first discussion area",
	}, pgtype.UUID{Bytes: [16]byte{0x61}, Valid: true})
	if err != nil || created.AreaID <= 0 || created.AuditID <= 0 || created.Slug != "general" {
		t.Fatalf("CreateArea() = (%+v, %v)", created, err)
	}
	groupArea, err := CreateArea(ctx, runtime, func() time.Time { return createdAt.Add(time.Minute) }, actor, AreaInput{
		Slug: "members", Name: "Members", DisplayOrder: 2, Visibility: policy.VisibilityGroups,
		PostingMode: policy.PostingReadOnly, GroupIDs: []int64{groupID}, Reason: "Create a restricted member area",
	}, pgtype.UUID{Bytes: [16]byte{0x62}, Valid: true})
	if err != nil {
		t.Fatalf("CreateArea(group restricted) returned error: %v", err)
	}
	page, err := LoadAreaManagement(ctx, runtimeAreaQuerier{runtime}, actor)
	if err != nil || len(page.Areas) != 2 || len(page.Groups) != 1 || len(page.Areas[1].GroupIDs) != 1 {
		t.Fatalf("LoadAreaManagement() = (%+v, %v)", page, err)
	}
	revision := page.Areas[1].UpdatedAt
	updatedAt := createdAt.Add(2 * time.Minute)
	updated, err := UpdateArea(ctx, runtime, func() time.Time { return updatedAt }, actor, groupArea.AreaID, AreaInput{
		Slug: "members", Name: "Member archive", Description: "Visible to signed-in members", DisplayOrder: 3,
		Visibility: policy.VisibilityAuthenticated, PostingMode: policy.PostingArchived,
		Reason: "Open the archive to all signed-in members", Revision: revision,
	}, pgtype.UUID{Bytes: [16]byte{0x63}, Valid: true})
	if err != nil || updated.AreaID != groupArea.AreaID || updated.AuditID <= groupArea.AuditID {
		t.Fatalf("UpdateArea() = (%+v, %v)", updated, err)
	}
	var mappingCount, auditCount int64
	var action, previousVisibility, resultingVisibility string
	if err := owner.QueryRow(ctx, `
SELECT (SELECT count(*) FROM public.area_groups WHERE area_id = $1),
       (SELECT count(*) FROM public.moderation_actions WHERE target_area_id = $1),
       action_type, previous_state->>'visibility', resulting_state->>'visibility'
FROM public.moderation_actions WHERE id = $2`, groupArea.AreaID, updated.AuditID).Scan(&mappingCount, &auditCount, &action, &previousVisibility, &resultingVisibility); err != nil || mappingCount != 0 || auditCount != 2 || action != "update_area" || previousVisibility != "groups" || resultingVisibility != "authenticated" {
		t.Fatalf("persisted update = (mappings %d, audits %d, %q, %q -> %q, %v)", mappingCount, auditCount, action, previousVisibility, resultingVisibility, err)
	}
	if stale, staleErr := UpdateArea(ctx, runtime, time.Now, actor, groupArea.AreaID, AreaInput{Slug: "members", Name: "Stale", Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, Reason: "Stale overwrite", Revision: revision}, pgtype.UUID{Bytes: [16]byte{0x64}, Valid: true}); stale != (AreaMutationResult{}) || !errors.Is(staleErr, ErrAreaAdministrationConflict) {
		t.Fatalf("stale UpdateArea() = (%+v, %v)", stale, staleErr)
	}
	refreshed, err := LoadAreaManagement(ctx, runtimeAreaQuerier{runtime}, actor)
	if err != nil {
		t.Fatalf("reload area administration: %v", err)
	}
	current := refreshed.Areas[1]
	if unchanged, unchangedErr := UpdateArea(ctx, runtime, time.Now, actor, current.ID, AreaInput{Slug: current.Slug, Name: current.Name, Description: current.Description, DisplayOrder: current.DisplayOrder, Visibility: current.Visibility, PostingMode: current.PostingMode, GroupIDs: current.GroupIDs, Reason: "No actual change", Revision: current.UpdatedAt}, pgtype.UUID{Bytes: [16]byte{0x66}, Valid: true}); unchanged != (AreaMutationResult{}) || !errors.Is(unchangedErr, ErrAreaAdministrationConflict) {
		t.Fatalf("unchanged UpdateArea() = (%+v, %v)", unchanged, unchangedErr)
	}
	if wrongSlug, wrongSlugErr := UpdateArea(ctx, runtime, time.Now, actor, current.ID, AreaInput{Slug: "different", Name: current.Name, Description: current.Description, DisplayOrder: current.DisplayOrder, Visibility: current.Visibility, PostingMode: current.PostingMode, Reason: "Attempt slug rewrite", Revision: current.UpdatedAt}, pgtype.UUID{Bytes: [16]byte{0x67}, Valid: true}); wrongSlug != (AreaMutationResult{}) || !errors.Is(wrongSlugErr, ErrAreaAdministrationInput) {
		t.Fatalf("slug-changing UpdateArea() = (%+v, %v)", wrongSlug, wrongSlugErr)
	}
	if missing, missingErr := UpdateArea(ctx, runtime, time.Now, actor, 9999, AreaInput{Slug: "missing", Name: "Missing", Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, Reason: "Update missing area", Revision: current.UpdatedAt}, pgtype.UUID{Bytes: [16]byte{0x68}, Valid: true}); missing != (AreaMutationResult{}) || !errors.Is(missingErr, pgx.ErrNoRows) {
		t.Fatalf("missing UpdateArea() = (%+v, %v)", missing, missingErr)
	}
	if unknownGroup, unknownGroupErr := CreateArea(ctx, runtime, time.Now, actor, AreaInput{Slug: "unknown-group", Name: "Unknown group", Visibility: policy.VisibilityGroups, PostingMode: policy.PostingNormal, GroupIDs: []int64{9999}, Reason: "Reject missing group"}, pgtype.UUID{Bytes: [16]byte{0x69}, Valid: true}); unknownGroup != (AreaMutationResult{}) || !errors.Is(unknownGroupErr, ErrAreaAdministrationInput) {
		t.Fatalf("unknown-group CreateArea() = (%+v, %v)", unknownGroup, unknownGroupErr)
	}
	if duplicate, duplicateErr := CreateArea(ctx, runtime, time.Now, actor, AreaInput{Slug: "general", Name: "Duplicate", Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, Reason: "Duplicate slug"}, pgtype.UUID{Bytes: [16]byte{0x65}, Valid: true}); duplicate != (AreaMutationResult{}) || !errors.Is(duplicateErr, ErrAreaAdministrationConflict) {
		t.Fatalf("duplicate CreateArea() = (%+v, %v)", duplicate, duplicateErr)
	}
	if _, err := owner.Exec(ctx, `
CREATE FUNCTION public.reject_area_administration_audit()
RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reject area audit'; END; $$;
CREATE TRIGGER reject_area_administration_audit
BEFORE INSERT ON public.moderation_actions
FOR EACH ROW EXECUTE FUNCTION public.reject_area_administration_audit();`); err != nil {
		t.Fatalf("create rejecting area audit trigger: %v", err)
	}
	if rejected, rejectedErr := CreateArea(ctx, runtime, time.Now, actor, AreaInput{Slug: "rolled-back", Name: "Rolled back", Visibility: policy.VisibilityPublic, PostingMode: policy.PostingNormal, Reason: "Exercise audit rollback"}, pgtype.UUID{Bytes: [16]byte{0x70}, Valid: true}); rejected != (AreaMutationResult{}) || rejectedErr == nil {
		t.Fatalf("audit-rejected CreateArea() = (%+v, %v)", rejected, rejectedErr)
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER reject_area_administration_audit ON public.moderation_actions; DROP FUNCTION public.reject_area_administration_audit()`); err != nil {
		t.Fatalf("drop rejecting area audit trigger: %v", err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM public.areas`).Scan(&mappingCount); err != nil || mappingCount != 2 {
		t.Fatalf("area count after rejected changes = (%d, %v)", mappingCount, err)
	}
}

type runtimeAreaQuerier struct{ connection *pgx.Conn }

func (querier runtimeAreaQuerier) ListAreasForAdministration(ctx context.Context) ([]db.ListAreasForAdministrationRow, error) {
	return db.New(querier.connection).ListAreasForAdministration(ctx)
}

func (querier runtimeAreaQuerier) ListForumGroupsForAreaAdministration(ctx context.Context) ([]db.ListForumGroupsForAreaAdministrationRow, error) {
	return db.New(querier.connection).ListForumGroupsForAreaAdministration(ctx)
}
