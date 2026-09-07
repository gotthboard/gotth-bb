//go:build integration

package rerender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
)

const (
	rerenderTestDatabase                   = "gotth_bb_alpha3_rerender_test"
	rerenderPerformanceTestDatabase        = "gotth_bb_alpha3_rerender_performance_test"
	rerenderPopulationTestDatabase         = "gotth_bb_alpha3_rerender_population_test"
	rerenderPopulationPerformancePostCount = 25_000
	rendererCursorConstraintDefinition     = "CHECK ((((converted_count = 0) AND (last_processed_post_id IS NULL)) OR ((converted_count > 0) AND (last_processed_post_id IS NOT NULL))))"
)

type countedPreflightDatabase struct {
	connection   *pgx.Conn
	transactions int
	roundTrips   int
}

func (database *countedPreflightDatabase) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	database.transactions++
	tx, err := database.connection.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &countedPreflightTx{Tx: tx, roundTrips: &database.roundTrips}, nil
}

type countedPreflightTx struct {
	pgx.Tx
	roundTrips *int
}

type mutationSelection struct {
	cursor *int64
	ids    []int64
}

type explainDocument struct {
	Plan explainNode `json:"Plan"`
}

type explainNode struct {
	NodeType            string        `json:"Node Type"`
	RelationName        string        `json:"Relation Name"`
	IndexName           string        `json:"Index Name"`
	IndexCond           string        `json:"Index Cond"`
	ActualRows          float64       `json:"Actual Rows"`
	RowsRemovedByFilter float64       `json:"Rows Removed by Filter"`
	Plans               []explainNode `json:"Plans"`
}

type selectionPlanEvidence struct {
	queries      int
	returnedRows int
	examinedRows int
}

type countedMutationDatabase struct {
	connection *pgx.Conn
	selections []*mutationSelection
}

func (database *countedMutationDatabase) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.connection.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &countedMutationTx{Tx: tx, database: database}, nil
}

type countedMutationTx struct {
	pgx.Tx
	database *countedMutationDatabase
}

func (tx *countedMutationTx) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, sql, arguments...)
	if err != nil || (sql != selectInitialStalePostsSQL && sql != selectStalePostsAfterCursorSQL) {
		return rows, err
	}
	selection := &mutationSelection{}
	if sql == selectStalePostsAfterCursorSQL {
		value, ok := arguments[2].(int64)
		if !ok {
			rows.Close()
			return nil, fmt.Errorf("counted renderer cursor is %T, want int64", arguments[2])
		}
		selection.cursor = &value
	}
	tx.database.selections = append(tx.database.selections, selection)
	return &countedMutationRows{Rows: rows, selection: selection}, nil
}

type countedMutationRows struct {
	pgx.Rows
	selection *mutationSelection
}

func (rows *countedMutationRows) Scan(destinations ...any) error {
	if err := rows.Rows.Scan(destinations...); err != nil {
		return err
	}
	id, ok := destinations[0].(*int64)
	if !ok {
		return fmt.Errorf("counted renderer selection first destination is %T, want *int64", destinations[0])
	}
	rows.selection.ids = append(rows.selection.ids, *id)
	return nil
}

type commitUnknownDatabase struct {
	connection *pgx.Conn
	used       bool
}

func (database *commitUnknownDatabase) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.connection.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if database.used {
		return tx, nil
	}
	database.used = true
	return &commitUnknownTx{Tx: tx}, nil
}

type commitUnknownTx struct {
	pgx.Tx
}

func (tx *commitUnknownTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("simulated lost commit acknowledgement")
}

func measureMutationSelectionPlans(ctx context.Context, connection *pgx.Conn, rowCount, batchSize int) (selectionPlanEvidence, error) {
	const cursorPlanName = "alpha3_population_mutation_cursor_explain"
	if _, err := connection.Exec(ctx, "PREPARE "+cursorPlanName+"(text, text, bigint, integer) AS "+selectStalePostsAfterCursorSQL); err != nil {
		return selectionPlanEvidence{}, fmt.Errorf("prepare generic renderer mutation cursor plan: %w", err)
	}
	var evidence selectionPlanEvidence
	var cursor *int64
	for {
		var encoded string
		var row pgx.Row
		if cursor == nil {
			row = connection.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+selectInitialStalePostsSQL, contentrender.RendererVersion, legacyPreservedRendererVersion, int32(batchSize))
		} else {
			row = connection.QueryRow(ctx, fmt.Sprintf(
				"EXPLAIN (ANALYZE, FORMAT JSON) EXECUTE %s(%s, %s, %d, %d)",
				cursorPlanName,
				postgresStringLiteral(contentrender.RendererVersion),
				postgresStringLiteral(legacyPreservedRendererVersion),
				*cursor,
				batchSize,
			))
		}
		if err := row.Scan(&encoded); err != nil {
			return selectionPlanEvidence{}, fmt.Errorf("explain renderer mutation selection after %v: %w", cursor, err)
		}
		var documents []explainDocument
		if err := json.Unmarshal([]byte(encoded), &documents); err != nil || len(documents) != 1 {
			return selectionPlanEvidence{}, fmt.Errorf("decode renderer mutation selection plan after %v: documents=%d: %w", cursor, len(documents), err)
		}
		evidence.queries++
		returned := int(documents[0].Plan.ActualRows)
		evidence.returnedRows += returned
		scans, examined := inspectPostIndexScan(documents[0].Plan)
		if scans != 1 {
			return selectionPlanEvidence{}, fmt.Errorf("renderer mutation selection after %v used %d exact posts_pkey scans, want 1", cursor, scans)
		}
		evidence.examinedRows += examined
		if cursor != nil && !postIndexConditionContains(documents[0].Plan, "id>$3") {
			return selectionPlanEvidence{}, fmt.Errorf("renderer mutation selection after %v did not retain id > $3 as a generic-plan index condition; observed %q", cursor, postIndexConditions(documents[0].Plan))
		}
		if returned == 0 {
			return evidence, nil
		}
		nextCursor := int64(evidence.returnedRows)
		if nextCursor > int64(rowCount) {
			return selectionPlanEvidence{}, fmt.Errorf("renderer mutation plan returned %d rows from %d-row fixture", nextCursor, rowCount)
		}
		cursor = &nextCursor
	}
}

