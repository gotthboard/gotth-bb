package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

const transactionRollbackTimeout = 5 * time.Second

var errNilTransaction = errors.New("transaction begin returned no transaction")

// WithinTx runs one callback against sqlc queries bound to a new pgx
// transaction. It never retries a callback or an unknown commit outcome.
//
// Complexity: local work and auxiliary space are tight Theta(1). Total time is
// delegated begin B, callback W, commit C, and possible rollback R work:
// O(B+W+C+R), Omega(W), with no tighter Theta bound because database and
// callback costs are external.
func WithinTx(ctx context.Context, beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}, action func(*db.Queries) error) (result error) {
	if ctx == nil {
		return fmt.Errorf("transaction context is required")
	}
	if beginner == nil {
		return fmt.Errorf("transaction beginner is required")
	}
	if action == nil {
		return fmt.Errorf("transaction action is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("transaction canceled: %w", err)
	}
	tx, err := beginner.Begin(ctx)
	if tx == nil {
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		return errNilTransaction
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), transactionRollbackTimeout)
		defer cancel()
		if err := tx.Rollback(rollbackContext); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			result = errors.Join(result, fmt.Errorf("rollback transaction: %w", err))
		}
	}()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := action(db.New(tx)); err != nil {
		return fmt.Errorf("transaction action failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction (outcome unknown; inspect state before retry): %w", err)
	}
	committed = true
	return nil
}
