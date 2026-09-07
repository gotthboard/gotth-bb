// Package rerender rebuilds persisted derived post HTML after an immutable
// renderer-version change.
package rerender

import (
	"context"
	"errors"
	"fmt"
	"io"
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
// by the source, persisted-output, and batch limits. Output is one
// constant-size line per batch.
func Run(ctx context.Context, database database, output io.Writer, batchSize int) error {
	if ctx == nil {
		return fmt.Errorf("renderer migration context is required")
	}
	if database == nil {
		return fmt.Errorf("renderer migration database is required")
	}
	if output == nil {
		return fmt.Errorf("renderer migration output is required")
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
		if _, err := io.WriteString(output, progressLine(result)); err != nil {
			return fmt.Errorf("write renderer migration progress: %w", err)
		}
		if result.Complete {
			return nil
		}
	}
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

// progressLine formats one fixed-field, content-free operator record.
//
// Complexity: for decimal digit count d, time and returned space are O(d),
// Omega(1), and tight Theta(d); d is bounded by the batch limit.
func progressLine(result BatchResult) string {
	return fmt.Sprintf("renderer_migration target=%s converted=%d complete=%t\n", contentrender.RendererVersion, result.Converted, result.Complete)
}
