package migration

import (
	"crypto/sha256"
	"testing"
)

func TestPendingMigrationsAcceptsExactAppliedPrefix(t *testing.T) {
	t.Parallel()

	files := []migrationFile{
		{Version: 1, Name: "000001_initial.sql", SHA256: sha256.Sum256([]byte("one"))},
		{Version: 2, Name: "000002_second.sql", SHA256: sha256.Sum256([]byte("two"))},
	}
	tests := []struct {
		name        string
		applied     []appliedMigration
		wantPending int
	}{
		{name: "fresh database", wantPending: 2},
		{name: "one applied", applied: []appliedMigration{{Version: 1, Name: files[0].Name, SHA256: files[0].SHA256}}, wantPending: 1},
		{name: "all applied", applied: []appliedMigration{
			{Version: 1, Name: files[0].Name, SHA256: files[0].SHA256},
			{Version: 2, Name: files[1].Name, SHA256: files[1].SHA256},
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pending, err := pendingMigrations(files, test.applied)
			if err != nil {
				t.Fatalf("pendingMigrations() returned error: %v", err)
			}
			if len(pending) != test.wantPending {
				t.Fatalf("pendingMigrations() count = %d, want %d", len(pending), test.wantPending)
			}
			if len(pending) > 0 && pending[0].Version != int64(len(test.applied)+1) {
				t.Fatalf("first pending version = %d, want %d", pending[0].Version, len(test.applied)+1)
			}
		})
	}
}

func TestPendingMigrationsRejectsDriftAndInvalidHistory(t *testing.T) {
	t.Parallel()

	firstHash := sha256.Sum256([]byte("one"))
	secondHash := sha256.Sum256([]byte("two"))
	files := []migrationFile{
		{Version: 1, Name: "000001_initial.sql", SHA256: firstHash},
		{Version: 2, Name: "000002_second.sql", SHA256: secondHash},
	}
	tests := map[string][]appliedMigration{
		"version gap":   {{Version: 2, Name: files[0].Name, SHA256: firstHash}},
		"renamed file":  {{Version: 1, Name: "000001_renamed.sql", SHA256: firstHash}},
		"changed bytes": {{Version: 1, Name: files[0].Name, SHA256: secondHash}},
		"unknown version": {
			{Version: 1, Name: files[0].Name, SHA256: firstHash},
			{Version: 2, Name: files[1].Name, SHA256: secondHash},
			{Version: 3, Name: "000003_unknown.sql", SHA256: sha256.Sum256([]byte("three"))},
		},
	}
	for name, applied := range tests {
		name, applied := name, applied
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if pending, err := pendingMigrations(files, applied); err == nil || pending != nil {
				t.Fatalf("pendingMigrations() = (%+v, %v), want (nil, error)", pending, err)
			}
		})
	}
	if pending, err := pendingMigrations(nil, nil); err == nil || pending != nil {
		t.Fatalf("pendingMigrations(nil, nil) = (%+v, %v), want (nil, error)", pending, err)
	}
}
