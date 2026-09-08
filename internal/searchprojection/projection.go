// Package searchprojection rebuilds the PostgreSQL search projection after an
// immutable projection-version change.
package searchprojection

import (
	"context"
	"errors"
	"fmt"
	"time"

	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"
)

const (
	MaximumBatchSize = 100
	maximumAttempts  = 3
	rollbackTimeout  = 5 * time.Second
)

const selectInitialTopicsSQL = `SELECT topic.id, topic.title
FROM public.topics AS topic
ORDER BY topic.id
LIMIT $1
FOR UPDATE`

const selectTopicsAfterCursorSQL = `SELECT topic.id, topic.title
FROM public.topics AS topic
WHERE topic.id > $1
ORDER BY topic.id
LIMIT $2
FOR UPDATE`

const selectInitialPostsSQL = `SELECT post.id, post.rendered_html, post.redacted_at IS NOT NULL
FROM public.posts AS post
ORDER BY post.id
LIMIT $1
FOR UPDATE`

const selectPostsAfterCursorSQL = `SELECT post.id, post.rendered_html, post.redacted_at IS NOT NULL
FROM public.posts AS post
WHERE post.id > $1
ORDER BY post.id
LIMIT $2
FOR UPDATE`

const selectPreflightPostsSQL = `SELECT post.id, post.rendered_html, post.created_at,
       pg_catalog.to_jsonb(post)->>'search_vector',
       pg_catalog.to_jsonb(post)->>'search_projection_version',
       COALESCE((pg_catalog.to_jsonb(post)->>'redacted_at') IS NOT NULL, false)
FROM public.posts AS post`

const vectorizeSQL = `SELECT candidate.id,
       pg_catalog.to_tsvector('pg_catalog.simple'::pg_catalog.regconfig, candidate.input)::text
FROM ROWS FROM (
    pg_catalog.unnest($1::bigint[]),
    pg_catalog.unnest($2::text[])
) WITH ORDINALITY AS candidate(id, input, ordinal)
ORDER BY candidate.ordinal`

const lockStateSQL = `SELECT target_version, phase, last_processed_id,
       topics_converted_count, posts_converted_count, completed_at
FROM public.search_projection_state
WHERE singleton
FOR UPDATE`

type database interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type preflightDatabase interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type readinessDatabase interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type candidate struct {
	id            int64
	input         string
	storedVector  *string
	storedVersion *string
	createdAt     pgtype.Timestamptz
}

type state struct {
	target          string
	phase           string
	cursor          *int64
	topicsConverted int64
	postsConverted  int64
	completedAt     pgtype.Timestamptz
}

type batchResult struct {
	complete    bool
	needAnalyze bool
}

// Preflight proves on the candidate PostgreSQL 17 server that every topic and
// post has a positive identity, finite creation time, derivable input, and—if
// already populated—an exact current vector. Every read-only snapshot retains
// at most batchSize rows; the documented stop/drain makes keyset traversal
// complete across snapshots.
//
// Complexity: for q rows and n retained source bytes, time is O(q+n), Omega(q)
// plus PostgreSQL vector work, with ceil(q/b) bounded transactions. Auxiliary
// space is O(b+n), Omega(1), for b <= 100; no mutation, retry, or output I/O
// occurs.
func Preflight(ctx context.Context, database preflightDatabase, batchSize int) error {
	if ctx == nil {
		return fmt.Errorf("search projection preflight context is required")
	}
	if database == nil {
		return fmt.Errorf("search projection preflight database is required")
	}
	if batchSize < 1 || batchSize > MaximumBatchSize {
		return fmt.Errorf("search projection preflight batch size must be between 1 and %d", MaximumBatchSize)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("search projection preflight canceled: %w", err)
	}
	present, requireCurrent, err := inspectServer(ctx, database)
	if err != nil || !present {
		return err
	}
	if err := preflightKind(ctx, database, batchSize, "topic", requireCurrent); err != nil {
		return err
	}
	return preflightKind(ctx, database, batchSize, "post", requireCurrent)
}

// Run resumes topic then post population solely from the locked singleton,
// analyzes both populated tables after an empty post selection, and admits
// completion only after the whole-table oracle and six checks succeed.
//
// Complexity: for q rows and n visible-text bytes, time is O(r*(q+n)),
// Omega(q+n), for at most three deadlock/serialization attempts per batch;
// completion adds population-linear validation and ANALYZE work. Auxiliary
// space is O(b+n), Omega(1), for b <= 100. There is no detached work or output
// I/O.
func Run(ctx context.Context, database database, batchSize int) error {
	if ctx == nil {
		return fmt.Errorf("search projection migration context is required")
	}
	if database == nil {
		return fmt.Errorf("search projection migration database is required")
	}
	if batchSize < 1 || batchSize > MaximumBatchSize {
		return fmt.Errorf("search projection migration batch size must be between 1 and %d", MaximumBatchSize)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("search projection migration canceled: %w", err)
	}
	for {
		var result batchResult
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
		if result.complete {
			return Ready(ctx, database)
		}
		if result.needAnalyze {
			if _, err := database.Exec(ctx, `ANALYZE public.topics`); err != nil {
				return fmt.Errorf("analyze search projection topics: %w", err)
			}
			if _, err := database.Exec(ctx, `ANALYZE public.posts`); err != nil {
				return fmt.Errorf("analyze search projection posts: %w", err)
			}
			if err := complete(ctx, database); err != nil {
				return err
			}
			return Ready(ctx, database)
		}
	}
}