func measurePreflightSelectionPlans(ctx context.Context, connection *pgx.Conn, rowCount, batchSize int) (selectionPlanEvidence, error) {
	const cursorPlanName = "alpha3_population_preflight_cursor_explain"
	if _, err := connection.Exec(ctx, "PREPARE "+cursorPlanName+"(bigint, integer) AS "+selectPreflightPostsAfterCursorSQL); err != nil {
		return selectionPlanEvidence{}, fmt.Errorf("prepare generic renderer preflight cursor plan: %w", err)
	}
	var evidence selectionPlanEvidence
	var cursor *int64
	for {
		var encoded string
		var row pgx.Row
		if cursor == nil {
			row = connection.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+selectInitialPreflightPostsSQL, int32(batchSize))
		} else {
			row = connection.QueryRow(ctx, fmt.Sprintf(
				"EXPLAIN (ANALYZE, FORMAT JSON) EXECUTE %s(%d, %d)",
				cursorPlanName,
				*cursor,
				batchSize,
			))
		}
		if err := row.Scan(&encoded); err != nil {
			return selectionPlanEvidence{}, fmt.Errorf("explain renderer preflight selection after %v: %w", cursor, err)
		}
		var documents []explainDocument
		if err := json.Unmarshal([]byte(encoded), &documents); err != nil || len(documents) != 1 {
			return selectionPlanEvidence{}, fmt.Errorf("decode renderer preflight selection plan after %v: documents=%d: %w", cursor, len(documents), err)
		}
		evidence.queries++
		returned := int(documents[0].Plan.ActualRows)
		evidence.returnedRows += returned
		scans, examined := inspectPostIndexScan(documents[0].Plan)
		if scans != 1 {
			return selectionPlanEvidence{}, fmt.Errorf("renderer preflight selection after %v used %d exact posts_pkey scans, want 1", cursor, scans)
		}
		evidence.examinedRows += examined
		if cursor != nil && !postIndexConditionContains(documents[0].Plan, "id>$1") {
			return selectionPlanEvidence{}, fmt.Errorf("renderer preflight selection after %v did not retain id > $1 as a generic-plan index condition; observed %q", cursor, postIndexConditions(documents[0].Plan))
		}
		if returned == 0 {
			return evidence, nil
		}
		nextCursor := int64(evidence.returnedRows)
		if nextCursor > int64(rowCount) {
			return selectionPlanEvidence{}, fmt.Errorf("renderer preflight plan returned %d rows from %d-row fixture", nextCursor, rowCount)
		}
		cursor = &nextCursor
	}
}

func inspectPostIndexScan(node explainNode) (scans, examined int) {
	if node.NodeType == "Index Scan" && node.RelationName == "posts" && node.IndexName == "posts_pkey" {
		scans++
		examined += int(node.ActualRows + node.RowsRemovedByFilter)
	}
	for _, child := range node.Plans {
		childScans, childExamined := inspectPostIndexScan(child)
		scans += childScans
		examined += childExamined
	}
	return scans, examined
}

func postIndexConditionContains(node explainNode, compactFragment string) bool {
	if node.NodeType == "Index Scan" && node.RelationName == "posts" && node.IndexName == "posts_pkey" && strings.Contains(strings.ReplaceAll(node.IndexCond, " ", ""), compactFragment) {
		return true
	}
	for _, child := range node.Plans {
		if postIndexConditionContains(child, compactFragment) {
			return true
		}
	}
	return false
}

func postIndexConditions(node explainNode) []string {
	var conditions []string
	if node.NodeType == "Index Scan" && node.RelationName == "posts" && node.IndexName == "posts_pkey" {
		conditions = append(conditions, node.IndexCond)
	}
	for _, child := range node.Plans {
		conditions = append(conditions, postIndexConditions(child)...)
	}
	return conditions
}

func postgresStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (tx *countedPreflightTx) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	*tx.roundTrips++
	return tx.Tx.Query(ctx, sql, arguments...)
}

func (tx *countedPreflightTx) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	*tx.roundTrips++
	return tx.Tx.QueryRow(ctx, sql, arguments...)
}

func TestRendererMigrationOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+rerenderTestDatabase+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+rerenderTestDatabase); err != nil {
		t.Fatalf("create renderer test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+rerenderTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = rerenderTestDatabase
	if err := migration.Apply(ctx, testConfig, preAlpha3MigrationFS(t)); err != nil {
		t.Fatalf("apply pre-alpha.3 schema: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer test database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	runnerConnection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer runner: %v", err)
	}
	t.Cleanup(func() { _ = runnerConnection.Close(context.Background()) })
	contentionConnection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer contention probe: %v", err)
	}
	t.Cleanup(func() { _ = contentionConnection.Close(context.Background()) })
	var userID, areaID, topicID, rootID, replyID, currentID, denseTaskID, denseTableID int64
	const minimumPostID = int64(-1 << 63)
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Renderer owner', 'administrator') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert renderer owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('renderer', 'Renderer', $1, $1) RETURNING id`, userID).Scan(&areaID); err != nil {
		t.Fatalf("insert renderer area: %v", err)
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin renderer fixture: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.topics', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id')),
nextval(pg_get_serial_sequence('public.posts', 'id'))`).Scan(&topicID, &rootID, &replyID, &currentID, &denseTaskID, &denseTableID); err != nil {
		t.Fatalf("allocate renderer fixture identifiers: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics (id, area_id, author_id, title, first_post_id, latest_post_id, reply_count, next_post_number) VALUES ($1, $2, $3, 'Renderer topic', $4, 0, 6, 8)`, topicID, areaID, userID, rootID); err != nil {
		t.Fatalf("insert renderer topic: %v", err)
	}
	denseTasks := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	tableHeader := "|a|b|c|d|\n|-|-|-|-|\n"
	tableRow := "|x|x|x|x|\n"
	denseTable := tableHeader + strings.Repeat(tableRow, (contentrender.MaximumMarkdownBytes-len(tableHeader))/len(tableRow))
	rootLegacyHTML := exactLegacyHTML(t, "~~root~~")
	replyLegacyHTML := exactLegacyHTML(t, "- [x] reply")
	denseTaskLegacyHTML := exactLegacyHTML(t, denseTasks)
	denseTableLegacyHTML := exactLegacyHTML(t, denseTable)
	if _, err := tx.Exec(ctx, `INSERT INTO public.posts (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id, thread_path) VALUES
		($1, $2, $3, 1, '~~root~~', $4, $5, NULL, ARRAY[1]),
		($6, $2, $3, 2, '- [x] reply', $7, $5, $1, ARRAY[1,2]),
		($8, $2, $3, 3, 'current', '<p>current</p>', $9, $1, ARRAY[1,3]),
		($10, $2, $3, 4, $11, $12, $5, $1, ARRAY[1,4]),
		($13, $2, $3, 5, $14, $15, $5, $1, ARRAY[1,5]),
		($16, $2, $3, 6, 'minimum id', '<p>minimum id</p>', $5, $1, ARRAY[1,6]),
		(0, $2, $3, 7, 'zero id', '<p>zero id</p>', $5, $1, ARRAY[1,7])`,
		rootID, topicID, userID, rootLegacyHTML, contentrender.LegacyRendererVersion,
		replyID, replyLegacyHTML, currentID, contentrender.RendererVersion,
		denseTaskID, denseTasks, denseTaskLegacyHTML, denseTableID, denseTable, denseTableLegacyHTML, minimumPostID); err != nil {
		t.Fatalf("insert renderer posts: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit renderer fixture: %v", err)
	}
	if err := migration.Apply(ctx, testConfig, alpha3MigrationFS(t)); err != nil {
		t.Fatalf("apply alpha.3 schema: %v", err)
	}
	var cursorTypeExact, cursorNullable, cursorPlain, cursorNoDefault bool
	var cursorConstraintDefinition string
	if err := connection.QueryRow(ctx, `SELECT
cursor_column.atttypid = 'pg_catalog.int8'::regtype AND cursor_column.atttypmod = -1,
NOT cursor_column.attnotnull,
cursor_column.attgenerated = '' AND cursor_column.attidentity = '',
NOT cursor_column.atthasdef,
pg_catalog.pg_get_constraintdef(cursor_constraint.oid, false)
FROM pg_catalog.pg_attribute AS cursor_column
JOIN pg_catalog.pg_constraint AS cursor_constraint
  ON cursor_constraint.conrelid = cursor_column.attrelid
 AND cursor_constraint.conname = 'content_renderer_state_cursor_progress'
WHERE cursor_column.attrelid = 'public.content_renderer_state'::regclass
  AND cursor_column.attname = 'last_processed_post_id'
  AND NOT cursor_column.attisdropped`).Scan(&cursorTypeExact, &cursorNullable, &cursorPlain, &cursorNoDefault, &cursorConstraintDefinition); err != nil {
		t.Fatalf("inspect renderer cursor schema: %v", err)
	}
	if !cursorTypeExact || !cursorNullable || !cursorPlain || !cursorNoDefault || cursorConstraintDefinition != rendererCursorConstraintDefinition {
		t.Fatalf("renderer cursor schema = type %t/nullable %t/plain %t/no-default %t/constraint %q", cursorTypeExact, cursorNullable, cursorPlain, cursorNoDefault, cursorConstraintDefinition)
	}

	var rendererValidated, sizeValidated bool
	var sizeDefinition string
	wantSizeDefinition := fmt.Sprintf("CHECK ((octet_length(rendered_html) <= %d))", contentrender.MaximumRenderedHTMLBytes)
	if err := connection.QueryRow(ctx, `SELECT
(SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current'),
(SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_rendered_size'),
(SELECT pg_catalog.pg_get_constraintdef(oid, false) FROM pg_catalog.pg_constraint WHERE conname = 'posts_rendered_size')`).Scan(&rendererValidated, &sizeValidated, &sizeDefinition); err != nil || rendererValidated || !sizeValidated || sizeDefinition != wantSizeDefinition {
		t.Fatalf("new constraints = (renderer %t, size %t, definition %q, %v), want false/true/exact/nil", rendererValidated, sizeValidated, sizeDefinition, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET rendered_html = $1 WHERE id = $2`, strings.Repeat("x", contentrender.MaximumRenderedHTMLBytes+1), currentID); err == nil {
		t.Fatal("NOT VALID rendered-size constraint accepted an oversized changed row")
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET rendered_html = rendered_html WHERE id = $1`, rootID); err == nil {
		t.Fatal("NOT VALID constraint accepted an old-version update")
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET rendered_html = rendered_html WHERE topic_id = $1 AND post_number = 3`, topicID); err != nil {
		t.Fatalf("NOT VALID constraint rejected a current-version update: %v", err)
	}
	if _, err := connection.Exec(ctx, `WITH effective AS (SELECT clock_timestamp() AS at_time)
UPDATE public.posts
SET markdown_source = '[Content removed by moderation]',
    rendered_html = '<p>Content removed by moderation.</p>',
    renderer_version = 'moderation-redaction-v1',
    deleted_at = effective.at_time,
    deleted_by = $2,
    deletion_reason = 'redacted',
    redacted_at = effective.at_time,
    redacted_by = $2,
    redaction_reason = 'redacted'
FROM effective
WHERE topic_id = $1 AND post_number = 3`, topicID, userID); err != nil {
		t.Fatalf("NOT VALID constraint rejected exact moderation redaction: %v", err)
	}
	statementTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin writer constraint probe: %v", err)
	}
	if _, err := statementTx.Exec(ctx, `INSERT INTO public.posts (topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id) VALUES ($1, $2, 4, 'old', '<p>old</p>', 'legacy-p1', $3)`, topicID, userID, rootID); err == nil {
		t.Fatal("NOT VALID constraint accepted an old-version insert")
	}
	_ = statementTx.Rollback(ctx)

	editTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent edit probe: %v", err)
	}
	if _, err := editTx.Exec(ctx, `UPDATE public.posts SET markdown_source = '~~edited~~', rendered_html = '<p><del>edited</del></p>', renderer_version = $2 WHERE id = $1`, minimumPostID, contentrender.RendererVersion); err != nil {
		t.Fatalf("lock stale row with concurrent edit: %v", err)
	}
	blockedEditContext, blockedEditCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err := Run(blockedEditContext, contentionConnection, 1); !errors.Is(err, context.DeadlineExceeded) {
		blockedEditCancel()
		_ = editTx.Rollback(ctx)
		t.Fatalf("edit-versus-rerender error = %v, want deadline", err)
	}
	blockedEditCancel()
	var rolledBackCursor *int64
	if err := connection.QueryRow(ctx, `SELECT last_processed_post_id FROM public.content_renderer_state WHERE singleton`).Scan(&rolledBackCursor); err != nil || rolledBackCursor != nil {
		t.Fatalf("blocked batch cursor = (%v, %v), want NULL/nil", rolledBackCursor, err)
	}
	if err := editTx.Rollback(ctx); err != nil {
		t.Fatalf("release concurrent edit row: %v", err)
	}
	if !contentionConnection.IsClosed() {
		contentionConnection.Close(context.Background())
	}

	result, _, err := runBatch(ctx, runnerConnection, 1)
	if err != nil || result.Converted != 1 || result.Complete {
		t.Fatalf("first batch = (%+v, %v), want one/incomplete", result, err)
	}
	var firstCursor *int64
	var firstConverted int64
	if err := connection.QueryRow(ctx, `SELECT last_processed_post_id, converted_count FROM public.content_renderer_state WHERE singleton`).Scan(&firstCursor, &firstConverted); err != nil || firstCursor == nil || *firstCursor != minimumPostID || firstConverted != 1 {
		t.Fatalf("first committed cursor = (%v, %d, %v), want MinInt64/1/nil", firstCursor, firstConverted, err)
	}
	unknownDatabase := &commitUnknownDatabase{connection: runnerConnection}
	if _, _, err := runBatch(ctx, unknownDatabase, 1); err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("lost commit acknowledgement = %v, want outcome-unknown error", err)
	}
	var unknownCursor *int64
	var unknownConverted int64
	if err := connection.QueryRow(ctx, `SELECT last_processed_post_id, converted_count FROM public.content_renderer_state WHERE singleton`).Scan(&unknownCursor, &unknownConverted); err != nil || unknownCursor == nil || *unknownCursor != 0 || unknownConverted != 2 {
		t.Fatalf("commit-unknown cursor = (%v, %d, %v), want 0/2/nil", unknownCursor, unknownConverted, err)
	}
	countedDatabase := &countedMutationDatabase{connection: runnerConnection}
	if err := Run(ctx, countedDatabase, 1); err != nil {
		t.Fatalf("restart Run() returned error: %v", err)
	}
	selectedIDs := make(map[int64]struct{})
	for index, selection := range countedDatabase.selections {
		for _, id := range selection.ids {
			if selection.cursor != nil && id <= *selection.cursor {
				t.Fatalf("selection %d revisited prefix: cursor %d, id %d", index, *selection.cursor, id)
			}
			if _, duplicate := selectedIDs[id]; duplicate {
				t.Fatalf("selection %d repeated post id %d", index, id)
			}
			selectedIDs[id] = struct{}{}
		}
	}
	if len(selectedIDs) != 4 || len(countedDatabase.selections) != 5 || len(countedDatabase.selections[len(countedDatabase.selections)-1].ids) != 0 {
		t.Fatalf("restart selections = %d unique rows/%d queries/final %d rows, want 4/5/0", len(selectedIDs), len(countedDatabase.selections), len(countedDatabase.selections[len(countedDatabase.selections)-1].ids))
	}
	var staleCount int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.posts WHERE redacted_at IS NULL AND renderer_version <> $1`, contentrender.RendererVersion).Scan(&staleCount); err != nil || staleCount != 2 {
		t.Fatalf("post-output-failure stale rows = (%d, %v), want two/nil", staleCount, err)
	}
	var completedAt time.Time
	if err := connection.QueryRow(ctx, `SELECT completed_at FROM public.content_renderer_state WHERE singleton`).Scan(&completedAt); err != nil {
		t.Fatalf("read renderer completion time: %v", err)
	}
	if err := Run(ctx, runnerConnection, 1); err != nil {
		t.Fatalf("idempotent Run() returned error: %v", err)
	}
	var repeatedCompletedAt time.Time
	if err := connection.QueryRow(ctx, `SELECT completed_at FROM public.content_renderer_state WHERE singleton`).Scan(&repeatedCompletedAt); err != nil || !repeatedCompletedAt.Equal(completedAt) {
		t.Fatalf("idempotent completion time = (%s, %v), want %s/nil", repeatedCompletedAt, err, completedAt)
	}
	if err := connection.QueryRow(ctx, `SELECT
(SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current'),
(SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_rendered_size')`).Scan(&rendererValidated, &sizeValidated); err != nil || !rendererValidated || !sizeValidated {
		t.Fatalf("completed constraints = (renderer %t, size %t, %v), want true/true/nil", rendererValidated, sizeValidated, err)
	}
	var rootHTML string
	if err := connection.QueryRow(ctx, `SELECT rendered_html FROM public.posts WHERE id = $1`, rootID).Scan(&rootHTML); err != nil || rootHTML != "<p><del>root</del></p>\n" {
		t.Fatalf("rerendered root = (%q, %v)", rootHTML, err)
	}
	var denseTaskHTML, denseTaskVersion, denseTableHTML, denseTableVersion string
	if err := connection.QueryRow(ctx, `SELECT
	(SELECT rendered_html FROM public.posts WHERE id = $1),
	(SELECT renderer_version FROM public.posts WHERE id = $1),
	(SELECT rendered_html FROM public.posts WHERE id = $2),
	(SELECT renderer_version FROM public.posts WHERE id = $2)`, denseTaskID, denseTableID).Scan(&denseTaskHTML, &denseTaskVersion, &denseTableHTML, &denseTableVersion); err != nil {
		t.Fatalf("inspect dense legacy migration output: %v", err)
	}
	if denseTaskHTML != denseTaskLegacyHTML || denseTableHTML != denseTableLegacyHTML ||
		denseTaskVersion != legacyPreservedRendererVersion || denseTableVersion != legacyPreservedRendererVersion {
		t.Fatalf("dense compatibility rows = (task %d bytes/%q, table %d bytes/%q), want exact preserved p1 HTML and marker", len(denseTaskHTML), denseTaskVersion, len(denseTableHTML), denseTableVersion)
	}

	lockTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin singleton lock probe: %v", err)
	}
	if _, err := lockTx.Exec(ctx, `SELECT target_version FROM public.content_renderer_state WHERE singleton FOR UPDATE`); err != nil {
		t.Fatalf("lock renderer singleton: %v", err)
	}
	singletonConnection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("connect singleton contention probe: %v", err)
	}
	blockedContext, blockedCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer blockedCancel()
	if err := Run(blockedContext, singletonConnection, 1); !errors.Is(err, context.DeadlineExceeded) {
		_ = singletonConnection.Close(context.Background())
		t.Fatalf("concurrent runner error = %v, want deadline", err)
	}
	_ = singletonConnection.Close(context.Background())
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("release renderer singleton: %v", err)
	}

	if _, err := connection.Exec(ctx, `ALTER TABLE public.posts DROP CONSTRAINT posts_renderer_version_current`); err != nil {
		t.Fatalf("drop renderer constraint for validation failure probe: %v", err)
	}
	if _, err := connection.Exec(ctx, `ALTER TABLE public.posts ADD CONSTRAINT posts_renderer_version_current CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("install invalid renderer constraint probe: %v", err)
	}
	if err := Run(ctx, runnerConnection, MaximumBatchSize); err == nil || !strings.Contains(err.Error(), "validate current renderer constraint") {
		t.Fatalf("constraint validation failure = %v", err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM public.content_renderer_state WHERE singleton`); err != nil {
		t.Fatalf("delete renderer state probe: %v", err)
	}
	if err := Run(ctx, runnerConnection, MaximumBatchSize); err == nil || !strings.Contains(err.Error(), "lock renderer migration state") {
		t.Fatalf("missing renderer state failure = %v", err)
	}
}

