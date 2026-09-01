package migration

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type appliedRowFixture struct {
	version int64
	name    string
	digest  []byte
}

type stubAppliedRows struct {
	pgx.Rows
	fixtures []appliedRowFixture
	next     int
	scanErr  error
	rowsErr  error
	closed   bool
}

func (rows *stubAppliedRows) Close() { rows.closed = true }

func (rows *stubAppliedRows) Err() error { return rows.rowsErr }

func (rows *stubAppliedRows) Next() bool {
	if rows.next >= len(rows.fixtures) {
		return false
	}
	rows.next++
	return true
}

func (rows *stubAppliedRows) Scan(dest ...any) error {
	if rows.scanErr != nil {
		return rows.scanErr
	}
	fixture := rows.fixtures[rows.next-1]
	*dest[0].(*int64) = fixture.version
	*dest[1].(*string) = fixture.name
	*dest[2].(*[]byte) = fixture.digest
	return nil
}

func TestScanAppliedMigrationsCopiesExactRowsAndCloses(t *testing.T) {
	t.Parallel()

	firstDigest := sha256.Sum256([]byte("one"))
	secondDigest := sha256.Sum256([]byte("two"))
	rows := &stubAppliedRows{fixtures: []appliedRowFixture{
		{version: 1, name: "000001_initial.sql", digest: firstDigest[:]},
		{version: 2, name: "000002_second.sql", digest: secondDigest[:]},
	}}
	applied, err := scanAppliedMigrations(rows)
	if err != nil {
		t.Fatalf("scanAppliedMigrations() returned error: %v", err)
	}
	if !rows.closed {
		t.Fatal("scanAppliedMigrations() did not close rows")
	}
	want := []appliedMigration{
		{Version: 1, Name: "000001_initial.sql", SHA256: firstDigest},
		{Version: 2, Name: "000002_second.sql", SHA256: secondDigest},
	}
	if len(applied) != len(want) {
		t.Fatalf("scanAppliedMigrations() count = %d, want %d", len(applied), len(want))
	}
	for index := range want {
		if applied[index] != want[index] {
			t.Fatalf("scanAppliedMigrations()[%d] = %+v, want %+v", index, applied[index], want[index])
		}
	}
	originalFirstDigest := firstDigest
	rows.fixtures[0].digest[0]++
	if applied[0].SHA256 != originalFirstDigest {
		t.Fatal("scanAppliedMigrations() retained the driver's digest buffer")
	}
}

func TestScanAppliedMigrationsRejectsInvalidRows(t *testing.T) {
	t.Parallel()

	failure := errors.New("row failure")
	tests := map[string]*stubAppliedRows{
		"scan failure":      {fixtures: []appliedRowFixture{{}}, scanErr: failure},
		"iteration failure": {rowsErr: failure},
		"short digest":      {fixtures: []appliedRowFixture{{version: 1, name: "000001_initial.sql", digest: make([]byte, sha256.Size-1)}}},
		"long digest":       {fixtures: []appliedRowFixture{{version: 1, name: "000001_initial.sql", digest: make([]byte, sha256.Size+1)}}},
	}
	for name, rows := range tests {
		name, rows := name, rows
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if applied, err := scanAppliedMigrations(rows); err == nil || applied != nil {
				t.Fatalf("scanAppliedMigrations() = (%+v, %v), want (nil, error)", applied, err)
			}
			if !rows.closed {
				t.Fatal("scanAppliedMigrations() did not close rows after failure")
			}
		})
	}
	if applied, err := scanAppliedMigrations(nil); err == nil || applied != nil {
		t.Fatalf("scanAppliedMigrations(nil) = (%+v, %v), want (nil, error)", applied, err)
	}
}