// Ready attests the exact PostgreSQL major, completed singleton, projection
// columns, nine checks, state primary key, five indexes, six narrowed triggers,
// and unchanged validation function used by every serving process.
//
// Complexity: local time/space are tight Theta(1); PostgreSQL performs two
// constant-shape catalog statements and no population scan.
func Ready(ctx context.Context, database readinessDatabase) error {
	if ctx == nil {
		return fmt.Errorf("search projection readiness context is required")
	}
	if database == nil {
		return fmt.Errorf("search projection readiness database is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("search projection readiness canceled: %w", err)
	}
	var valid bool
	if err := database.QueryRow(ctx, readyStateSQL, contentrender.SearchProjectionVersion).Scan(&valid); err != nil {
		return fmt.Errorf("query search projection readiness state: %w", err)
	}
	if !valid {
		return fmt.Errorf("search projection readiness state is invalid")
	}
	if err := database.QueryRow(ctx, schemaAttestationSQL, topicPostStateFunctionDefinition).Scan(&valid); err != nil {
		return fmt.Errorf("query search projection schema attestation: %w", err)
	}
	if !valid {
		return fmt.Errorf("search projection schema attestation failed")
	}
	return nil
}

// inspectServer checks schema presence and the bound PostgreSQL major without
// mutating a fresh or existing database.
//
// Complexity: tight Theta(1) local time/space plus one read-only transaction
// and two constant-shape statements.
func inspectServer(ctx context.Context, database preflightDatabase) (present bool, requireCurrent bool, resultErr error) {
	tx, err := database.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, false, fmt.Errorf("begin search projection server inspection: %w", err)
	}
	if tx == nil {
		return false, false, fmt.Errorf("begin search projection server inspection returned no transaction")
	}
	defer rollback(ctx, tx, "search projection server inspection", &resultErr)
	var major int
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer / 10000`).Scan(&major); err != nil {
		return false, false, fmt.Errorf("inspect search projection PostgreSQL version: %w", err)
	}
	if major != 17 {
		return false, false, fmt.Errorf("search projection requires PostgreSQL major 17")
	}
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.topics') IS NOT NULL AND pg_catalog.to_regclass('public.posts') IS NOT NULL`).Scan(&present); err != nil {
		return false, false, fmt.Errorf("inspect search projection schema: %w", err)
	}
	if !present {
		return false, false, nil
	}
	var statePresent bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.search_projection_state') IS NOT NULL`).Scan(&statePresent); err != nil {
		return false, false, fmt.Errorf("inspect search projection state schema: %w", err)
	}
	if !statePresent {
		return true, false, nil
	}
	var stateRows, completedRows int
	if err := tx.QueryRow(ctx, `SELECT count(*)::integer,
    count(*) FILTER (WHERE phase = 'complete')::integer
FROM public.search_projection_state`).Scan(&stateRows, &completedRows); err != nil {
		return false, false, fmt.Errorf("inspect search projection state: %w", err)
	}
	if stateRows != 1 || completedRows > 1 {
		return false, false, fmt.Errorf("search projection state cardinality is invalid")
	}
	return true, completedRows == 1, nil
}

// preflightKind walks one table in bounded read-only keyset transactions and
// compares any existing projection tuple with a candidate-server vector.
//
// Complexity: for q rows and n source bytes, time is O(q+n), Omega(q), and
// space is O(b+n), Omega(1), for b <= 100.
func preflightKind(ctx context.Context, database preflightDatabase, batchSize int, kind string, requireCurrent bool) error {
	var afterID *int64
	for {
		count, cursor, err := preflightBatch(ctx, database, batchSize, kind, afterID, requireCurrent)
		if err != nil {
			return err
		}
		if count < batchSize {
			return nil
		}
		afterID = cursor
	}
}

