package migration

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
)

const maxMigrationBytes = 1 << 20

var migrationNamePattern = regexp.MustCompile(`^([0-9]{6})_[a-z0-9]+(?:_[a-z0-9]+)*\.sql$`)

// migrationFile is one loaded, forward-only database state transition.
type migrationFile struct {
	Version int64
	Name    string
	SQL     string
	SHA256  [sha256.Size]byte
}

// loadMigrations validates a flat, contiguous migration set and hashes the
// exact bytes that will be executed and recorded in PostgreSQL.
//
// Complexity: for f files, p filename bytes, m migration bytes, and delegated
// directory-read cost D(f), valid-set time is O(D(f)+p+m), Omega(f+m), with no
// tighter Theta bound established because fs.ReadDir implementation cost is
// interface-dependent. Returned and peak auxiliary space are O(f+m),
// Omega(f+m), and tight Theta(f+m): every valid migration and SQL byte is
// retained in the result while each file is read and converted once.
func loadMigrations(filesystem fs.FS) ([]migrationFile, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("migration filesystem is required")
	}
	entries, err := fs.ReadDir(filesystem, ".")
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("at least one migration is required")
	}
	loaded := make([]migrationFile, 0, len(entries))
	for index, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("migration directory must be flat")
		}
		matches := migrationNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("migration filename is invalid: %s", entry.Name())
		}
		version := int64(0)
		for offset := 0; offset < len(matches[1]); offset++ {
			version = version*10 + int64(matches[1][offset]-'0')
		}
		expectedVersion := int64(index + 1)
		if version != expectedVersion {
			return nil, fmt.Errorf("migration version %06d is required", expectedVersion)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect migration %s: %w", entry.Name(), err)
		}
		if info.Size() < 1 || info.Size() > maxMigrationBytes {
			return nil, fmt.Errorf("migration %s size is invalid", entry.Name())
		}
		body, err := fs.ReadFile(filesystem, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if len(body) > maxMigrationBytes || len(bytes.TrimSpace(body)) == 0 {
			return nil, fmt.Errorf("migration %s content is invalid", entry.Name())
		}
		loaded = append(loaded, migrationFile{
			Version: version,
			Name:    entry.Name(),
			SQL:     string(body),
			SHA256:  sha256.Sum256(body),
		})
	}
	return loaded, nil
}
