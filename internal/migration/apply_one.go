package migration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	insertMigrationRecordSQL = `INSERT INTO public.gotth_schema_migrations (version, name, sha256)
VALUES ($1, $2, $3)`
	migrationRollbackTimeout = 5 * time.Second
)

var errNilMigrationTransaction = errors.New("migration begin returned no transaction")

type migrationBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// applyMigration executes one previously loaded immutable file and its ledger
// record in the same PostgreSQL transaction. Commit failures have an unknown
// outcome and must be resolved by reading the ledger before any retry.
//
// Complexity: for s SQL bytes plus delegated begin B, execution E(s), ledger I,
// commit C, and possible rollback R costs, time is O(s+B+E(s)+I+C+R), Omega(s),
// with no tighter Theta bound because PostgreSQL work is external. Local
// auxiliary space is O(s), Omega(1), with no tighter Theta bound established:
// the compiler may or may not elide the temporary string-to-byte hash view.
func applyMigration(ctx context.Context, beginner migrationBeginner, file migrationFile) (result error) {
	if ctx == nil {
		return fmt.Errorf("migration transaction context is required")
	}
	if beginner == nil {
		return fmt.Errorf("migration transaction connection is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration transaction canceled: %w", err)
	}
	matches := migrationNamePattern.FindStringSubmatch(file.Name)
	if file.Version < 1 || matches == nil || matches[1] != fmt.Sprintf("%06d", file.Version) {
		return fmt.Errorf("migration identity is invalid")
	}
	if len(file.SQL) > maxMigrationBytes || len(strings.TrimSpace(file.SQL)) == 0 {
		return fmt.Errorf("migration SQL is invalid")
	}
	if sha256.Sum256([]byte(file.SQL)) != file.SHA256 {
		return fmt.Errorf("migration digest does not match SQL")
	}

	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %06d: %w", file.Version, err)
	}
	if tx == nil {
		return errNilMigrationTransaction
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), migrationRollbackTimeout)
		defer cancel()
		if err := tx.Rollback(rollbackContext); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			result = errors.Join(result, fmt.Errorf("rollback migration %06d: %w", file.Version, err))
		}
	}()

	if _, err := tx.Exec(ctx, file.SQL); err != nil {
		return fmt.Errorf("execute migration %06d: %w", file.Version, err)
	}
	if _, err := tx.Exec(ctx, insertMigrationRecordSQL, file.Version, file.Name, file.SHA256[:]); err != nil {
		return fmt.Errorf("record migration %06d: %w", file.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %06d (outcome unknown; inspect ledger before retry): %w", file.Version, err)
	}
	committed = true
	return nil
}