// preflightBatch owns one bounded snapshot and reports only row kind, ID, and
// fixed failure class for content-derived validation failures.
//
// Complexity: for b rows and n bytes, time/space are O(b+n), Omega(1), bounded
// by b <= 100 and persisted field limits.
func preflightBatch(ctx context.Context, database preflightDatabase, batchSize int, kind string, afterID *int64, requireCurrent bool) (count int, cursor *int64, resultErr error) {
	tx, err := database.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, nil, fmt.Errorf("begin search projection %s preflight: %w", kind, err)
	}
	if tx == nil {
		return 0, nil, fmt.Errorf("begin search projection %s preflight returned no transaction", kind)
	}
	defer rollback(ctx, tx, "search projection "+kind+" preflight", &resultErr)
	candidates, err := loadPreflightCandidates(ctx, tx, batchSize, kind, afterID)
	if err != nil {
		return 0, nil, err
	}
	vectors, err := vectorize(ctx, tx, candidates)
	if err != nil {
		return 0, nil, fmt.Errorf("construct search projection %s vectors: %w", kind, err)
	}
	for index := range candidates {
		candidate := candidates[index]
		if candidate.id <= 0 || !finiteTimestamp(candidate.createdAt) {
			return 0, nil, fmt.Errorf("search projection %s %d: invalid identity or time", kind, candidate.id)
		}
		if (candidate.storedVector == nil) != (candidate.storedVersion == nil) {
			return 0, nil, fmt.Errorf("search projection %s %d: partial projection tuple", kind, candidate.id)
		}
		if requireCurrent && candidate.storedVersion == nil {
			return 0, nil, fmt.Errorf("search projection %s %d: missing projection after completion", kind, candidate.id)
		}
		if candidate.storedVersion != nil && (*candidate.storedVersion != contentrender.SearchProjectionVersion || *candidate.storedVector != vectors[index]) {
			return 0, nil, fmt.Errorf("search projection %s %d: stale or mismatched projection", kind, candidate.id)
		}
	}
	if len(candidates) > 0 {
		last := candidates[len(candidates)-1].id
		cursor = &last
	}
	return len(candidates), cursor, nil
}

// loadPreflightCandidates reads a schema-compatible row shape before and after
// migration 000008 by extracting optional projection fields through to_jsonb.
//
// Complexity: for b rows and n source bytes, returned space is O(b+n),
// Omega(1); database work is one ordered primary-key query.
func loadPreflightCandidates(ctx context.Context, tx pgx.Tx, batchSize int, kind string, afterID *int64) ([]candidate, error) {
	var query string
	if kind == "topic" {
		query = `SELECT topic.id, topic.title, topic.created_at,
       pg_catalog.to_jsonb(topic)->>'search_vector',
       pg_catalog.to_jsonb(topic)->>'search_projection_version', false
FROM public.topics AS topic`
	} else {
		query = selectPreflightPostsSQL
	}
	arguments := []any{int32(batchSize)}
	if afterID == nil {
		query += ` ORDER BY id LIMIT $1`
	} else {
		query += ` WHERE id > $1 ORDER BY id LIMIT $2`
		arguments = []any{*afterID, int32(batchSize)}
	}
	rows, err := tx.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("select search projection %s preflight rows: %w", kind, err)
	}
	defer rows.Close()
	result := make([]candidate, 0, batchSize)
	for rows.Next() {
		var value candidate
		var raw string
		var redacted bool
		if err := rows.Scan(&value.id, &raw, &value.createdAt, &value.storedVector, &value.storedVersion, &redacted); err != nil {
			return nil, fmt.Errorf("scan search projection %s preflight row: %w", kind, err)
		}
		if kind == "topic" {
			value.input = norm.NFC.String(raw)
		} else if !redacted {
			value.input = contentrender.SanitizeHTML(raw).VisibleText()
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search projection %s preflight rows: %w", kind, err)
	}
	return result, nil
}

