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
