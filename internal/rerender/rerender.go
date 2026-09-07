// Package rerender rebuilds persisted derived post HTML after an immutable
// renderer-version change.
package rerender

import (
	"context"
	"errors"
	"fmt"
	"time"

	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaximumBatchSize               = 100
	maximumAttempts                = 3
	rollbackTimeout                = 5 * time.Second
	legacyPreservedRendererVersion = "goldmark-v1.8.5-bluemonday-v1.0.27-p1-preserved"
)

const selectStalePostsSQL = `SELECT id, markdown_source, rendered_html, renderer_version
FROM public.posts
WHERE redacted_at IS NULL
  AND renderer_version <> $1
  AND renderer_version <> $2
ORDER BY id
LIMIT $3
FOR UPDATE`

const lockRendererStateSQL = `SELECT target_version
FROM public.content_renderer_state
WHERE singleton
FOR UPDATE`

const updateRenderedPostSQL = `UPDATE public.posts
SET rendered_html = $1,
    renderer_version = $2
WHERE id = $3
  AND redacted_at IS NULL
  AND renderer_version = $4
  AND markdown_source = $5
  AND rendered_html = $6`

const recordBatchSQL = `UPDATE public.content_renderer_state
SET converted_count = converted_count + $1,
    completed_at = NULL
WHERE singleton
  AND target_version = $2`

const completeMigrationSQL = `UPDATE public.content_renderer_state
SET completed_at = COALESCE(completed_at, clock_timestamp())
WHERE singleton
  AND target_version = $1
  AND NOT EXISTS (
      SELECT 1
      FROM public.posts
      WHERE redacted_at IS NULL
        AND renderer_version <> $1
        AND renderer_version <> $2
  )`

type database interface {
	Begin(context.Context) (pgx.Tx, error)
}

type preflightDatabase interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// BatchResult is one committed transaction's content-free progress record.
type BatchResult struct {
	Converted int
	Complete  bool
}

type post struct {
	id              int64
	markdown        string
	originalHTML    string
	originalVersion string
	nextHTML        string
	nextVersion     string
}

// Run processes finite stale rows in bounded transactions until one committed
// empty batch proves completion. Serialization and deadlock failures before a
// commit attempt are retried at most three times; commit-unknown outcomes are
// returned for operator inspection and safe command rerun.
//
// Complexity: for p stale posts, source bytes n, rendered bytes h, batch size
// b <= 100, and retry count r <= 3 per batch, time is O(r*(p+n+h)) and
// Omega(p+n+h), with no tight Theta bound because database I/O and retry
// occurrence vary; auxiliary space is O(b*65,536+b*262,144), Omega(1), bounded
// by the source, persisted-output, and batch limits. Durable renderer state is
// the sole progress record; the loop performs no synchronous output I/O.
func Run(ctx context.Context, database database, batchSize int) error {
	if ctx == nil {
		return fmt.Errorf("renderer migration context is required")
	}
	if database == nil {
		return fmt.Errorf("renderer migration database is required")
	}
	if batchSize < 1 || batchSize > MaximumBatchSize {
		return fmt.Errorf("renderer migration batch size must be between 1 and %d", MaximumBatchSize)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("renderer migration canceled: %w", err)
	}
	for {
		var result BatchResult
		var err error
		for attempt := 1; attempt <= maximumAttempts; attempt++ {
			var retryable bool
			result, retryable, err = runBatch(ctx, database, batchSize)
			if err == nil || !retryable || attempt == maximumAttempts {
				break
			}
		}
		if err != nil {
			return err
		}
		if result.Complete {
			return nil
		}
	}
}

// Preflight proves, without mutation, that every existing post can cross the
// Alpha.3 renderer boundary before migration 000007 installs its incompatible
// writer constraint. It uses one repeatable-read, read-only snapshot and
// retains at most batchSize rows. The required release stop/drain prevents a
// writer from changing rows after this snapshot and before schema apply; this
// function does not pretend those separate operations are atomic.
func Preflight(ctx context.Context, database preflightDatabase, batchSize int) (resultErr error) {
	if ctx == nil {
		return fmt.Errorf("renderer preflight context is required")
	}
	if database == nil {
		return fmt.Errorf("renderer preflight database is required")
	}
	if batchSize < 1 || batchSize > MaximumBatchSize {
		return fmt.Errorf("renderer preflight batch size must be between 1 and %d", MaximumBatchSize)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("renderer preflight canceled: %w", err)
	}
	tx, err := database.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin renderer preflight snapshot: %w", err)
	}
	if tx == nil {
		return fmt.Errorf("begin renderer preflight snapshot returned no transaction")
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackContext); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback renderer preflight snapshot: %w", rollbackErr))
		}
	}()
	var postsExist bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.posts') IS NOT NULL`).Scan(&postsExist); err != nil {
		return fmt.Errorf("inspect renderer preflight schema: %w", err)
	}
	if !postsExist {
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if err := tx.Rollback(rollbackContext); err != nil {
			return fmt.Errorf("close empty renderer preflight snapshot: %w", err)
		}
		closed = true
		return nil
	}
	var historyExists bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.gotth_schema_migrations') IS NOT NULL`).Scan(&historyExists); err != nil {
		return fmt.Errorf("inspect renderer preflight ledger: %w", err)
	}
	alpha3Applied := false
	if historyExists {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.gotth_schema_migrations WHERE version = 7 AND name = '000007_gfm_renderer.sql')`).Scan(&alpha3Applied); err != nil {
			return fmt.Errorf("inspect renderer preflight Alpha.3 ledger state: %w", err)
		}
	}
	var afterID *int64
	for {
		rows, err := tx.Query(ctx, `SELECT post.id,
