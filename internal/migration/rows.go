package migration

import (
	"crypto/sha256"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// scanAppliedMigrations owns and drains an ordered migration-history result
// set, copying each driver-owned digest into immutable application storage.
// The query issuing the rows must order them by version.
//
// Complexity: for an applied-row count a, time is O(a), Omega(a), and tight Theta(a).
// Returned and auxiliary space are O(a), Omega(a), and tight Theta(a).
func scanAppliedMigrations(rows pgx.Rows) ([]appliedMigration, error) {
	if rows == nil {
		return nil, fmt.Errorf("applied migration rows are required")
	}
	defer rows.Close()

	var applied []appliedMigration
	for rows.Next() {
		var record appliedMigration
		var digest []byte
		if err := rows.Scan(&record.Version, &record.Name, &digest); err != nil {
			return nil, fmt.Errorf("scan applied migration row: %w", err)
		}
		if len(digest) != sha256.Size {
			return nil, fmt.Errorf("applied migration digest length is invalid")
		}
		copy(record.SHA256[:], digest)
		applied = append(applied, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read applied migration rows: %w", err)
	}
	return applied, nil
}