// vectorize asks the candidate PostgreSQL server to construct exact vectors
// for one bounded ordered input batch.
//
// Complexity: for b inputs containing n bytes, time/returned space are O(b+n),
// Omega(1), plus PostgreSQL parser work; it performs one statement.
func vectorize(ctx context.Context, tx pgx.Tx, candidates []candidate) ([]string, error) {
	if len(candidates) == 0 {
		return []string{}, nil
	}
	ids := make([]int64, len(candidates))
	inputs := make([]string, len(candidates))
	for index := range candidates {
		ids[index], inputs[index] = candidates[index].id, candidates[index].input
	}
	rows, err := tx.Query(ctx, vectorizeSQL, ids, inputs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vectors := make([]string, 0, len(candidates))
	for rows.Next() {
		var id int64
		var vector string
		if err := rows.Scan(&id, &vector); err != nil {
			return nil, err
		}
		if len(vectors) >= len(candidates) || id != candidates[len(vectors)].id {
			return nil, fmt.Errorf("candidate vector order is invalid")
		}
		vectors = append(vectors, vector)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(vectors) != len(candidates) {
		return nil, fmt.Errorf("candidate vector count is invalid")
	}
	return vectors, nil
}

// runBatch locks the singleton and one bounded row set, then atomically writes
// vectors and durable phase/cursor/count progress.
//
// Complexity: for b rows and n source bytes, time/space are O(b+n), Omega(1),
// bounded by b <= 100; one transaction commits or advances nothing.
func runBatch(ctx context.Context, database database, batchSize int) (result batchResult, retryable bool, resultErr error) {
	tx, err := database.Begin(ctx)
	if err != nil {
		return batchResult{}, isRetryable(err), fmt.Errorf("begin search projection batch: %w", err)
	}
	if tx == nil {
		return batchResult{}, false, fmt.Errorf("begin search projection batch returned no transaction")
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, tx, "search projection batch", &resultErr)
		}
	}()
	current, err := lockState(ctx, tx)
	if err != nil {
		return batchResult{}, isRetryable(err), err
	}
	if current.target != contentrender.SearchProjectionVersion {
		return batchResult{}, false, fmt.Errorf("search projection target does not match this release")
	}
	if current.phase == "complete" {
		return commitResult(ctx, tx, batchResult{complete: true}, &committed)
	}
	candidates, err := loadMutationCandidates(ctx, tx, batchSize, current)
	if err != nil {
		return batchResult{}, isRetryable(err), err
	}
	if len(candidates) == 0 {
		if current.phase == "topics" {
			tag, err := tx.Exec(ctx, `UPDATE public.search_projection_state SET phase = 'posts', last_processed_id = NULL WHERE singleton AND phase = 'topics'`)
			if err != nil || tag.RowsAffected() != 1 {
				return batchResult{}, isRetryable(err), fmt.Errorf("advance search projection to posts: %w", err)
			}
			return commitResult(ctx, tx, batchResult{}, &committed)
		}
		return commitResult(ctx, tx, batchResult{needAnalyze: true}, &committed)
	}
	vectors, err := vectorize(ctx, tx, candidates)
	if err != nil {
		return batchResult{}, isRetryable(err), fmt.Errorf("construct search projection batch vectors: %w", err)
	}
	for index := range candidates {
		table := "public.topics"
		if current.phase == "posts" {
			table = "public.posts"
		}
		tag, err := tx.Exec(ctx, `UPDATE `+table+` SET search_vector = $1::tsvector, search_projection_version = $2 WHERE id = $3`, vectors[index], contentrender.SearchProjectionVersion, candidates[index].id)
		if err != nil || tag.RowsAffected() != 1 {
			return batchResult{}, isRetryable(err), fmt.Errorf("write search projection %s %d: %w", current.phase, candidates[index].id, err)
		}
	}
	lastID := candidates[len(candidates)-1].id
	countColumn := "topics_converted_count"
	if current.phase == "posts" {
		countColumn = "posts_converted_count"
	}
	tag, err := tx.Exec(ctx, `UPDATE public.search_projection_state SET `+countColumn+` = `+countColumn+` + $1, last_processed_id = $2 WHERE singleton AND phase = $3 AND last_processed_id IS NOT DISTINCT FROM $4`, int64(len(candidates)), lastID, current.phase, current.cursor)
	if err != nil || tag.RowsAffected() != 1 {
		return batchResult{}, isRetryable(err), fmt.Errorf("record search projection %s batch: %w", current.phase, err)
	}
	return commitResult(ctx, tx, batchResult{}, &committed)
}

