package migration

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type controlledFS struct {
	entries []fs.DirEntry
	body    []byte
	dirErr  error
	readErr error
}

func (filesystem controlledFS) Open(string) (fs.File, error) {
	return nil, fs.ErrInvalid
}

func (filesystem controlledFS) ReadDir(string) ([]fs.DirEntry, error) {
	return filesystem.entries, filesystem.dirErr
}

func (filesystem controlledFS) ReadFile(string) ([]byte, error) {
	return filesystem.body, filesystem.readErr
}

type controlledEntry struct {
	name    string
	size    int64
	infoErr error
}

func (entry controlledEntry) Name() string               { return entry.name }
func (entry controlledEntry) IsDir() bool                { return false }
func (entry controlledEntry) Type() fs.FileMode          { return 0 }
func (entry controlledEntry) Info() (fs.FileInfo, error) { return controlledInfo(entry), entry.infoErr }

type controlledInfo controlledEntry

func (info controlledInfo) Name() string       { return info.name }
func (info controlledInfo) Size() int64        { return info.size }
func (info controlledInfo) Mode() fs.FileMode  { return 0 }
func (info controlledInfo) ModTime() time.Time { return time.Time{} }
func (info controlledInfo) IsDir() bool        { return false }
func (info controlledInfo) Sys() any           { return nil }

func TestLoadMigrationsReturnsContiguousHashedFiles(t *testing.T) {
	t.Parallel()

	first := []byte("CREATE TABLE first_table (id bigint PRIMARY KEY);\n")
	second := []byte("CREATE TABLE second_table (id bigint PRIMARY KEY);\n")
	loaded, err := loadMigrations(fstest.MapFS{
		"000001_initial.sql": {Data: first},
		"000002_second.sql":  {Data: second},
	})
	if err != nil {
		t.Fatalf("LoadMigrations() returned error: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("LoadMigrations() count = %d, want 2", len(loaded))
	}
	wants := []struct {
		version int64
		name    string
		sql     string
		hash    [sha256.Size]byte
	}{
		{version: 1, name: "000001_initial.sql", sql: string(first), hash: sha256.Sum256(first)},
		{version: 2, name: "000002_second.sql", sql: string(second), hash: sha256.Sum256(second)},
	}
	for index, want := range wants {
		got := loaded[index]
		if got.Version != want.version || got.Name != want.name || got.SQL != want.sql || got.SHA256 != want.hash {
			t.Fatalf("LoadMigrations()[%d] = %+v, want %+v", index, got, want)
		}
	}
}

func TestLoadMigrationsRejectsInvalidSets(t *testing.T) {
	t.Parallel()

	tests := map[string]fstest.MapFS{
		"empty set":          {},
		"invalid name":       {"1_initial.sql": {Data: []byte("SELECT 1;")}},
		"invalid characters": {"000001_INITIAL.sql": {Data: []byte("SELECT 1;")}},
		"missing first":      {"000002_second.sql": {Data: []byte("SELECT 1;")}},
		"missing middle": {
			"000001_initial.sql": {Data: []byte("SELECT 1;")},
			"000003_third.sql":   {Data: []byte("SELECT 3;")},
		},
		"duplicate version": {
			"000001_first.sql":  {Data: []byte("SELECT 1;")},
			"000001_second.sql": {Data: []byte("SELECT 2;")},
		},
		"empty migration":  {"000001_empty.sql": {Data: nil}},
		"whitespace only":  {"000001_empty.sql": {Data: []byte(" \n\t")}},
		"nested directory": {"nested/000001_initial.sql": {Data: []byte("SELECT 1;")}},
	}
	for name, migrations := range tests {
		name, migrations := name, migrations
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if loaded, err := loadMigrations(migrations); err == nil || loaded != nil {
				t.Fatalf("LoadMigrations() = (%+v, %v), want (nil, error)", loaded, err)
			}
		})
	}
	if loaded, err := loadMigrations(nil); err == nil || loaded != nil {
		t.Fatalf("LoadMigrations(nil) = (%+v, %v), want (nil, error)", loaded, err)
	}
}

func TestLoadMigrationsEnforcesFileSizeBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		size      int
		wantError bool
	}{
		{name: "limit minus one", size: maxMigrationBytes - 1},
		{name: "limit", size: maxMigrationBytes},
		{name: "limit plus one", size: maxMigrationBytes + 1, wantError: true},
		{name: "materially beyond", size: maxMigrationBytes * 4, wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loaded, err := loadMigrations(fstest.MapFS{
				"000001_boundary.sql": {Data: []byte(strings.Repeat("x", test.size))},
			})
			if test.wantError {
				if err == nil || loaded != nil {
					t.Fatalf("LoadMigrations(%d bytes) = (%+v, %v), want (nil, error)", test.size, loaded, err)
				}
				return
			}
			if err != nil || len(loaded) != 1 {
				t.Fatalf("LoadMigrations(%d bytes) = (%+v, %v), want one migration", test.size, loaded, err)
			}
		})
	}
}

func TestLoadMigrationsReportsFilesystemFailuresAndDishonestSize(t *testing.T) {
	t.Parallel()

	failure := errors.New("filesystem failure")
	tests := map[string]controlledFS{
		"directory read": {dirErr: failure},
		"file info": {
			entries: []fs.DirEntry{controlledEntry{name: "000001_initial.sql", infoErr: failure}},
		},
		"file read": {
			entries: []fs.DirEntry{controlledEntry{name: "000001_initial.sql", size: 1}},
			readErr: failure,
		},
		"size changed after info": {
			entries: []fs.DirEntry{controlledEntry{name: "000001_initial.sql", size: 1}},
			body:    []byte(strings.Repeat("x", maxMigrationBytes+1)),
		},
	}
	for name, filesystem := range tests {
		name, filesystem := name, filesystem
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if loaded, err := loadMigrations(filesystem); err == nil || loaded != nil {
				t.Fatalf("LoadMigrations() = (%+v, %v), want (nil, error)", loaded, err)
			}
		})
	}
}
