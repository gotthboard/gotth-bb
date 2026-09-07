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

const selectInitialStalePostsSQL = `SELECT id, markdown_source, rendered_html, renderer_version
FROM public.posts
WHERE redacted_at IS NULL
  AND renderer_version <> $1
  AND renderer_version <> $2
ORDER BY id
LIMIT $3
FOR UPDATE`

const selectStalePostsAfterCursorSQL = `SELECT id, markdown_source, rendered_html, renderer_version
FROM public.posts
WHERE redacted_at IS NULL
  AND renderer_version <> $1
  AND renderer_version <> $2
  AND id > $3
ORDER BY id
LIMIT $4
FOR UPDATE`

const selectInitialPreflightPostsSQL = `SELECT post.id,
post.markdown_source,
post.rendered_html,
post.renderer_version,
COALESCE((pg_catalog.to_jsonb(post)->>'redacted_at') IS NOT NULL, false)
FROM public.posts AS post
ORDER BY post.id
LIMIT $1`

const selectPreflightPostsAfterCursorSQL = `SELECT post.id,
post.markdown_source,
post.rendered_html,
post.renderer_version,
COALESCE((pg_catalog.to_jsonb(post)->>'redacted_at') IS NOT NULL, false)
FROM public.posts AS post
WHERE post.id > $1
ORDER BY post.id
LIMIT $2`

const lockRendererStateSQL = `SELECT target_version, last_processed_post_id
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
    last_processed_post_id = $2,
    completed_at = NULL
WHERE singleton
  AND target_version = $3
  AND last_processed_post_id IS NOT DISTINCT FROM $4`

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

type preflightBatchResult struct {
	afterID *int64
	count   int
}

type preflightState struct {
	postsExist    bool
	alpha3Applied bool
}

// Run processes finite stale rows in bounded transactions until one committed
// empty batch proves completion. Serialization and deadlock failures before a
// commit attempt are retried at most three times; commit-unknown outcomes are
// returned for operator inspection and safe command rerun.
//
// Complexity: for q total posts, p stale posts containing n source and h
// rendered bytes, batch size b <= 100, and retry count r <= 3 per batch, the
// persisted primary-key cursor makes time O(r*(q+n+h)) and Omega(p+n+h), with
// no tight Theta bound because database I/O and retry occurrence vary.
// Auxiliary space is O(b*65,536+b*262,144), Omega(1), bounded by the source,
// persisted-output, and batch limits. Durable renderer state is the sole
// progress record; the loop performs no synchronous output I/O.
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
// writer constraint. Each read-only transaction retains at most batchSize
// rows; the required release stop/drain makes nullable-ID keyset pagination
// complete across those separate snapshots and through the later schema apply.
//
// Complexity: for p posts containing n source and h rendered bytes and batch
// size b <= 100, time is O(p+n+h), Omega(p), with one state transaction plus
// ceil(p/b)+1 row-batch transactions and no tighter Theta bound because
// database and renderer costs vary. Auxiliary space is
// O(b*65,536+b*262,144), Omega(1); total population work, transaction count,
// and I/O are deliberately not called batch-bounded.
func Preflight(ctx context.Context, database preflightDatabase, batchSize int) error {
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
	state, err := inspectPreflightState(ctx, database)
	if err != nil {
		return err
	}
	if !state.postsExist {
		return nil
	}
	var afterID *int64
	for {
		result, err := preflightBatch(ctx, database, batchSize, afterID, state.alpha3Applied)
		if err != nil {
			return err
		}
		if result.count < batchSize {
			return nil
		}
		afterID = result.afterID
	}
}