func TestMaximumCompatibilityBatchPerformanceOnPostgreSQL17(t *testing.T) {
	if os.Getenv("GOTTH_BB_RUN_PERFORMANCE") != "1" {
		t.Skip("set GOTTH_BB_RUN_PERFORMANCE=1 to run the bounded compatibility-batch admission")
	}
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adminConfig, err := parsePerformanceDatabaseConfig(databaseURL, os.Getenv("GOTTH_BB_EXPECTED_DATABASE_HOST"), os.Getenv("GOTTH_BB_EXPECTED_DATABASE_PORT"))
	if err != nil {
		t.Fatalf("parse bounded performance database target: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+rerenderPerformanceTestDatabase+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+rerenderPerformanceTestDatabase); err != nil {
		t.Fatalf("create renderer performance database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+rerenderPerformanceTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = rerenderPerformanceTestDatabase
	if err := migration.Apply(ctx, testConfig, preAlpha3MigrationFS(t)); err != nil {
		t.Fatalf("apply pre-alpha.3 schema: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer performance database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	logPerformanceServerIdentity(t, ctx, connection, rerenderPerformanceTestDatabase)

	var userID, areaID, topicID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Renderer performance owner', 'administrator') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert renderer performance owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('renderer-performance', 'Renderer performance', $1, $1) RETURNING id`, userID).Scan(&areaID); err != nil {
		t.Fatalf("insert renderer performance area: %v", err)
	}
	if err := connection.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.topics', 'id'))`).Scan(&topicID); err != nil {
		t.Fatalf("allocate renderer performance topic: %v", err)
	}
	postIDs := make([]int64, MaximumBatchSize)
	rows, err := connection.Query(ctx, `SELECT nextval(pg_get_serial_sequence('public.posts', 'id')) FROM generate_series(1, $1)`, MaximumBatchSize)
	if err != nil {
		t.Fatalf("allocate renderer performance posts: %v", err)
	}
	for index := 0; rows.Next(); index++ {
		if index >= len(postIDs) {
			rows.Close()
			t.Fatal("allocated more renderer performance posts than requested")
		}
		if err := rows.Scan(&postIDs[index]); err != nil {
			rows.Close()
			t.Fatalf("scan renderer performance post %d: %v", index, err)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate renderer performance post identifiers: %v", err)
	}
	rows.Close()
	if postIDs[len(postIDs)-1] == 0 {
		t.Fatalf("allocated renderer performance posts = %v, want %d identifiers", postIDs, MaximumBatchSize)
	}
	denseSource := strings.Repeat("- [x]\n", contentrender.MaximumMarkdownBytes/len("- [x]\n"))
	denseLegacyHTML := exactLegacyHTML(t, denseSource)
	t.Logf("fixture source_bytes=%d source_sha256=%x legacy_html_bytes=%d legacy_html_sha256=%x", len(denseSource), sha256.Sum256([]byte(denseSource)), len(denseLegacyHTML), sha256.Sum256([]byte(denseLegacyHTML)))
	fixtureTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin renderer performance fixture: %v", err)
	}
	if _, err := fixtureTx.Exec(ctx, `INSERT INTO public.topics (id, area_id, author_id, title, first_post_id, latest_post_id, reply_count, next_post_number) VALUES ($1, $2, $3, 'Renderer performance topic', $4, $5, $6, $7)`, topicID, areaID, userID, postIDs[0], postIDs[len(postIDs)-1], MaximumBatchSize-1, MaximumBatchSize+1); err != nil {
		_ = fixtureTx.Rollback(ctx)
		t.Fatalf("insert renderer performance topic: %v", err)
	}
	for index, postID := range postIDs {
		var parentID any
		if index > 0 {
			parentID = postIDs[0]
		}
		if _, err := fixtureTx.Exec(ctx, `INSERT INTO public.posts (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, postID, topicID, userID, index+1, denseSource, denseLegacyHTML, contentrender.LegacyRendererVersion, parentID); err != nil {
			_ = fixtureTx.Rollback(ctx)
			t.Fatalf("insert renderer performance post %d: %v", index+1, err)
		}
	}
	if err := fixtureTx.Commit(ctx); err != nil {
		t.Fatalf("commit renderer performance fixture: %v", err)
	}
	if err := migration.Apply(ctx, testConfig, alpha3MigrationFS(t)); err != nil {
		t.Fatalf("apply alpha.3 schema: %v", err)
	}

	started := time.Now()
	result, retryable, err := runBatch(ctx, connection, MaximumBatchSize)
	elapsed := time.Since(started)
	if err != nil || retryable || result.Converted != MaximumBatchSize || result.Complete {
		t.Fatalf("maximum compatibility batch = (%+v, retryable %t, %v), want %d/incomplete/nil", result, retryable, err, MaximumBatchSize)
	}
	var preservedCount, exactHTMLCount int
	var convertedCount int64
	var completedAt *time.Time
	if err := connection.QueryRow(ctx, `SELECT
	(SELECT count(*) FROM public.posts WHERE renderer_version = $1),
	(SELECT count(*) FROM public.posts WHERE renderer_version = $1 AND rendered_html = $2),
	(SELECT converted_count FROM public.content_renderer_state WHERE singleton),
	(SELECT completed_at FROM public.content_renderer_state WHERE singleton)`, legacyPreservedRendererVersion, denseLegacyHTML).Scan(&preservedCount, &exactHTMLCount, &convertedCount, &completedAt); err != nil {
		t.Fatalf("inspect maximum compatibility batch: %v", err)
	}
	if preservedCount != MaximumBatchSize || exactHTMLCount != MaximumBatchSize || convertedCount != MaximumBatchSize || completedAt != nil {
		t.Fatalf("maximum compatibility output = (%d preserved, %d exact, %d converted, completed %v), want %d/%d/%d/nil", preservedCount, exactHTMLCount, convertedCount, completedAt, MaximumBatchSize, MaximumBatchSize, MaximumBatchSize)
	}
	completion, retryable, err := runBatch(ctx, connection, MaximumBatchSize)
	if err != nil || retryable || !completion.Complete || completion.Converted != 0 {
		t.Fatalf("maximum compatibility completion = (%+v, retryable %t, %v), want complete/nil", completion, retryable, err)
	}
	var rendererValidated bool
	if err := connection.QueryRow(ctx, `SELECT convalidated FROM pg_catalog.pg_constraint WHERE conname = 'posts_renderer_version_current'`).Scan(&rendererValidated); err != nil || !rendererValidated {
		t.Fatalf("maximum compatibility renderer constraint = (%t, %v), want true/nil", rendererValidated, err)
	}
	t.Logf("compatibility_batch rows=%d elapsed=%s preserved=%d exact_html=%d converted=%d", MaximumBatchSize, elapsed, preservedCount, exactHTMLCount, convertedCount)
}

func TestPopulationMigrationPerformanceOnPostgreSQL17(t *testing.T) {
	if os.Getenv("GOTTH_BB_RUN_POPULATION_PERFORMANCE") != "1" {
		t.Skip("set GOTTH_BB_RUN_POPULATION_PERFORMANCE=1 to run the population migration admission")
	}
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	adminConfig, err := parsePerformanceDatabaseConfig(databaseURL, os.Getenv("GOTTH_BB_EXPECTED_DATABASE_HOST"), os.Getenv("GOTTH_BB_EXPECTED_DATABASE_PORT"))
	if err != nil {
		t.Fatalf("parse bounded population database target: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+rerenderPopulationTestDatabase+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+rerenderPopulationTestDatabase); err != nil {
		t.Fatalf("create renderer population database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+rerenderPopulationTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = rerenderPopulationTestDatabase
	if err := migration.Apply(ctx, testConfig, preAlpha3MigrationFS(t)); err != nil {
		t.Fatalf("apply pre-alpha.3 schema: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect renderer population database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	logPerformanceServerIdentity(t, ctx, connection, rerenderPopulationTestDatabase)

	const populationSource = "Representative **population** post."
	populationLegacyHTML := exactLegacyHTML(t, populationSource)
	t.Logf("population_fixture rows=%d full_pages_25=%d source_bytes=%d source_sha256=%x legacy_html_bytes=%d legacy_html_sha256=%x", rerenderPopulationPerformancePostCount, rerenderPopulationPerformancePostCount/25, len(populationSource), sha256.Sum256([]byte(populationSource)), len(populationLegacyHTML), sha256.Sum256([]byte(populationLegacyHTML)))
	var userID, areaID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Population owner', 'administrator') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert population owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, created_by, updated_by) VALUES ('population', 'Population', $1, $1) RETURNING id`, userID).Scan(&areaID); err != nil {
		t.Fatalf("insert population area: %v", err)
	}
	fixtureTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin population fixture: %v", err)
	}
	// Fixture construction is outside every measured phase. Suppress per-row
	// application triggers for this bulk load, then verify the exact coherent
	// topic/thread shape before timing any production mechanism.
	if _, err := fixtureTx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = fixtureTx.Rollback(ctx)
		t.Fatalf("isolate population fixture trigger cost: %v", err)
	}
	if _, err := fixtureTx.Exec(ctx, `INSERT INTO public.topics (id, area_id, author_id, title, first_post_id, latest_post_id, reply_count, next_post_number)
SELECT generated.topic_id,
       $1,
       $2,
       'Population topic ' || generated.topic_id,
       ((generated.topic_id - 1) * 25) + 1,
       generated.topic_id * 25,
       24,
       26
FROM generate_series(1, $3::bigint / 25) AS generated(topic_id)`, areaID, userID, rerenderPopulationPerformancePostCount); err != nil {
		_ = fixtureTx.Rollback(ctx)
		t.Fatalf("insert population topics: %v", err)
	}
	if _, err := fixtureTx.Exec(ctx, `INSERT INTO public.posts (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, parent_post_id, thread_path)
SELECT generated.id,
       ((generated.id - 1) / 25) + 1,
       $1,
       ((generated.id - 1) % 25)::integer + 1,
       $2,
       $3,
       $4,
       CASE WHEN ((generated.id - 1) % 25) = 0 THEN NULL ELSE (((generated.id - 1) / 25) * 25) + 1 END,
       CASE
           WHEN ((generated.id - 1) % 25) = 0 THEN ARRAY[1]::integer[]
           ELSE ARRAY[1, (((generated.id - 1) % 25) + 1)::integer]
       END
FROM generate_series(1, $5::bigint) AS generated(id)`, userID, populationSource, populationLegacyHTML, contentrender.LegacyRendererVersion, rerenderPopulationPerformancePostCount); err != nil {
		_ = fixtureTx.Rollback(ctx)
		t.Fatalf("insert population posts: %v", err)
	}
	if err := fixtureTx.Commit(ctx); err != nil {
		t.Fatalf("commit population fixture: %v", err)
	}
	var fixtureRows, invalidFixtureRows, fixtureTopics, invalidFixtureTopics int
	if err := connection.QueryRow(ctx, `SELECT
count(*)::integer,
count(*) FILTER (WHERE NOT (
    (post_number = 1 AND parent_post_id IS NULL AND thread_path = ARRAY[1]::integer[])
    OR
    (post_number > 1
     AND parent_post_id = ((topic_id - 1) * 25) + 1
     AND thread_path = ARRAY[1, post_number])
))::integer
FROM public.posts`).Scan(&fixtureRows, &invalidFixtureRows); err != nil {
		t.Fatalf("verify population fixture: %v", err)
	}
	if fixtureRows != rerenderPopulationPerformancePostCount || invalidFixtureRows != 0 {
		t.Fatalf("population fixture = %d rows/%d invalid, want %d/0", fixtureRows, invalidFixtureRows, rerenderPopulationPerformancePostCount)
	}
	if err := connection.QueryRow(ctx, `SELECT
count(*)::integer,
count(*) FILTER (WHERE NOT (
    post_state.post_count = 25
    AND post_state.root_count = 1
    AND topic.first_post_id = post_state.minimum_post_id
    AND topic.latest_post_id = post_state.maximum_post_id
    AND topic.reply_count = 24
    AND topic.next_post_number = 26
))::integer
FROM public.topics AS topic
CROSS JOIN LATERAL (
    SELECT count(*)::integer AS post_count,
           count(*) FILTER (WHERE post.post_number = 1 AND post.parent_post_id IS NULL)::integer AS root_count,
           min(post.id) AS minimum_post_id,
           max(post.id) AS maximum_post_id
    FROM public.posts AS post
    WHERE post.topic_id = topic.id
) AS post_state`).Scan(&fixtureTopics, &invalidFixtureTopics); err != nil {
		t.Fatalf("verify population topic relationships: %v", err)
	}
	wantTopics := rerenderPopulationPerformancePostCount / 25
	if fixtureTopics != wantTopics || invalidFixtureTopics != 0 {
		t.Fatalf("population topics = %d/%d invalid, want %d/0", fixtureTopics, invalidFixtureTopics, wantTopics)
	}
	// Fixture construction bypasses normal application activity and autovacuum
	// timing. Refresh planner statistics outside every measured release phase so
	// the plan proof represents the loaded population rather than an empty-table
	// estimate.
	if _, err := connection.Exec(ctx, `ANALYZE public.posts`); err != nil {
		t.Fatalf("analyze population fixture: %v", err)
	}
	if _, err := connection.Exec(ctx, `SET plan_cache_mode = force_generic_plan`); err != nil {
		t.Fatalf("force generic PostgreSQL plans: %v", err)
	}
	preflightPlanEvidence, err := measurePreflightSelectionPlans(ctx, connection, rerenderPopulationPerformancePostCount, MaximumBatchSize)
	if err != nil {
		t.Fatalf("measure population preflight plans: %v", err)
	}
	planEvidence, err := measureMutationSelectionPlans(ctx, connection, rerenderPopulationPerformancePostCount, MaximumBatchSize)
	if err != nil {
		t.Fatalf("measure population mutation plans: %v", err)
	}
	if _, err := connection.Exec(ctx, `SET plan_cache_mode = auto`); err != nil {
		t.Fatalf("restore automatic PostgreSQL plans: %v", err)
	}
	wantMutationQueries := (rerenderPopulationPerformancePostCount / MaximumBatchSize) + 1
	if preflightPlanEvidence.queries != wantMutationQueries || preflightPlanEvidence.returnedRows != rerenderPopulationPerformancePostCount || preflightPlanEvidence.examinedRows != rerenderPopulationPerformancePostCount {
		t.Fatalf("population preflight plans = %d queries/%d returned/%d examined, want %d/%d/%d", preflightPlanEvidence.queries, preflightPlanEvidence.returnedRows, preflightPlanEvidence.examinedRows, wantMutationQueries, rerenderPopulationPerformancePostCount, rerenderPopulationPerformancePostCount)
	}
	if planEvidence.queries != wantMutationQueries || planEvidence.returnedRows != rerenderPopulationPerformancePostCount || planEvidence.examinedRows != rerenderPopulationPerformancePostCount {
		t.Fatalf("population mutation plans = %d queries/%d returned/%d examined, want %d/%d/%d", planEvidence.queries, planEvidence.returnedRows, planEvidence.examinedRows, wantMutationQueries, rerenderPopulationPerformancePostCount, rerenderPopulationPerformancePostCount)
	}

	releaseStarted := time.Now()
	preflightStarted := time.Now()
	preflightDatabase := &countedPreflightDatabase{connection: connection}
	if err := Preflight(ctx, preflightDatabase, MaximumBatchSize); err != nil {
		t.Fatalf("preflight population: %v", err)
	}
	preflightElapsed := time.Since(preflightStarted)
	schemaStarted := time.Now()
	if err := migration.Apply(ctx, testConfig, alpha3MigrationFS(t)); err != nil {
		t.Fatalf("apply alpha.3 schema: %v", err)
	}
	schemaElapsed := time.Since(schemaStarted)
	conversionStarted := time.Now()
	converted := 0
	mutationBatches := 0
	mutationDatabase := &countedMutationDatabase{connection: connection}
	for converted < rerenderPopulationPerformancePostCount {
		result, retryable, err := runBatch(ctx, mutationDatabase, MaximumBatchSize)
		if err != nil || retryable || result.Complete || result.Converted < 1 || result.Converted > MaximumBatchSize {
			t.Fatalf("population conversion batch %d = (%+v, retryable %t, %v)", mutationBatches+1, result, retryable, err)
		}
		converted += result.Converted
		mutationBatches++
	}
	conversionElapsed := time.Since(conversionStarted)
	validationStarted := time.Now()
	completion, retryable, err := runBatch(ctx, mutationDatabase, MaximumBatchSize)
	validationElapsed := time.Since(validationStarted)
	if err != nil || retryable || !completion.Complete || completion.Converted != 0 {
		t.Fatalf("population completion = (%+v, retryable %t, %v)", completion, retryable, err)
	}
	rerenderElapsed := conversionElapsed + validationElapsed
	releaseElapsed := time.Since(releaseStarted)
	var rowCount, currentCount int
	var convertedCount int64
	var lastProcessedPostID *int64
	var completed bool
	var rendererValidated bool
	if err := connection.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.posts),
(SELECT count(*) FROM public.posts WHERE renderer_version = $1),
(SELECT converted_count FROM public.content_renderer_state WHERE singleton),
(SELECT last_processed_post_id FROM public.content_renderer_state WHERE singleton),
(SELECT completed_at IS NOT NULL FROM public.content_renderer_state WHERE singleton),
(SELECT convalidated FROM pg_catalog.pg_constraint WHERE conrelid = 'public.posts'::regclass AND conname = 'posts_renderer_version_current')`, contentrender.RendererVersion).Scan(&rowCount, &currentCount, &convertedCount, &lastProcessedPostID, &completed, &rendererValidated); err != nil {
		t.Fatalf("inspect population result: %v", err)
	}
	if rowCount != rerenderPopulationPerformancePostCount || currentCount != rowCount || convertedCount != int64(rowCount) || lastProcessedPostID == nil || *lastProcessedPostID != int64(rowCount) || !completed || !rendererValidated {
		t.Fatalf("population result = rows %d/current %d/converted %d/cursor %v/completed %t/validated %t", rowCount, currentCount, convertedCount, lastProcessedPostID, completed, rendererValidated)
	}
	preflightBatches := (rowCount / MaximumBatchSize) + 1
	preflightTransactions := preflightBatches + 1
	preflightRoundTrips := preflightBatches + 3
	if preflightDatabase.transactions != preflightTransactions || preflightDatabase.roundTrips != preflightRoundTrips {
		t.Fatalf("population preflight work = %d transactions/%d round trips, want %d/%d", preflightDatabase.transactions, preflightDatabase.roundTrips, preflightTransactions, preflightRoundTrips)
	}
	mutationSelectedRows := 0
	selectedIDs := make(map[int64]struct{}, rowCount)
	for index, selection := range mutationDatabase.selections {
		for _, id := range selection.ids {
			if selection.cursor != nil && id <= *selection.cursor {
				t.Fatalf("population mutation selection %d revisited prefix: cursor %d, id %d", index, *selection.cursor, id)
			}
			if _, duplicate := selectedIDs[id]; duplicate {
				t.Fatalf("population mutation selection %d repeated post id %d", index, id)
			}
			selectedIDs[id] = struct{}{}
			mutationSelectedRows++
		}
	}
	if len(mutationDatabase.selections) != wantMutationQueries || mutationSelectedRows != rowCount || len(selectedIDs) != rowCount || len(mutationDatabase.selections[len(mutationDatabase.selections)-1].ids) != 0 {
		t.Fatalf("population mutation selections = %d queries/%d selected/%d unique/final %d, want %d/%d/%d/0", len(mutationDatabase.selections), mutationSelectedRows, len(selectedIDs), len(mutationDatabase.selections[len(mutationDatabase.selections)-1].ids), wantMutationQueries, rowCount, rowCount)
	}
	t.Logf("population_migration rows=%d preflight_batches=%d preflight_transactions=%d preflight_round_trips=%d mutation_batches=%d mutation_selection_queries=%d mutation_selected_rows=%d generic_plan_mode=forced preflight_explain_queries=%d preflight_explain_returned_rows=%d preflight_explain_examined_rows=%d mutation_explain_queries=%d mutation_explain_returned_rows=%d mutation_explain_examined_rows=%d preflight_elapsed=%s schema_elapsed=%s conversion_elapsed=%s validation_elapsed=%s rerender_total_elapsed=%s release_total_elapsed=%s current=%d converted=%d cursor=%d completed=%t validated=%t", rowCount, preflightBatches, preflightTransactions, preflightRoundTrips, mutationBatches, len(mutationDatabase.selections), mutationSelectedRows, preflightPlanEvidence.queries, preflightPlanEvidence.returnedRows, preflightPlanEvidence.examinedRows, planEvidence.queries, planEvidence.returnedRows, planEvidence.examinedRows, preflightElapsed, schemaElapsed, conversionElapsed, validationElapsed, rerenderElapsed, releaseElapsed, currentCount, convertedCount, *lastProcessedPostID, completed, rendererValidated)
}

func preAlpha3MigrationFS(t *testing.T) fs.FS {
	t.Helper()
	legacy := fstest.MapFS{}
	for _, name := range []string{
		"000001_identity_and_sessions.sql", "000002_groups_and_areas.sql",
		"000003_topics_posts_and_reads.sql", "000004_reports_and_audit.sql",
		"000005_threaded_posts.sql", "000006_reports_moderation_completion.sql",
	} {
		body, err := fs.ReadFile(migrations.Files(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		legacy[name] = &fstest.MapFile{Data: body}
	}
	return legacy
}

func alpha3MigrationFS(t *testing.T) fs.FS {
	t.Helper()
	result := fstest.MapFS{}
	for _, name := range []string{
		"000001_identity_and_sessions.sql", "000002_groups_and_areas.sql",
		"000003_topics_posts_and_reads.sql", "000004_reports_and_audit.sql",
		"000005_threaded_posts.sql", "000006_reports_moderation_completion.sql",
		"000007_gfm_renderer.sql",
	} {
		body, err := fs.ReadFile(migrations.Files(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		result[name] = &fstest.MapFile{Data: body}
	}
	return result
}

func TestPerformanceDatabaseConfigRejectsAlternateTargets(t *testing.T) {
	t.Parallel()

	const (
		wantHost = "127.0.0.1"
		wantPort = "32797"
	)
	for _, test := range []struct {
		name       string
		connection string
	}{
		{name: "query host override", connection: "postgres://user:pass@127.0.0.1:32797/db?sslmode=disable&host=10.0.0.42"},
		{name: "query port override", connection: "postgres://user:pass@127.0.0.1:32797/db?sslmode=disable&port=5432"},
		{name: "multiple hosts", connection: "postgres://user:pass@127.0.0.1:32797,10.0.0.42:5432/db?sslmode=disable"},
		{name: "service override", connection: "postgres://user:pass@127.0.0.1:32797/db?sslmode=disable&service=alternate"},
		{name: "service file override", connection: "postgres://user:pass@127.0.0.1:32797/db?sslmode=disable&servicefile=/tmp/alternate.conf&service=alternate"},
		{name: "target session fallback", connection: "postgres://user:pass@127.0.0.1:32797/db?sslmode=disable&target_session_attrs=prefer-standby"},
		{name: "TLS fallback", connection: "postgres://user:pass@127.0.0.1:32797/db?sslmode=prefer"},
		{name: "alternate authority", connection: "postgres://user:pass@10.0.0.42:5432/db?sslmode=disable"},
		{name: "keyword settings", connection: "host=127.0.0.1 port=32797 database=db sslmode=disable"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parsePerformanceDatabaseConfig(test.connection, wantHost, wantPort); err == nil {
				t.Fatalf("parsePerformanceDatabaseConfig(%q) succeeded, want rejection", test.connection)
			}
		})
	}

	config, err := parsePerformanceDatabaseConfig("postgres://user:pass@127.0.0.1:32797/db?sslmode=disable", wantHost, wantPort)
	if err != nil {
		t.Fatalf("parse exact performance target: %v", err)
	}
	if config.Host != wantHost || config.Port != 32797 || config.Database != "db" || len(config.Fallbacks) != 0 || config.TLSConfig != nil {
		t.Fatalf("exact performance target = host %q/port %d/database %q/fallbacks %d/TLS %t", config.Host, config.Port, config.Database, len(config.Fallbacks), config.TLSConfig != nil)
	}
}

func parsePerformanceDatabaseConfig(connectionString, wantHost, wantPortText string) (*pgx.ConnConfig, error) {
	if !strings.HasPrefix(connectionString, "postgres://") && !strings.HasPrefix(connectionString, "postgresql://") {
		return nil, errors.New("performance database target must be a PostgreSQL URI")
	}
	parsedURL, err := url.Parse(connectionString)
	if err != nil {
		return nil, fmt.Errorf("parse performance database URI: %w", err)
	}
	query := parsedURL.Query()
	for key, values := range query {
		if key != "sslmode" || len(values) != 1 || values[0] != "disable" {
			return nil, fmt.Errorf("performance database URI query key %q is not the single required sslmode=disable setting", key)
		}
	}
	if query.Get("sslmode") != "disable" {
		return nil, errors.New("performance database URI requires sslmode=disable")
	}
	wantPort, err := strconv.ParseUint(wantPortText, 10, 16)
	if err != nil || wantPort == 0 || wantHost != "127.0.0.1" {
		return nil, fmt.Errorf("invalid inspected container endpoint %q:%q", wantHost, wantPortText)
	}
	config, err := pgx.ParseConfigWithOptions(connectionString, pgx.ParseConfigOptions{ParseConfigOptions: pgconn.ParseConfigOptions{ConnStringAllowedKeys: []string{"host", "port", "database", "user", "password", "sslmode"}}})
	if err != nil {
		return nil, fmt.Errorf("parse effective pgx target: %w", err)
	}
	if config.Host != wantHost || config.Port != uint16(wantPort) || len(config.Fallbacks) != 0 || config.TLSConfig != nil {
		return nil, fmt.Errorf("effective pgx target = host %q/port %d/fallbacks %d/TLS %t, want %q/%d/0/false", config.Host, config.Port, len(config.Fallbacks), config.TLSConfig != nil, wantHost, wantPort)
	}
	return config, nil
}

func logPerformanceServerIdentity(t *testing.T, ctx context.Context, connection *pgx.Conn, wantDatabase string) {
	t.Helper()
	var versionNumber, serverPort int
	var serverAddress, database, systemIdentifier string
	if err := connection.QueryRow(ctx, `SELECT
current_setting('server_version_num')::integer,
COALESCE(inet_server_addr()::text, ''),
COALESCE(inet_server_port(), 0),
current_database(),
system_identifier::text
FROM pg_control_system()`).Scan(&versionNumber, &serverAddress, &serverPort, &database, &systemIdentifier); err != nil {
		t.Fatalf("query live PostgreSQL identity: %v", err)
	}
	wantSystemIdentifier := os.Getenv("GOTTH_BB_EXPECTED_DATABASE_SYSTEM_IDENTIFIER")
	if versionNumber != 170010 || serverAddress == "" || serverPort != 5432 || database != wantDatabase || wantSystemIdentifier == "" || systemIdentifier != wantSystemIdentifier {
		t.Fatalf("live PostgreSQL identity = version %d/address %q/port %d/database %q/system %q, want 170010/nonempty/5432/%q/%q", versionNumber, serverAddress, serverPort, database, systemIdentifier, wantDatabase, wantSystemIdentifier)
	}
	t.Logf("sql_server_identity version_num=%d address=%s port=%d database=%s system_identifier=%s", versionNumber, serverAddress, serverPort, database, systemIdentifier)
}

func exactLegacyHTML(t *testing.T, source string) string {
	t.Helper()
	var rendered bytes.Buffer
	if err := goldmark.New().Convert([]byte(source), &rendered); err != nil {
		t.Fatalf("render legacy fixture: %v", err)
	}
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "em", "strong", "ul", "ol", "li", "a", "blockquote", "pre", "code", "br")
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy.Sanitize(rendered.String())
}