// loadMutationCandidates selects and locks at most one batch after the durable
// cursor, deriving exact NFC topic or visible post input in memory.
//
// Complexity: for b rows and n bytes, returned space/time are O(b+n), Omega(1),
// with one primary-key statement and b <= 100.
func loadMutationCandidates(ctx context.Context, tx pgx.Tx, batchSize int, current state) ([]candidate, error) {
	var rows pgx.Rows
	var err error
	if current.phase == "topics" {
		if current.cursor == nil {
			rows, err = tx.Query(ctx, selectInitialTopicsSQL, int32(batchSize))
		} else {
			rows, err = tx.Query(ctx, selectTopicsAfterCursorSQL, *current.cursor, int32(batchSize))
		}
	} else {
		if current.cursor == nil {
			rows, err = tx.Query(ctx, selectInitialPostsSQL, int32(batchSize))
		} else {
			rows, err = tx.Query(ctx, selectPostsAfterCursorSQL, *current.cursor, int32(batchSize))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("select search projection %s batch: %w", current.phase, err)
	}
	defer rows.Close()
	result := make([]candidate, 0, batchSize)
	for rows.Next() {
		var value candidate
		var raw string
		var redacted bool
		if current.phase == "topics" {
			if err := rows.Scan(&value.id, &raw); err != nil {
				return nil, fmt.Errorf("scan search projection topic: %w", err)
			}
			value.input = norm.NFC.String(raw)
		} else {
			if err := rows.Scan(&value.id, &raw, &redacted); err != nil {
				return nil, fmt.Errorf("scan search projection post: %w", err)
			}
			if !redacted {
				value.input = contentrender.SanitizeHTML(raw).VisibleText()
			}
		}
		if value.id <= 0 {
			return nil, fmt.Errorf("search projection %s %d: invalid identity", current.phase, value.id)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search projection %s batch: %w", current.phase, err)
	}
	return result, nil
}

// complete validates the six table checks and reconciles every projection row,
// durable count, and retained final-post cursor before preserving one database
// completion timestamp.
//
// Complexity: PostgreSQL performs population-linear validation/oracle scans;
// local time and auxiliary space are tight Theta(1).
func complete(ctx context.Context, database database) (resultErr error) {
	tx, err := database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin search projection completion: %w", err)
	}
	if tx == nil {
		return fmt.Errorf("begin search projection completion returned no transaction")
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, tx, "search projection completion", &resultErr)
		}
	}()
	current, err := lockState(ctx, tx)
	if err != nil {
		return err
	}
	if current.target != contentrender.SearchProjectionVersion {
		return fmt.Errorf("search projection completion target does not match this release")
	}
	if current.phase == "complete" {
		_, _, err := commitResult(ctx, tx, batchResult{complete: true}, &committed)
		return err
	}
	if current.phase != "posts" {
		return fmt.Errorf("search projection completion phase is invalid")
	}
	for _, statement := range []string{
		`ALTER TABLE public.topics VALIDATE CONSTRAINT topics_search_projection_current`,
		`ALTER TABLE public.posts VALIDATE CONSTRAINT posts_search_projection_current`,
		`ALTER TABLE public.topics VALIDATE CONSTRAINT topics_search_created_finite`,
		`ALTER TABLE public.posts VALIDATE CONSTRAINT posts_search_created_finite`,
		`ALTER TABLE public.topics VALIDATE CONSTRAINT topics_search_id_positive`,
		`ALTER TABLE public.posts VALIDATE CONSTRAINT posts_search_id_positive`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("validate search projection constraint: %w", err)
		}
	}
	if err := attestSchema(ctx, tx); err != nil {
		return err
	}
	var valid bool
	if err := tx.QueryRow(ctx, completionOracleSQL, contentrender.SearchProjectionVersion, current.topicsConverted, current.postsConverted, current.cursor).Scan(&valid); err != nil {
		return fmt.Errorf("query search projection completion oracle: %w", err)
	}
	if !valid {
		return fmt.Errorf("search projection completion oracle is not satisfied")
	}
	tag, err := tx.Exec(ctx, `UPDATE public.search_projection_state SET phase = 'complete', completed_at = COALESCE(completed_at, clock_timestamp()) WHERE singleton AND phase = 'posts'`)
	if err != nil || tag.RowsAffected() != 1 {
		return fmt.Errorf("record search projection completion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit search projection completion (outcome unknown; inspect state before retry): %w", err)
	}
	committed = true
	return nil
}

const completionOracleSQL = `SELECT
    (SELECT count(*) FROM public.topics) = $2
    AND (SELECT count(*) FROM public.posts) = $3
    AND (SELECT max(id) FROM public.posts) IS NOT DISTINCT FROM $4
    AND NOT EXISTS (
        SELECT 1 FROM public.topics
        WHERE search_vector IS NULL OR search_projection_version IS DISTINCT FROM $1
    )
    AND NOT EXISTS (
        SELECT 1 FROM public.posts
        WHERE search_vector IS NULL OR search_projection_version IS DISTINCT FROM $1
    )
    AND (SELECT count(*) FROM pg_catalog.pg_constraint
         WHERE conname IN ('topics_search_projection_current', 'posts_search_projection_current',
             'topics_search_created_finite', 'posts_search_created_finite',
             'topics_search_id_positive', 'posts_search_id_positive')
           AND convalidated) = 6`

const readyStateSQL = `SELECT
    current_setting('server_version_num')::integer / 10000 = 17
    AND (SELECT count(*) FROM public.search_projection_state) = 1
    AND EXISTS (
        SELECT 1 FROM public.search_projection_state
        WHERE singleton
          AND target_version = $1
          AND phase = 'complete'
          AND completed_at IS NOT NULL
          AND pg_catalog.isfinite(completed_at)
    )`

