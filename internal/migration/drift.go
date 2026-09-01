package migration

import (
	"crypto/sha256"
	"fmt"
)

type appliedMigration struct {
	Version int64
	Name    string
	SHA256  [sha256.Size]byte
}

// pendingMigrations proves that database history is an exact prefix of the
// loaded immutable files and returns the unconsumed suffix without copying it.
//
// Complexity: for an applied-row count a, valid-history time is O(a), Omega(a),
// and tight Theta(a); an early mismatch is Omega(1). Auxiliary space is O(1),
// Omega(1), and tight Theta(1); the returned slice aliases the loaded files.
func pendingMigrations(files []migrationFile, applied []appliedMigration) ([]migrationFile, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("loaded migration set is empty")
	}
	if len(applied) > len(files) {
		return nil, fmt.Errorf("database migration history contains an unknown version")
	}
	for index, record := range applied {
		expectedVersion := int64(index + 1)
		if record.Version != expectedVersion {
			return nil, fmt.Errorf("database migration history is not contiguous at version %d", expectedVersion)
		}
		file := files[index]
		if record.Name != file.Name || record.SHA256 != file.SHA256 {
			return nil, fmt.Errorf("database migration drift detected at version %d", expectedVersion)
		}
	}
	return files[len(applied):], nil
}
