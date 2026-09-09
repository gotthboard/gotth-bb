package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestFilesReturnsOnlyContiguousSQLMigrations(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(Files(), ".")
	if err != nil {
		t.Fatalf("fs.ReadDir(Files()) returned error: %v", err)
	}
	want := []string{
		"000001_identity_and_sessions.sql",
		"000002_groups_and_areas.sql",
		"000003_topics_posts_and_reads.sql",
		"000004_reports_and_audit.sql",
		"000005_threaded_posts.sql",
		"000006_reports_moderation_completion.sql",
		"000007_gfm_renderer.sql",
		"000008_search_projection.sql",
		"000009_unread_state.sql",
		"000010_administration_completion.sql",
		"000011_publication_limits.sql",
		"000012_external_identity_rebind.sql",
		"000013_board_control_plane.sql",
	}
	if len(entries) != len(want) {
		t.Fatalf("Files() entry count = %d, want %d", len(entries), len(want))
	}
	for index, entry := range entries {
		if entry.IsDir() || entry.Name() != want[index] {
			t.Fatalf("Files()[%d] = (%q, directory %t), want (%q, false)", index, entry.Name(), entry.IsDir(), want[index])
		}
		body, err := fs.ReadFile(Files(), entry.Name())
		if err != nil || len(body) == 0 {
			t.Fatalf("read %s = (%d bytes, %v), want nonempty SQL", entry.Name(), len(body), err)
		}
	}
}

func TestBoardControlPlaneSchemaStepIsClosedAndDoesNotCrossDatabases(t *testing.T) {
	t.Parallel()

	body, err := fs.ReadFile(Files(), "000013_board_control_plane.sql")
	if err != nil {
		t.Fatalf("read board-control-plane migration: %v", err)
	}
	sql := string(body)
	for _, forbidden := range []string{
		"authentik_core", "authentik_stages", "dblink", "postgres_fdw",
		"DELETE FROM", "DROP TABLE", "CREATE EXTENSION",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("board-control-plane migration contains forbidden boundary %q", forbidden)
		}
	}
	for _, required := range []string{
		"registration_mode text NOT NULL DEFAULT 'closed'",
		"maintenance_enabled boolean NOT NULL DEFAULT false",
		"site_settings_registration_mode_closed",
		"site_settings_publication_limits_positive",
		"site_settings_session_limits_positive",
		"authentik_sync_state text NOT NULL DEFAULT 'unknown'",
		"UPDATE public.users",
		"CREATE TABLE public.pending_registrations",
		"CREATE TABLE public.registration_invitations",
		"CREATE TABLE public.email_test_state",
		"octet_length(request_fingerprint) = 32",
		"'update_control_settings'",
		"'record_registration_transition_result'",
		"'record_invitation_result'",
		"'reconcile_identity_access'",
		"'gotth-bb-expiry-reconciler'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("board-control-plane migration lacks required contract %q", required)
		}
	}
}

func TestExternalIdentityRebindMigrationAddsOnlyTheGovernedAuditAction(t *testing.T) {
	t.Parallel()

	body, err := fs.ReadFile(Files(), "000012_external_identity_rebind.sql")
	if err != nil {
		t.Fatalf("read external-identity-rebind migration: %v", err)
	}
	sql := string(body)
	for _, forbidden := range []string{
		"UPDATE public.external_identities",
		"UPDATE public.sessions",
		"DELETE FROM public.oidc_login_attempts",
		"INSERT INTO public.moderation_actions",
		"CREATE TABLE",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("external-identity-rebind migration contains operator work %q", forbidden)
		}
	}
	if !strings.Contains(sql, "'rebind_external_identity'") ||
		strings.Count(sql, "DROP CONSTRAINT") != 1 || strings.Count(sql, "ADD CONSTRAINT") != 1 {
		t.Fatal("external-identity-rebind migration does not make the one bounded audit action admissible")
	}
}

func TestPublicationLimitSchemaStepIsAdditiveAndDoesNotSpend(t *testing.T) {
	t.Parallel()

	body, err := fs.ReadFile(Files(), "000011_publication_limits.sql")
	if err != nil {
		t.Fatalf("read publication-limit migration: %v", err)
	}
	sql := string(body)
	for _, forbidden := range []string{
		"UPDATE public.users",
		"DELETE FROM public.users",
		"CREATE INDEX",
		"CREATE TRIGGER",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("publication-limit migration contains forbidden work %q", forbidden)
		}
	}
	for _, required := range []string{
		"publication_window_started_at timestamp with time zone",
		"publication_count integer NOT NULL DEFAULT 0",
		"users_created_at_finite",
		"pg_catalog.isfinite(created_at)",
		"users_publication_window_consistent",
		"pg_catalog.isfinite(publication_window_started_at)",
		"publication_window_started_at >= created_at",
		"publication_count BETWEEN 1 AND 100000",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("publication-limit migration lacks required contract %q", required)
		}
	}
}