const topicPostStateFunctionDefinition = `CREATE OR REPLACE FUNCTION public.gotth_validate_topic_post_state()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public'
AS $function$
DECLARE
    target_topic_id bigint;
    topic_first_post_id bigint;
    topic_latest_post_id bigint;
    topic_reply_count integer;
    topic_next_post_number integer;
    first_post_topic_id bigint;
    first_post_number integer;
    latest_post_topic_id bigint;
    latest_post_number integer;
    actual_post_count bigint;
    actual_max_post_number integer;
BEGIN
    IF TG_TABLE_NAME = 'topics' THEN
        target_topic_id := COALESCE(NEW.id, OLD.id);
    ELSE
        target_topic_id := COALESCE(NEW.topic_id, OLD.topic_id);
    END IF;

    SELECT first_post_id, latest_post_id, reply_count, next_post_number
    INTO topic_first_post_id, topic_latest_post_id, topic_reply_count, topic_next_post_number
    FROM public.topics
    WHERE id = target_topic_id;

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    SELECT count(*), max(post_number)
    INTO actual_post_count, actual_max_post_number
    FROM public.posts
    WHERE topic_id = target_topic_id;

    SELECT topic_id, post_number
    INTO first_post_topic_id, first_post_number
    FROM public.posts
    WHERE id = topic_first_post_id;

    IF NOT FOUND OR first_post_topic_id <> target_topic_id OR first_post_number <> 1 THEN
        RAISE EXCEPTION 'topic first post is inconsistent' USING ERRCODE = 'check_violation';
    END IF;

    SELECT topic_id, post_number
    INTO latest_post_topic_id, latest_post_number
    FROM public.posts
    WHERE id = topic_latest_post_id;

    IF NOT FOUND
       OR latest_post_topic_id <> target_topic_id
       OR latest_post_number <> actual_max_post_number THEN
        RAISE EXCEPTION 'topic latest post is inconsistent' USING ERRCODE = 'check_violation';
    END IF;

    IF actual_post_count < 1
       OR topic_reply_count <> actual_post_count - 1
       OR topic_next_post_number <> actual_max_post_number + 1 THEN
        RAISE EXCEPTION 'topic post counters are inconsistent' USING ERRCODE = 'check_violation';
    END IF;

    RETURN NULL;
END;
$function$
`

