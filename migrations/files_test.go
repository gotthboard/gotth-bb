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
