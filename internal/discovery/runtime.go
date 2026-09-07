package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const discoveryRollbackTimeout = 5 * time.Second

type transactionDatabase interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Search opens the mandatory read-only repeatable-read snapshot, installs the
// bounded PostgreSQL settings, and commits only a completely validated page.
//
// Complexity: local work is tight Theta(1) around searchWithQuerier; total
// costs inherit its O(r+h) local bound and delegated transaction/query work.
func Search(ctx context.Context, database transactionDatabase, request SearchRequest, actor policy.AccessContext) (SearchPage, error) {
	var page SearchPage
	err := withDiscoveryTransaction(ctx, database, "5s", func(queries *db.Queries) error {
		var err error
		page, err = searchWithQuerier(ctx, queries, request, actor)
		return err
	})
	if err != nil {
		return SearchPage{}, err
	}
	return page, nil
}

// RecentActivity samples PostgreSQL time, checks active-key issuance and any
// already-MAC-authenticated cursor, queries one bounded page, and encodes its
// continuation inside the same read-only repeatable-read transaction.
//
// Complexity: with g groups and r <= 26 rows, local time is O(g+r), Omega(1),
// tight Theta(g+r) for a full authenticated page; returned space is O(r).
func RecentActivity(ctx context.Context, database transactionDatabase, verified *AuthenticatedCursor, ring CursorKeyring, actor policy.AccessContext) (ActivityPage, error) {
	var page ActivityPage
	err := withDiscoveryTransaction(ctx, database, "2s", func(queries *db.Queries) error {
		databaseTime, err := queries.GetDiscoveryDatabaseTime(ctx)
		if err != nil || !validFiniteTimestamp(databaseTime) {
			return fmt.Errorf("sample activity database time")
		}
		now := databaseTime.Time.UTC()
		if !validCursorKey(ring.Active) || now.Before(ring.Active.NotBefore) || now.After(ring.Active.IssueNotAfter) {
			return fmt.Errorf("activity cursor key is not issuing")
		}
		boundary := ActivityBoundary{}
		if verified != nil {
			if !verified.belongsTo(ring) {
				return fmt.Errorf("activity cursor keyring changed")
			}
			if err := verified.ValidateTime(now); err != nil {
				return err
			}
			boundary, err = verified.BindAudience(actor)
			if err != nil {
				return err
			}
		}
		page, err = activityWithQuerier(ctx, databaseActivityQuerier{queries: queries}, boundary, actor)
		if err != nil {
			return err
		}
		if page.NextBoundary != nil {
			page.NextCursor, err = ring.EncodeCursor(now, *page.NextBoundary, actor)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ActivityPage{}, err
	}
	return page, nil
}

type databaseActivityQuerier struct {
	queries *db.Queries
}

// ListRecentActivity selects the initial or strict-boundary generated query
// and converts its bounded rows into the package representation.
//
// Complexity: with r <= 26 rows, time and auxiliary space are tight Theta(r).
func (adapter databaseActivityQuerier) ListRecentActivity(ctx context.Context, parameters activityQueryParameters) ([]activityQueryRow, error) {
	if parameters.Boundary.PostID == 0 {
		rows, err := adapter.queries.ListRecentActivityInitial(ctx, db.ListRecentActivityInitialParams{
			IsStaff: parameters.IsStaff, IsMember: parameters.IsMember, GroupIds: parameters.GroupIDs,
		})
		if err != nil {
			return nil, err
		}
		converted := make([]activityQueryRow, len(rows))
		for index, row := range rows {
			converted[index] = activityQueryRow(row)
		}
		return converted, nil
	}
	rows, err := adapter.queries.ListRecentActivityAfter(ctx, db.ListRecentActivityAfterParams{
		IsStaff: parameters.IsStaff, IsMember: parameters.IsMember, GroupIds: parameters.GroupIDs,
		CursorCreatedAt: pgtype.Timestamptz{Time: parameters.Boundary.CreatedAt, Valid: true}, CursorPostID: parameters.Boundary.PostID,
	})
	if err != nil {
		return nil, err
	}
	converted := make([]activityQueryRow, len(rows))
	for index, row := range rows {
		converted[index] = activityQueryRow(row)
	}
	return converted, nil
}

// withDiscoveryTransaction applies one read-only repeatable-read snapshot and
// bounded local PostgreSQL settings around the supplied query sequence.
//
// Complexity: local time and auxiliary space are tight Theta(1); database
// work is bounded by the caller's query and the configured timeout.
func withDiscoveryTransaction(ctx context.Context, database transactionDatabase, statementTimeout string, action func(*db.Queries) error) (result error) {
	if ctx == nil || database == nil || action == nil {
		return fmt.Errorf("discovery transaction input is invalid")
	}
	tx, err := database.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil || tx == nil {
		return fmt.Errorf("begin discovery transaction")
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryRollbackTimeout)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackContext); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			result = errors.Join(result, fmt.Errorf("rollback discovery transaction: %w", rollbackErr))
		}
	}()
	settings := `SELECT
set_config('statement_timeout', $1, true),
set_config('lock_timeout', '250ms', true),
set_config('work_mem', '4MB', true),
set_config('max_parallel_workers_per_gather', '0', true),
set_config('jit', 'off', true)`
	if _, err := tx.Exec(ctx, settings, statementTimeout); err != nil {
		return fmt.Errorf("configure discovery transaction")
	}
	if err := action(db.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit discovery transaction")
	}
	committed = true
	return nil
}