const schemaAttestationSQL = `WITH expected_constraints(name, relation_name, definition) AS (
    VALUES
      ('topics_search_projection_current', 'public.topics', 'CHECK ((((search_vector IS NULL) AND (search_projection_version IS NULL)) OR ((search_vector IS NOT NULL) AND (search_projection_version = ''search-v1-pg17-simple-u15-p2''::text))))'),
      ('posts_search_projection_current', 'public.posts', 'CHECK ((((search_vector IS NULL) AND (search_projection_version IS NULL)) OR ((search_vector IS NOT NULL) AND (search_projection_version = ''search-v1-pg17-simple-u15-p2''::text))))'),
      ('topics_search_created_finite', 'public.topics', 'CHECK (isfinite(created_at))'),
      ('posts_search_created_finite', 'public.posts', 'CHECK (isfinite(created_at))'),
      ('topics_search_id_positive', 'public.topics', 'CHECK ((id > 0))'),
      ('posts_search_id_positive', 'public.posts', 'CHECK ((id > 0))'),
      ('search_projection_state_target_current', 'public.search_projection_state', 'CHECK ((target_version = ''search-v1-pg17-simple-u15-p2''::text))'),
      ('search_projection_state_shape', 'public.search_projection_state', 'CHECK ((singleton AND (phase = ANY (ARRAY[''topics''::text, ''posts''::text, ''complete''::text])) AND ((last_processed_id IS NULL) OR (last_processed_id > 0)) AND (topics_converted_count >= 0) AND (posts_converted_count >= 0) AND (((phase = ''topics''::text) AND (posts_converted_count = 0) AND (completed_at IS NULL) AND ((topics_converted_count = 0) = (last_processed_id IS NULL))) OR ((phase = ''posts''::text) AND (completed_at IS NULL) AND ((posts_converted_count = 0) = (last_processed_id IS NULL))) OR ((phase = ''complete''::text) AND (completed_at IS NOT NULL) AND ((posts_converted_count = 0) = (last_processed_id IS NULL))))))'),
      ('search_projection_state_completion_finite', 'public.search_projection_state', 'CHECK (((completed_at IS NULL) OR isfinite(completed_at)))')
), expected_columns(relation_name, name, type_name, not_null, default_expression, identity_kind, generated_kind) AS (
    VALUES
      ('public.topics', 'search_vector', 'tsvector', false, '', '', ''),
      ('public.topics', 'search_projection_version', 'text', false, '', '', ''),
      ('public.posts', 'search_vector', 'tsvector', false, '', '', ''),
      ('public.posts', 'search_projection_version', 'text', false, '', '', ''),
      ('public.search_projection_state', 'singleton', 'boolean', true, 'true', '', ''),
      ('public.search_projection_state', 'target_version', 'text', true, '', '', ''),
      ('public.search_projection_state', 'phase', 'text', true, '''topics''::text', '', ''),
      ('public.search_projection_state', 'last_processed_id', 'bigint', false, '', '', ''),
      ('public.search_projection_state', 'topics_converted_count', 'bigint', true, '0', '', ''),
      ('public.search_projection_state', 'posts_converted_count', 'bigint', true, '0', '', ''),
      ('public.search_projection_state', 'completed_at', 'timestamp with time zone', false, '', '', '')
), expected_indexes(name, definition) AS (
    VALUES
      ('topics_search_vector_current_idx', 'CREATE INDEX topics_search_vector_current_idx ON public.topics USING gin (search_vector) WHERE ((deleted_at IS NULL) AND (search_projection_version = ''search-v1-pg17-simple-u15-p2''::text))'),
      ('posts_search_vector_current_idx', 'CREATE INDEX posts_search_vector_current_idx ON public.posts USING gin (search_vector) WHERE ((deleted_at IS NULL) AND (redacted_at IS NULL) AND (search_projection_version = ''search-v1-pg17-simple-u15-p2''::text))'),
      ('topics_search_author_current_idx', 'CREATE INDEX topics_search_author_current_idx ON public.topics USING btree (author_id, created_at DESC, id DESC) WHERE ((deleted_at IS NULL) AND (search_projection_version = ''search-v1-pg17-simple-u15-p2''::text))'),
      ('posts_search_author_current_idx', 'CREATE INDEX posts_search_author_current_idx ON public.posts USING btree (author_id, created_at DESC, id DESC) WHERE ((deleted_at IS NULL) AND (redacted_at IS NULL) AND (search_projection_version = ''search-v1-pg17-simple-u15-p2''::text))'),
      ('posts_activity_current_idx', 'CREATE INDEX posts_activity_current_idx ON public.posts USING btree (created_at DESC, id DESC) WHERE ((deleted_at IS NULL) AND (redacted_at IS NULL) AND (search_projection_version = ''search-v1-pg17-simple-u15-p2''::text))')
), expected_triggers(name, relation_name, definition) AS (
    VALUES
      ('topics_validate_post_state_insert', 'public.topics', 'CREATE CONSTRAINT TRIGGER topics_validate_post_state_insert AFTER INSERT ON public.topics DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gotth_validate_topic_post_state()'),
      ('topics_validate_post_state_update', 'public.topics', 'CREATE CONSTRAINT TRIGGER topics_validate_post_state_update AFTER UPDATE OF id, first_post_id, latest_post_id, reply_count, next_post_number ON public.topics DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (((old.id IS DISTINCT FROM new.id) OR (old.first_post_id IS DISTINCT FROM new.first_post_id) OR (old.latest_post_id IS DISTINCT FROM new.latest_post_id) OR (old.reply_count IS DISTINCT FROM new.reply_count) OR (old.next_post_number IS DISTINCT FROM new.next_post_number))) EXECUTE FUNCTION gotth_validate_topic_post_state()'),
      ('topics_validate_post_state_delete', 'public.topics', 'CREATE CONSTRAINT TRIGGER topics_validate_post_state_delete AFTER DELETE ON public.topics DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gotth_validate_topic_post_state()'),
      ('posts_validate_topic_state_insert', 'public.posts', 'CREATE CONSTRAINT TRIGGER posts_validate_topic_state_insert AFTER INSERT ON public.posts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gotth_validate_topic_post_state()'),
      ('posts_validate_topic_state_update', 'public.posts', 'CREATE CONSTRAINT TRIGGER posts_validate_topic_state_update AFTER UPDATE OF id, topic_id, post_number ON public.posts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (((old.id IS DISTINCT FROM new.id) OR (old.topic_id IS DISTINCT FROM new.topic_id) OR (old.post_number IS DISTINCT FROM new.post_number))) EXECUTE FUNCTION gotth_validate_topic_post_state()'),
      ('posts_validate_topic_state_delete', 'public.posts', 'CREATE CONSTRAINT TRIGGER posts_validate_topic_state_delete AFTER DELETE ON public.posts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION gotth_validate_topic_post_state()')
)
SELECT
    (SELECT count(*) FROM expected_constraints AS expected
     JOIN pg_catalog.pg_constraint AS actual
       ON actual.conname = expected.name
      AND actual.conrelid = expected.relation_name::regclass
      AND actual.contype = 'c'
      AND actual.convalidated
      AND pg_catalog.pg_get_constraintdef(actual.oid, false) = expected.definition) = 9
    AND (SELECT count(*) FROM expected_columns AS expected
         JOIN pg_catalog.pg_attribute AS actual
           ON actual.attrelid = expected.relation_name::regclass
          AND actual.attname = expected.name
          AND actual.attnum > 0
          AND NOT actual.attisdropped
         LEFT JOIN pg_catalog.pg_attrdef AS default_value
           ON default_value.adrelid = actual.attrelid
          AND default_value.adnum = actual.attnum
         WHERE pg_catalog.format_type(actual.atttypid, actual.atttypmod) = expected.type_name
           AND actual.attnotnull = expected.not_null
           AND COALESCE(pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid), '') = expected.default_expression
           AND actual.attidentity::text = expected.identity_kind
           AND actual.attgenerated::text = expected.generated_kind) = 11
    AND (SELECT count(*) FROM pg_catalog.pg_attribute
         WHERE attrelid = 'public.search_projection_state'::regclass
           AND attnum > 0 AND NOT attisdropped) = 7
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint
        WHERE conname = 'search_projection_state_pkey'
          AND conrelid = 'public.search_projection_state'::regclass
          AND contype = 'p'
          AND convalidated
          AND pg_catalog.pg_get_constraintdef(oid, false) = 'PRIMARY KEY (singleton)'
    )
    AND (SELECT count(*) FROM expected_indexes AS expected
         JOIN pg_catalog.pg_class AS actual ON actual.relname = expected.name
         JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = actual.relnamespace AND namespace.nspname = 'public'
         JOIN pg_catalog.pg_index AS state ON state.indexrelid = actual.oid
         WHERE state.indisvalid AND state.indisready
           AND pg_catalog.pg_get_indexdef(actual.oid) = expected.definition) = 5
    AND (SELECT count(*) FROM expected_triggers AS expected
         JOIN pg_catalog.pg_trigger AS actual
           ON actual.tgname = expected.name
          AND actual.tgrelid = expected.relation_name::regclass
          AND NOT actual.tgisinternal
          AND actual.tgenabled = 'O'
          AND pg_catalog.pg_get_triggerdef(actual.oid, false) = expected.definition) = 6
    AND NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger
        WHERE tgname IN ('topics_validate_post_state', 'posts_validate_topic_state')
          AND NOT tgisinternal
    )
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_proc AS function
        JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = function.pronamespace
        JOIN pg_catalog.pg_language AS language ON language.oid = function.prolang
        WHERE namespace.nspname = 'public'
          AND function.proname = 'gotth_validate_topic_post_state'
          AND function.pronargs = 0
          AND language.lanname = 'plpgsql'
          AND function.proconfig = ARRAY['search_path=pg_catalog, public']::text[]
          AND pg_catalog.pg_get_functiondef(function.oid) = $1
    )`

