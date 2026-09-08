//go:build integration

package readiness

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/rerender"
	"github.com/gotthboard/gotth-bb/internal/searchprojection"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	readinessTestDatabase       = "gotth_bb_alpha1_readiness_test"
	readinessRestrictedRole     = "gotth_bb_alpha3_readiness_runtime"
	readinessRestrictedPassword = "alpha3-readiness-test-only"
)

func TestCheckerTracksReleaseAndAdministratorInvariantsOnPostgreSQL17(t *testing.T) {
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+readinessTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale readiness database: %v", err)
	}
	if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+readinessRestrictedRole); err != nil {
		t.Fatalf("drop stale readiness role: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE ROLE "+readinessRestrictedRole+" LOGIN PASSWORD '"+readinessRestrictedPassword+"'"); err != nil {
		t.Fatalf("create restricted readiness role: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, "DROP ROLE IF EXISTS "+readinessRestrictedRole); err != nil {
			t.Errorf("drop readiness role: %v", err)
		}
	})
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+readinessTestDatabase); err != nil {
		t.Fatalf("create readiness database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+readinessTestDatabase+" WITH (FORCE)"); err != nil {
			t.Errorf("drop readiness database: %v", err)
		}
	})

	testConfig := adminConfig.Copy()
	testConfig.Database = readinessTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect migrated database: %v", err)
	}
	t.Cleanup(func() {
		if err := connection.Close(context.Background()); err != nil {
			t.Errorf("close readiness connection: %v", err)
		}
	})
	// The release-owned migrate command always runs this completion phase after
	// applying schema migrations, including on an empty fresh database. That
	// validates the NOT VALID renderer constraint before readiness can pass.
	if err := rerender.Run(ctx, connection, rerender.MaximumBatchSize); err != nil {
		t.Fatalf("rerender.Run() returned error: %v", err)
	}
	if err := searchprojection.Run(ctx, connection, searchprojection.MaximumBatchSize); err != nil {
		t.Fatalf("searchprojection.Run() returned error: %v", err)
	}
	release, err := migration.NewReleaseVerifier(migrations.Files())
	if err != nil {
		t.Fatalf("migration.NewReleaseVerifier() returned error: %v", err)
	}
	checker, err := New(connection, func(checkContext context.Context) error {
		return release.Verify(checkContext, connection)
	}, time.Now)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if err := checker.Check(ctx); err == nil {
		t.Fatal("Check() accepted a database without an administrator")
	}
	if _, err := connection.Exec(ctx, "INSERT INTO public.users (display_name, role) VALUES ('Readiness Administrator', 'administrator')"); err != nil {
		t.Fatalf("insert readiness administrator: %v", err)
	}
	var liveConstraintDefinition, liveSizeDefinition, liveCursorConstraintDefinition string
	if err := connection.QueryRow(ctx, `SELECT
(SELECT pg_catalog.pg_get_constraintdef(oid, false) FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current'),
(SELECT pg_catalog.pg_get_constraintdef(oid, false) FROM pg_catalog.pg_constraint WHERE conname = 'posts_rendered_size'),
(SELECT pg_catalog.pg_get_constraintdef(oid, false) FROM pg_catalog.pg_constraint WHERE conname = 'content_renderer_state_cursor_progress')`).Scan(&liveConstraintDefinition, &liveSizeDefinition, &liveCursorConstraintDefinition); err != nil {
		t.Fatalf("read renderer constraint definitions: %v", err)
	}
	if liveConstraintDefinition != rendererConstraintDefinition {
		t.Fatalf("renderer constraint definition = %q, want %q", liveConstraintDefinition, rendererConstraintDefinition)
	}
	if liveSizeDefinition != renderedSizeConstraintDefinition {
		t.Fatalf("rendered-size constraint definition = %q, want %q", liveSizeDefinition, renderedSizeConstraintDefinition)
	}
	if liveCursorConstraintDefinition != rendererCursorConstraintDefinition {
		t.Fatalf("renderer cursor constraint definition = %q, want %q", liveCursorConstraintDefinition, rendererCursorConstraintDefinition)
	}
	if err := checker.Check(ctx); err == nil {
		t.Fatal("Check() accepted the migration-owner role as a runtime role")
	}

	roleIdentifier := pgx.Identifier{readinessRestrictedRole}.Sanitize()
	if _, err := connection.Exec(ctx, `GRANT CONNECT ON DATABASE `+pgx.Identifier{readinessTestDatabase}.Sanitize()+` TO `+roleIdentifier+`;
GRANT USAGE ON SCHEMA public TO `+roleIdentifier+`;
GRANT SELECT ON TABLE public.gotth_schema_migrations, public.governance_state, public.users TO `+roleIdentifier+`;`); err != nil {
		t.Fatalf("grant baseline readiness privileges: %v", err)
	}
	restrictedConfig := testConfig.Copy()
	restrictedConfig.User = readinessRestrictedRole
	restrictedConfig.Password = readinessRestrictedPassword
	restricted, err := pgx.ConnectConfig(ctx, restrictedConfig)
	if err != nil {
		t.Fatalf("connect as restricted runtime role: %v", err)
	}
	t.Cleanup(func() { _ = restricted.Close(context.Background()) })
	restrictedChecker, err := New(restricted, func(checkContext context.Context) error {
		return release.Verify(checkContext, restricted)
	}, time.Now)
	if err != nil {
		t.Fatalf("New(restricted) returned error: %v", err)
	}
	var postgresError *pgconn.PgError
	if err := restrictedChecker.Check(ctx); !errors.As(err, &postgresError) || postgresError.Code != "42501" {
		t.Fatalf("restricted Check() before packaged grant = %v, want SQLSTATE 42501", err)
	}

	grantTemplate, err := os.ReadFile("../../deploy/postgresql/runtime-grants.sql")
	if err != nil {
		t.Fatalf("read packaged runtime grants: %v", err)
	}
	const rolePlaceholder = `:"runtime_role"`
	if count := strings.Count(string(grantTemplate), rolePlaceholder); count != 8 {
		t.Fatalf("runtime grant role placeholder count = %d, want 8", count)
	}
	grantSQL := strings.ReplaceAll(string(grantTemplate), rolePlaceholder, roleIdentifier)
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := connection.Exec(ctx, grantSQL); err != nil {
			t.Fatalf("apply packaged runtime grants attempt %d: %v", attempt, err)
		}
	}
	var rendererOwner string
	var rendererSelect, rendererInsert, rendererUpdate, rendererDelete bool
	if err := connection.QueryRow(ctx, `SELECT
owner.rolname,
pg_catalog.has_table_privilege($1, 'public.content_renderer_state', 'SELECT'),
pg_catalog.has_table_privilege($1, 'public.content_renderer_state', 'INSERT'),
pg_catalog.has_table_privilege($1, 'public.content_renderer_state', 'UPDATE'),
pg_catalog.has_table_privilege($1, 'public.content_renderer_state', 'DELETE')
FROM pg_catalog.pg_class AS renderer_state
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = renderer_state.relnamespace
JOIN pg_catalog.pg_roles AS owner ON owner.oid = renderer_state.relowner
WHERE namespace.nspname = 'public' AND renderer_state.relname = 'content_renderer_state'`, readinessRestrictedRole).Scan(
		&rendererOwner, &rendererSelect, &rendererInsert, &rendererUpdate, &rendererDelete,
	); err != nil {
		t.Fatalf("inspect renderer-state ownership and privileges: %v", err)
	}
	if rendererOwner != testConfig.User || rendererOwner == readinessRestrictedRole || !rendererSelect || rendererInsert || rendererUpdate || rendererDelete {
		t.Fatalf("renderer-state boundary = (owner %q, select %t, insert %t, update %t, delete %t), migration owner %q/runtime %q", rendererOwner, rendererSelect, rendererInsert, rendererUpdate, rendererDelete, testConfig.User, readinessRestrictedRole)
	}
	var searchOwner string
	var searchSelect, searchInsert, searchUpdate, searchDelete bool
	if err := connection.QueryRow(ctx, `SELECT
owner.rolname,
pg_catalog.has_table_privilege($1, 'public.search_projection_state', 'SELECT'),
pg_catalog.has_table_privilege($1, 'public.search_projection_state', 'INSERT'),
pg_catalog.has_table_privilege($1, 'public.search_projection_state', 'UPDATE'),
pg_catalog.has_table_privilege($1, 'public.search_projection_state', 'DELETE')
FROM pg_catalog.pg_class AS search_state
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = search_state.relnamespace
JOIN pg_catalog.pg_roles AS owner ON owner.oid = search_state.relowner
WHERE namespace.nspname = 'public' AND search_state.relname = 'search_projection_state'`, readinessRestrictedRole).Scan(
		&searchOwner, &searchSelect, &searchInsert, &searchUpdate, &searchDelete,
	); err != nil {
		t.Fatalf("inspect search-state ownership and privileges: %v", err)
	}
	if searchOwner != testConfig.User || searchOwner == readinessRestrictedRole || !searchSelect || searchInsert || searchUpdate || searchDelete {
		t.Fatalf("search-state boundary = (owner %q, select %t, insert %t, update %t, delete %t), migration owner %q/runtime %q", searchOwner, searchSelect, searchInsert, searchUpdate, searchDelete, testConfig.User, readinessRestrictedRole)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("restricted Check() rejected exact release after packaged grant: %v", err)
	}
	if _, err := connection.Exec(ctx, `GRANT UPDATE (display_name) ON public.users TO `+roleIdentifier); err != nil {
		t.Fatalf("widen runtime user-column authority: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted runtime UPDATE authority outside the publication tuple")
	}
	if _, err := connection.Exec(ctx, `REVOKE UPDATE (display_name) ON public.users FROM `+roleIdentifier); err != nil {
		t.Fatalf("restore runtime user-column authority: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected restored publication privilege boundary: %v", err)
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.users DROP CONSTRAINT users_publication_window_consistent,
ADD CONSTRAINT users_publication_window_consistent CHECK (true)`); err != nil {
		t.Fatalf("replace publication tuple constraint with impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted a same-name publication CHECK (true) impostor")
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.users DROP CONSTRAINT users_publication_window_consistent,
ADD CONSTRAINT users_publication_window_consistent CHECK (
    (publication_window_started_at IS NULL AND publication_count = 0)
    OR
    (publication_window_started_at IS NOT NULL
     AND pg_catalog.isfinite(publication_window_started_at)
     AND publication_window_started_at >= created_at
     AND publication_count BETWEEN 1 AND 100000)
)`); err != nil {
		t.Fatalf("restore exact publication tuple constraint: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected restored publication tuple constraint: %v", err)
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.users ALTER COLUMN publication_count SET DEFAULT 1`); err != nil {
		t.Fatalf("install publication count default impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted publication count default drift")
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.users ALTER COLUMN publication_count SET DEFAULT 0`); err != nil {
		t.Fatalf("restore publication count default: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected restored publication count default: %v", err)
	}
	if _, err := connection.Exec(ctx, `DROP INDEX public.posts_activity_current_idx;
CREATE INDEX posts_activity_current_idx ON public.posts (id)`); err != nil {
		t.Fatalf("replace search activity index with same-name impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted a same-name search activity index impostor")
	}
	if _, err := connection.Exec(ctx, `DROP INDEX public.posts_activity_current_idx;
CREATE INDEX posts_activity_current_idx ON public.posts (created_at DESC, id DESC)
WHERE deleted_at IS NULL
  AND redacted_at IS NULL
  AND search_projection_version = 'search-v1-pg17-simple-u15-p2'`); err != nil {
		t.Fatalf("restore exact search activity index: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected restored exact search activity index: %v", err)
	}

	if _, err := connection.Exec(ctx, `ALTER TABLE public.content_renderer_state DROP CONSTRAINT content_renderer_state_cursor_progress,
ADD CONSTRAINT content_renderer_state_cursor_progress CHECK (true)`); err != nil {
		t.Fatalf("replace renderer cursor constraint with same-name impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted a same-name validated CHECK (true) renderer cursor constraint")
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.content_renderer_state DROP CONSTRAINT content_renderer_state_cursor_progress,
ADD CONSTRAINT content_renderer_state_cursor_progress CHECK (
    (converted_count = 0 AND last_processed_post_id IS NULL)
    OR (converted_count > 0 AND last_processed_post_id IS NOT NULL)
)`); err != nil {
		t.Fatalf("restore exact renderer cursor constraint: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected restored exact renderer cursor constraint: %v", err)
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.content_renderer_state ALTER COLUMN last_processed_post_id SET DEFAULT 0`); err != nil {
		t.Fatalf("install renderer cursor default impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted renderer cursor column with an unauthorized default")
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.content_renderer_state ALTER COLUMN last_processed_post_id DROP DEFAULT`); err != nil {
		t.Fatalf("remove renderer cursor default impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected restored exact renderer cursor column: %v", err)
	}

	if _, err := connection.Exec(ctx, `ALTER TABLE public.posts DROP CONSTRAINT posts_renderer_version_current,
ADD CONSTRAINT posts_renderer_version_current CHECK (true)`); err != nil {
		t.Fatalf("replace renderer constraint with same-name impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted a same-name validated CHECK (true) renderer constraint")
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.posts DROP CONSTRAINT posts_renderer_version_current,
ADD CONSTRAINT posts_renderer_version_current CHECK (
    renderer_version = 'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2'
	OR (renderer_version = 'goldmark-v1.8.5-bluemonday-v1.0.27-p1-preserved' AND redacted_at IS NULL)
    OR (renderer_version = 'moderation-redaction-v1' AND redacted_at IS NOT NULL)
)`); err != nil {
		t.Fatalf("restore exact renderer constraint: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err != nil {
		t.Fatalf("Check() rejected restored exact renderer constraint: %v", err)
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.posts DROP CONSTRAINT posts_rendered_size,
ADD CONSTRAINT posts_rendered_size CHECK (true)`); err != nil {
		t.Fatalf("replace rendered-size constraint with same-name impostor: %v", err)
	}
	if err := restrictedChecker.Check(ctx); err == nil {
		t.Fatal("Check() accepted a same-name validated CHECK (true) rendered-size constraint")
	}
}