func TestAdministrationCompletionSchemaStepIsBoundedAndAuditable(t *testing.T) {
	t.Parallel()

	body, err := fs.ReadFile(Files(), "000010_administration_completion.sql")
	if err != nil {
		t.Fatalf("read administration-completion migration: %v", err)
	}
	sql := string(body)
	for _, forbidden := range []string{
		"UPDATE public.users",
		"UPDATE public.forum_groups",
		"UPDATE public.areas",
		"CREATE INDEX CONCURRENTLY",
		"DROP TABLE",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("administration-completion migration contains forbidden work %q", forbidden)
		}
	}
	for _, required := range []string{
		"CREATE TABLE public.site_settings",
		"site_settings_singleton_true",
		"site_settings_rules_tuple_current",
		"goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2",
		"users_administration_revision_positive",
		"forum_groups_administration_revision_positive",
		"areas_administration_revision_positive",
		"ADD COLUMN target_site boolean",
		"moderation_actions_target_site_true",
		"'create_group', 'rename_group'",
		"'grant_area_group', 'revoke_area_group'",
		"'update_site_settings'",
		"octet_length(reason) >= 1",
		"octet_length(reason) <= 2000",
		"reason !~ '[[:cntrl:]]'",
		"reason = pg_catalog.btrim(reason, ' ')",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("administration-completion migration lacks required contract %q", required)
		}
	}
}

func TestUnreadStateSchemaStepIsAdditiveAndDoesNotInventMarkers(t *testing.T) {
	t.Parallel()

	body, err := fs.ReadFile(Files(), "000009_unread_state.sql")
	if err != nil {
		t.Fatalf("read unread-state migration: %v", err)
	}
	sql := string(body)
	for _, forbidden := range []string{
		"INSERT INTO public.topic_reads",
		"UPDATE public.topic_reads",
		"DELETE FROM public.topic_reads",
		"CREATE INDEX CONCURRENTLY",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("unread-state migration contains forbidden work %q", forbidden)
		}
	}
	for _, required := range []string{
		"topic_reads_read_at_finite",
		"pg_catalog.isfinite(read_at)",
		"NOT VALID",
		"VALIDATE CONSTRAINT topic_reads_read_at_finite",
		"posts_topic_unread_visible_idx",
		"ON public.posts (topic_id, post_number)",
		"INCLUDE (author_id)",
		"deleted_at IS NULL",
		"redacted_at IS NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("unread-state migration lacks required contract %q", required)
		}
	}
}

func TestSearchProjectionSchemaStepIsRestartablePopulationScaffolding(t *testing.T) {
	t.Parallel()

	body, err := fs.ReadFile(Files(), "000008_search_projection.sql")
	if err != nil {
		t.Fatalf("read search projection migration: %v", err)
	}
	sql := string(body)
	for _, forbidden := range []string{
		"VALIDATE CONSTRAINT",
		"UPDATE public.topics",
		"UPDATE public.posts",
		"search_visible_text",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("search projection schema step contains forbidden population work %q", forbidden)
		}
	}
	for _, required := range []string{
		"search_vector tsvector",
		"search_projection_version text",
		"CREATE TABLE public.search_projection_state",
		"CONSTRAINT search_projection_state_target_current",
		"CONSTRAINT search_projection_state_shape",
		"CONSTRAINT search_projection_state_completion_finite",
		"topics_search_projection_current",
		"posts_search_projection_current",
		"topics_search_created_finite",
		"posts_search_created_finite",
		"topics_search_id_positive",
		"posts_search_id_positive",
		"topics_search_vector_current_idx",
		"posts_search_vector_current_idx",
		"topics_search_author_current_idx",
		"posts_search_author_current_idx",
		"posts_activity_current_idx",
		"topics_validate_post_state_insert",
		"topics_validate_post_state_update",
		"topics_validate_post_state_delete",
		"posts_validate_topic_state_insert",
		"posts_validate_topic_state_update",
		"posts_validate_topic_state_delete",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("search projection schema step lacks required contract %q", required)
		}
	}
	if got := strings.Count(sql, ") NOT VALID"); got != 6 {
		t.Fatalf("search projection NOT VALID constraint count = %d, want 6", got)
	}
}

func TestGFMRendererSchemaStepIsMetadataOnlyAndIncomplete(t *testing.T) {
	t.Parallel()

	body, err := fs.ReadFile(Files(), "000007_gfm_renderer.sql")
	if err != nil {
		t.Fatalf("read renderer migration: %v", err)
	}
	sql := string(body)
	for _, forbidden := range []string{
		"FROM public.posts",
		"VALIDATE CONSTRAINT",
		"DROP CONSTRAINT posts_rendered_size",
		"1310720",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("renderer schema step contains forbidden populated-table work %q", forbidden)
		}
	}
	for _, required := range []string{
		"'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2',\n    NULL",
		"last_processed_post_id bigint",
		"content_renderer_state_cursor_progress",
		"goldmark-v1.8.5-bluemonday-v1.0.27-p1-preserved",
		"ADD CONSTRAINT posts_renderer_version_current",
		") NOT VALID",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("renderer schema step lacks required contract %q", required)
		}
	}
}