// attestSchema compares every search projection catalog object with its exact
// admitted PostgreSQL 17 definition.
//
// Complexity: tight Theta(1) local time/space plus one constant-size catalog
// statement; no heap rows are read.
func attestSchema(ctx context.Context, database readinessDatabase) error {
	var valid bool
	if err := database.QueryRow(ctx, schemaAttestationSQL, topicPostStateFunctionDefinition).Scan(&valid); err != nil {
		return fmt.Errorf("query search projection schema attestation: %w", err)
	}
	if !valid {
		return fmt.Errorf("search projection schema attestation failed")
	}
	return nil
}

// lockState loads the one database-owned durable progress row under a write
// lock and rejects malformed state before any population work.
//
// Complexity: tight Theta(1) local time/space plus one indexed statement.
func lockState(ctx context.Context, tx pgx.Tx) (state, error) {
	var current state
	if err := tx.QueryRow(ctx, lockStateSQL).Scan(&current.target, &current.phase, &current.cursor, &current.topicsConverted, &current.postsConverted, &current.completedAt); err != nil {
		return state{}, fmt.Errorf("lock search projection state: %w", err)
	}
	return current, nil
}

// commitResult commits one known transaction outcome and marks the caller's
// rollback guard only after success.
//
// Complexity: tight Theta(1) local time/space plus one commit round trip.
func commitResult(ctx context.Context, tx pgx.Tx, result batchResult, committed *bool) (batchResult, bool, error) {
	if err := tx.Commit(ctx); err != nil {
		return batchResult{}, false, fmt.Errorf("commit search projection batch (outcome unknown; inspect state before retry): %w", err)
	}
	*committed = true
	return result, false, nil
}

// rollback closes an uncommitted transaction under a bounded cleanup context
// and joins cleanup failure without hiding the original cause.
//
// Complexity: tight Theta(1) local time/space plus one rollback round trip.
func rollback(ctx context.Context, tx pgx.Tx, operation string, resultErr *error) {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := tx.Rollback(rollbackContext); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("rollback %s: %w", operation, err))
	}
}

// finiteTimestamp recognizes only finite PostgreSQL timestamps.
//
// Complexity: time and auxiliary space are tight Theta(1).
func finiteTimestamp(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite
}

// isRetryable recognizes only deadlock and serialization aborts known not to
// have committed.
//
// Complexity: error-chain traversal is O(d), Omega(1), for depth d; auxiliary
// space is tight Theta(1).
func isRetryable(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01")
}