func inspectPreflightState(ctx context.Context, database preflightDatabase) (state preflightState, resultErr error) {
	tx, err := database.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return preflightState{}, fmt.Errorf("begin renderer preflight state inspection: %w", err)
	}
	if tx == nil {
		return preflightState{}, fmt.Errorf("begin renderer preflight state inspection returned no transaction")
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackContext); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback renderer preflight state inspection: %w", rollbackErr))
		}
	}()
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.posts') IS NOT NULL`).Scan(&state.postsExist); err != nil {
		return preflightState{}, fmt.Errorf("inspect renderer preflight schema: %w", err)
	}
	var historyExists bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.gotth_schema_migrations') IS NOT NULL`).Scan(&historyExists); err != nil {
		return preflightState{}, fmt.Errorf("inspect renderer preflight ledger: %w", err)
	}
	if historyExists {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.gotth_schema_migrations WHERE version = 7 AND name = '000007_gfm_renderer.sql')`).Scan(&state.alpha3Applied); err != nil {
			return preflightState{}, fmt.Errorf("inspect renderer preflight Alpha.3 ledger state: %w", err)
		}
	}
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := tx.Rollback(rollbackContext); err != nil {
		return preflightState{}, fmt.Errorf("close renderer preflight state inspection: %w", err)
	}
	closed = true
	return state, nil
}

func preflightBatch(ctx context.Context, database preflightDatabase, batchSize int, afterID *int64, alpha3Applied bool) (result preflightBatchResult, resultErr error) {
	tx, err := database.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return preflightBatchResult{}, fmt.Errorf("begin renderer preflight batch: %w", err)
	}
	if tx == nil {
		return preflightBatchResult{}, fmt.Errorf("begin renderer preflight batch returned no transaction")
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackContext); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback renderer preflight batch: %w", rollbackErr))
		}
	}()
	var rows pgx.Rows
	if afterID == nil {
		rows, err = tx.Query(ctx, selectInitialPreflightPostsSQL, int32(batchSize))
	} else {
		rows, err = tx.Query(ctx, selectPreflightPostsAfterCursorSQL, *afterID, int32(batchSize))
	}
	if err != nil {
		return preflightBatchResult{}, fmt.Errorf("select renderer preflight rows: %w", err)
	}
	for rows.Next() {
		var candidate post
		var redacted bool
		if err := rows.Scan(&candidate.id, &candidate.markdown, &candidate.originalHTML, &candidate.originalVersion, &redacted); err != nil {
			rows.Close()
			return preflightBatchResult{}, fmt.Errorf("scan renderer preflight row: %w", err)
		}
		if err := preflightPost(ctx, &candidate, redacted, alpha3Applied); err != nil {
			rows.Close()
			return preflightBatchResult{}, fmt.Errorf("classify renderer preflight post %d: %w", candidate.id, err)
		}
		lastID := candidate.id
		result.afterID = &lastID
		result.count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return preflightBatchResult{}, fmt.Errorf("iterate renderer preflight rows: %w", err)
	}
	rows.Close()
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := tx.Rollback(rollbackContext); err != nil {
		return preflightBatchResult{}, fmt.Errorf("close renderer preflight batch: %w", err)
	}
	closed = true
	return result, nil
}

// runBatch locks and converts at most batchSize rows strictly after the
// persisted post-ID cursor, advancing that cursor atomically with their
// updates, or validates the writer constraint and whole-table completion
// oracle when no later stale row remains.
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
	var lastProcessedPostID *int64
	if err := tx.QueryRow(ctx, lockRendererStateSQL).Scan(&targetVersion, &lastProcessedPostID); err != nil {
		return BatchResult{}, isRetryable(err), fmt.Errorf("lock renderer migration state: %w", err)
	}
	if targetVersion != contentrender.RendererVersion {
		return BatchResult{}, false, fmt.Errorf("renderer migration target does not match this release")
	}
	var rows pgx.Rows
	if lastProcessedPostID == nil {
		rows, err = tx.Query(ctx, selectInitialStalePostsSQL, contentrender.RendererVersion, legacyPreservedRendererVersion, int32(batchSize))
	} else {
		rows, err = tx.Query(ctx, selectStalePostsAfterCursorSQL, contentrender.RendererVersion, legacyPreservedRendererVersion, *lastProcessedPostID, int32(batchSize))
	}
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
	nextCursor := posts[len(posts)-1].id
	tag, err := tx.Exec(ctx, recordBatchSQL, int64(len(posts)), nextCursor, contentrender.RendererVersion, lastProcessedPostID)
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
	if redacted {
		if candidate.originalVersion == "moderation-redaction-v1" && candidate.markdown == "[Content removed by moderation]" && candidate.originalHTML == "<p>Content removed by moderation.</p>" {
			return nil
		}
		return fmt.Errorf("redacted post does not match the admitted moderation rendering")
	}
	if candidate.originalVersion == contentrender.RendererVersion {
		if !alpha3Applied {
			return fmt.Errorf("current renderer marker predates Alpha.3 migration state")
		}
		rendered, err := contentrender.RenderMarkdown(candidate.markdown)
		if err != nil {
			return fmt.Errorf("verify current renderer post: %w", err)
		}
		html, version, err := rendered.PersistenceValues()
		if err != nil {
			return fmt.Errorf("read current renderer post: %w", err)
		}
		if version != candidate.originalVersion || html != candidate.originalHTML {
			return fmt.Errorf("current renderer output does not match canonical Markdown")
		}
		return nil
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
