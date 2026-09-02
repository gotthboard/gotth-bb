package migrations

import (
	"io/fs"
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