post.markdown_source,
post.rendered_html,
post.renderer_version,
COALESCE((pg_catalog.to_jsonb(post)->>'redacted_at') IS NOT NULL, false)
FROM public.posts AS post
WHERE $1::bigint IS NULL OR post.id > $1
ORDER BY post.id
LIMIT $2`, afterID, int32(batchSize))
		if err != nil {
			return fmt.Errorf("select renderer preflight rows: %w", err)
		}
		batchCount := 0
		for rows.Next() {
			var candidate post
			var redacted bool
			if err := rows.Scan(&candidate.id, &candidate.markdown, &candidate.originalHTML, &candidate.originalVersion, &redacted); err != nil {
				rows.Close()
				return fmt.Errorf("scan renderer preflight row: %w", err)
			}
			if err := preflightPost(ctx, &candidate, redacted, alpha3Applied); err != nil {
				rows.Close()
				return fmt.Errorf("classify renderer preflight post %d: %w", candidate.id, err)
			}
			lastID := candidate.id
			afterID = &lastID
			batchCount++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate renderer preflight rows: %w", err)
		}
		rows.Close()
		if batchCount < batchSize {
			break
		}
	}
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := tx.Rollback(rollbackContext); err != nil {
		return fmt.Errorf("close renderer preflight snapshot: %w", err)
	}
	closed = true
	return nil
}

// runBatch locks and converts at most batchSize current rows, or validates the
// writer constraint and completion oracle when no stale row remains.
//
// Complexity: for b selected rows, n source bytes, and h rendered bytes, time
// is O(b+n+h), Omega(1), with no tight Theta bound because database costs vary;
// auxiliary/returned space is O(b+n+h), Omega(1), bounded by the post size
// constraints and b <= 100.
func runBatch(ctx context.Context, database database, batchSize int) (result BatchResult, retryable bool, resultErr error) {
	tx, err := database.Begin(ctx)
	if err != nil {
		return BatchResult{}, isRetryable(err), fmt.Errorf("begin renderer migration batch: %w", err)
	}
	if tx == nil {
		return BatchResult{}, false, fmt.Errorf("begin renderer migration batch returned no transaction")
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackContext); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback renderer migration batch: %w", rollbackErr))
			retryable = false
		}
	}()
	var targetVersion string
	if err := tx.QueryRow(ctx, lockRendererStateSQL).Scan(&targetVersion); err != nil {
		return BatchResult{}, isRetryable(err), fmt.Errorf("lock renderer migration state: %w", err)
	}
	if targetVersion != contentrender.RendererVersion {
		return BatchResult{}, false, fmt.Errorf("renderer migration target does not match this release")
	}
	rows, err := tx.Query(ctx, selectStalePostsSQL, contentrender.RendererVersion, legacyPreservedRendererVersion, int32(batchSize))
	if err != nil {
		return BatchResult{}, isRetryable(err), fmt.Errorf("select stale renderer rows: %w", err)
	}
	posts := make([]post, 0, batchSize)
	for rows.Next() {
		var candidate post
		if err := rows.Scan(&candidate.id, &candidate.markdown, &candidate.originalHTML, &candidate.originalVersion); err != nil {
			rows.Close()
			return BatchResult{}, isRetryable(err), fmt.Errorf("scan stale renderer row: %w", err)
		}
		if err := preparePost(ctx, &candidate); err != nil {
			rows.Close()
			return BatchResult{}, false, fmt.Errorf("prepare stale post: %w", err)
		}
		posts = append(posts, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return BatchResult{}, isRetryable(err), fmt.Errorf("iterate stale renderer rows: %w", err)
	}
	rows.Close()
	if len(posts) == 0 {
		if _, err := tx.Exec(ctx, `ALTER TABLE public.posts VALIDATE CONSTRAINT posts_renderer_version_current`); err != nil {
			return BatchResult{}, isRetryable(err), fmt.Errorf("validate current renderer constraint: %w", err)
		}
		tag, err := tx.Exec(ctx, completeMigrationSQL, contentrender.RendererVersion, legacyPreservedRendererVersion)
		if err != nil {
			return BatchResult{}, isRetryable(err), fmt.Errorf("record renderer migration completion: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return BatchResult{}, false, fmt.Errorf("renderer migration completion oracle changed no state")
		}
		if err := tx.Commit(ctx); err != nil {
			return BatchResult{}, false, fmt.Errorf("commit renderer migration completion (outcome unknown; inspect state before retry): %w", err)
		}
		committed = true
		return BatchResult{Complete: true}, false, nil
	}
	for _, candidate := range posts {
		tag, err := tx.Exec(
			ctx,
			updateRenderedPostSQL,
			candidate.nextHTML,
			candidate.nextVersion,
			candidate.id,
			candidate.originalVersion,
			candidate.markdown,
			candidate.originalHTML,
		)
		if err != nil {
			return BatchResult{}, isRetryable(err), fmt.Errorf("update stale renderer row: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return BatchResult{}, false, fmt.Errorf("locked stale renderer row changed unexpectedly")
		}
	}
	tag, err := tx.Exec(ctx, recordBatchSQL, int64(len(posts)), contentrender.RendererVersion)
	if err != nil {
		return BatchResult{}, isRetryable(err), fmt.Errorf("record renderer migration batch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return BatchResult{}, false, fmt.Errorf("renderer migration target state is missing")
	}
	if err := tx.Commit(ctx); err != nil {
		return BatchResult{}, false, fmt.Errorf("commit renderer migration batch (outcome unknown; inspect state before retry): %w", err)
	}
	committed = true
	return BatchResult{Converted: len(posts)}, false, nil
}

// preparePost chooses the ordinary p2 result or the sole compatibility path.
// Compatibility is allowed only when current p2 rendering exceeds the
// unchanged persistence limit and the existing row exactly matches the
// admitted p1 renderer. It never exposes the compatibility marker to runtime
// publication or editing APIs.
func preparePost(ctx context.Context, candidate *post) error {
	if ctx == nil {
		return fmt.Errorf("stale post context is required")
	}
	if candidate == nil {
		return fmt.Errorf("stale post is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("prepare stale post canceled: %w", err)
	}
	rendered, renderErr := contentrender.RenderMarkdown(candidate.markdown)
	if renderErr == nil {
		var err error
		candidate.nextHTML, candidate.nextVersion, err = rendered.PersistenceValues()
		if err != nil {
			return fmt.Errorf("read rendered stale post: %w", err)
		}
		return nil
	}
	if !errors.Is(renderErr, contentrender.ErrRenderedHTMLTooLarge) {
		return fmt.Errorf("render stale post: %w", renderErr)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("prepare stale post canceled: %w", err)
	}
	if candidate.originalVersion != contentrender.LegacyRendererVersion {
		return fmt.Errorf("preserve oversized stale post: renderer is not the admitted p1 version")
	}
	if err := contentrender.ValidateLegacyRenderedHTML(candidate.markdown, candidate.originalHTML); err != nil {
		return fmt.Errorf("preserve oversized stale post: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("prepare stale post canceled: %w", err)
	}
	candidate.nextHTML = candidate.originalHTML
	candidate.nextVersion = legacyPreservedRendererVersion
	return nil
}

func preflightPost(ctx context.Context, candidate *post, redacted, alpha3Applied bool) error {
	if candidate == nil {
		return fmt.Errorf("preflight post is required")
	}
	if candidate.originalVersion == contentrender.RendererVersion {
		return nil
	}
	if redacted {
		if candidate.originalVersion == "moderation-redaction-v1" {
			return nil
		}
		return fmt.Errorf("redacted post has an unadmitted renderer version")
	}
	if candidate.originalVersion == legacyPreservedRendererVersion {
		if !alpha3Applied {
			return fmt.Errorf("p1-preserved renderer marker predates Alpha.3 migration state")
		}
		candidate.originalVersion = contentrender.LegacyRendererVersion
		if err := preparePost(ctx, candidate); err != nil {
			return err
		}
		if candidate.nextVersion != legacyPreservedRendererVersion {
			return fmt.Errorf("p1-preserved renderer row no longer requires compatibility")
		}
		return nil
	}
	return preparePost(ctx, candidate)
}

// isRetryable recognizes PostgreSQL's serialization and deadlock aborts,
// whose failed transactions are known not to have committed.
//
// Complexity: error-chain traversal depth d takes O(d) time, Omega(1), with no
// established tight Theta because the matching error may appear at any depth;
// auxiliary space is tight Theta(1).
func isRetryable(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01")
}
