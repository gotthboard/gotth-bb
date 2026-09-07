//go:build integration

package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const discoveryPlanTestDatabase = "gotth_bb_an02_02_plan_test"

const discoveryAdmissionTestDatabase = "gotth_bb_an02_04_admission_test"

type discoveryPlanPopulation struct {
	database      string
	topics        int64
	posts         int64
	postsPerTopic int64
	postIDOffset  int64
	timeout       time.Duration
	admission     bool
}

func TestDiscoveryPlansOnPostgreSQL17(t *testing.T) {
	population := requestedDiscoveryPlanPopulation(t)
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), population.timeout)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+population.database+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+population.database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+population.database+" WITH (FORCE)")
	})
	configured := adminConfig.Copy()
	configured.Database = population.database
	if err := migration.Apply(ctx, configured, migrations.Files()); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.ConnectConfig(ctx, configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	var serverVersion string
	if err := connection.QueryRow(ctx, `SELECT version()`).Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}
	t.Logf("postgres=%s database=%s admission=%t topics=%d posts=%d posts_per_topic=%d", serverVersion, population.database, population.admission, population.topics, population.posts, population.postsPerTopic)

	var ownerID, authorID, groupID, publicAreaID, authenticatedAreaID, groupAreaID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Owner', 'administrator') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Author') RETURNING id`).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Plan Group', $1) RETURNING id`, ownerID).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, visibility, created_by, updated_by) VALUES ('public', 'Public', 'public', $1, $1) RETURNING id`, ownerID).Scan(&publicAreaID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, visibility, created_by, updated_by) VALUES ('authenticated', 'Authenticated', 'authenticated', $1, $1) RETURNING id`, ownerID).Scan(&authenticatedAreaID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, visibility, created_by, updated_by) VALUES ('restricted', 'Restricted', 'groups', $1, $1) RETURNING id`, ownerID).Scan(&groupAreaID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by) VALUES ($1, $2, $3)`, groupAreaID, groupID, ownerID); err != nil {
		t.Fatal(err)
	}
	populationStart := time.Now()
	if _, err := connection.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.topics
    (id, area_id, author_id, title, state, first_post_id, latest_post_id, reply_count, next_post_number, created_at, updated_at, last_activity_at, search_vector, search_projection_version)
OVERRIDING SYSTEM VALUE
SELECT series,
       CASE series % 3 WHEN 0 THEN $1::bigint WHEN 1 THEN $2::bigint ELSE $3::bigint END,
       CASE WHEN series % 100 = 0 THEN $4::bigint ELSE $5::bigint END,
       CASE WHEN series % 997 = 0 THEN 'rareterm' ELSE 'common term' END,
       CASE WHEN series % 101 = 0 THEN 'hidden' ELSE 'open' END,
       $6::bigint + ((series - 1) * $7::bigint) + 1,
       $6::bigint + (series * $7::bigint),
       ($7::bigint - 1)::integer,
       ($7::bigint + 1)::integer,
       '2026-01-01T00:00:00Z'::timestamptz,
       '2026-01-01T00:00:00Z'::timestamptz,
       '2026-01-01T00:00:00Z'::timestamptz + interval '1 second',
       to_tsvector('pg_catalog.simple'::regconfig, CASE WHEN series % 997 = 0 THEN 'rareterm' ELSE 'common term' END),
       'search-v1-pg17-simple-u15-p2'
FROM generate_series(1, $8::bigint) AS series`, publicAreaID, authenticatedAreaID, groupAreaID, authorID, ownerID, population.postIDOffset, population.postsPerTopic, population.topics); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.topics
SET deleted_at = created_at
WHERE id % 1009 = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.posts
    (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, revision,
     created_at, updated_at, deleted_at, deleted_by, deletion_reason, redacted_at, redacted_by, redaction_reason,
     parent_post_id, thread_path, search_vector, search_projection_version)
OVERRIDING SYSTEM VALUE
SELECT $1::bigint + series,
       ((series - 1) / $2::bigint) + 1,
       CASE WHEN series % 100 = 0 THEN $3::bigint ELSE $4::bigint END,
       (((series - 1) % $2::bigint) + 1)::integer,
       CASE WHEN series % 1009 = 0 THEN '[Content removed by moderation]' WHEN series % 997 = 0 THEN 'rareterm body' ELSE 'common body' END,
       CASE WHEN series % 1009 = 0 THEN '<p>Content removed by moderation.</p>' WHEN series % 997 = 0 THEN '<p>rareterm body</p>' ELSE '<p>common body</p>' END,
       CASE WHEN series % 1009 = 0 THEN 'moderation-redaction-v1' ELSE 'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2' END,
       1,
       '2026-01-01T00:00:00Z'::timestamptz + (series % 10000) * interval '1 microsecond',
       '2026-01-01T00:00:00Z'::timestamptz + (series % 10000) * interval '1 microsecond',
       CASE WHEN series % 1009 = 0 OR series % 1013 = 0 THEN '2026-01-01T00:00:01Z'::timestamptz END,
       CASE WHEN series % 1009 = 0 OR series % 1013 = 0 THEN $4::bigint END,
       CASE WHEN series % 1009 = 0 THEN 'redacted' WHEN series % 1013 = 0 THEN 'deleted' END,
       CASE WHEN series % 1009 = 0 THEN '2026-01-01T00:00:01Z'::timestamptz END,
       CASE WHEN series % 1009 = 0 THEN $4::bigint END,
       CASE WHEN series % 1009 = 0 THEN 'redacted' END,
       CASE WHEN (series - 1) % $2::bigint = 0 THEN NULL ELSE $1::bigint + (((series - 1) / $2::bigint) * $2::bigint) + 1 END,
       CASE WHEN (series - 1) % $2::bigint = 0 THEN ARRAY[1]::integer[] ELSE ARRAY[1, (((series - 1) % $2::bigint) + 1)::integer] END,
       CASE WHEN series % 1009 = 0 THEN ''::tsvector ELSE to_tsvector('pg_catalog.simple'::regconfig, CASE WHEN series % 997 = 0 THEN 'rareterm body' ELSE 'common body' END) END,
       'search-v1-pg17-simple-u15-p2'
FROM generate_series(1, $5::bigint) AS series`, population.postIDOffset, population.postsPerTopic, authorID, ownerID, population.posts); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `SELECT
setval(pg_get_serial_sequence('public.topics', 'id'), $1, true),
setval(pg_get_serial_sequence('public.posts', 'id'), $2, true)`, population.topics, population.postIDOffset+population.posts); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `SET session_replication_role = origin; ANALYZE public.topics; ANALYZE public.posts; ANALYZE public.areas; ANALYZE public.area_groups`); err != nil {
		t.Fatal(err)
	}
	var topicRows, postRows, publicTopics, authenticatedTopics, groupTopics, hiddenTopics, deletedTopics, deletedPosts, redactedPosts, rareTopics, rarePosts, activityTimestamps int64
	if err := connection.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM public.topics),
    (SELECT count(*) FROM public.posts),
    (SELECT count(*) FROM public.topics AS topic JOIN public.areas AS area ON area.id = topic.area_id WHERE area.visibility = 'public'),
    (SELECT count(*) FROM public.topics AS topic JOIN public.areas AS area ON area.id = topic.area_id WHERE area.visibility = 'authenticated'),
    (SELECT count(*) FROM public.topics AS topic JOIN public.areas AS area ON area.id = topic.area_id WHERE area.visibility = 'groups'),
    (SELECT count(*) FROM public.topics WHERE state = 'hidden'),
    (SELECT count(*) FROM public.topics WHERE deleted_at IS NOT NULL),
    (SELECT count(*) FROM public.posts WHERE deleted_at IS NOT NULL),
    (SELECT count(*) FROM public.posts WHERE redacted_at IS NOT NULL),
    (SELECT count(*) FROM public.topics WHERE search_vector @@ to_tsquery('pg_catalog.simple'::regconfig, 'rareterm')),
    (SELECT count(*) FROM public.posts WHERE search_vector @@ to_tsquery('pg_catalog.simple'::regconfig, 'rareterm')),
    (SELECT count(DISTINCT created_at) FROM public.posts)`).Scan(
		&topicRows, &postRows, &publicTopics, &authenticatedTopics, &groupTopics, &hiddenTopics, &deletedTopics, &deletedPosts, &redactedPosts, &rareTopics, &rarePosts, &activityTimestamps,
	); err != nil {
		t.Fatal(err)
	}
	if topicRows != population.topics || postRows != population.posts || publicTopics == 0 || authenticatedTopics == 0 || groupTopics == 0 || hiddenTopics == 0 || deletedTopics == 0 || deletedPosts == 0 || redactedPosts == 0 || rareTopics == 0 || rarePosts == 0 || activityTimestamps >= postRows {
		t.Fatalf("population distribution = topics=%d posts=%d visibility=%d/%d/%d hidden_topics=%d deleted_topics=%d deleted_posts=%d redacted_posts=%d rare=%d/%d activity_timestamps=%d", topicRows, postRows, publicTopics, authenticatedTopics, groupTopics, hiddenTopics, deletedTopics, deletedPosts, redactedPosts, rareTopics, rarePosts, activityTimestamps)
	}
	t.Logf("population rows topics=%d posts=%d visibility_public/authenticated/groups=%d/%d/%d hidden_topics=%d deleted_topics=%d deleted_posts=%d redacted_posts=%d rare_topics/posts=%d/%d activity_timestamps=%d duration=%s", topicRows, postRows, publicTopics, authenticatedTopics, groupTopics, hiddenTopics, deletedTopics, deletedPosts, redactedPosts, rareTopics, rarePosts, activityTimestamps, time.Since(populationStart))
	logDiscoveryResourceSnapshot(t, ctx, connection, "populated")

	searchShapes := []struct {
		name      string
		arguments string
	}{
		{name: "current-vector-author", arguments: fmt.Sprintf("0,false,true,ARRAY[%d]::bigint[],false,$$'common'$$,%d,'',false,'2000-01-01T00:00:00Z',false,false,'2000-01-01T00:00:00Z'", groupID, authorID)},
		{name: "author", arguments: fmt.Sprintf("0,false,true,ARRAY[%d]::bigint[],true,$$'common'$$,%d,'',false,'2000-01-01T00:00:00Z',false,false,'2000-01-01T00:00:00Z'", groupID, authorID)},
		{name: "date-area", arguments: fmt.Sprintf("0,false,true,ARRAY[%d]::bigint[],true,$$'common'$$,0,'public',true,'2026-01-01T00:00:00Z',true,false,'2026-01-02T00:00:00Z'", groupID)},
		{name: "common-term", arguments: fmt.Sprintf("0,false,true,ARRAY[%d]::bigint[],true,$$'common'$$,0,'',false,'2000-01-01T00:00:00Z',false,false,'2000-01-01T00:00:00Z'", groupID)},
		{name: "rare-term", arguments: fmt.Sprintf("0,false,true,ARRAY[%d]::bigint[],true,$$'rareterm'$$,0,'',false,'2000-01-01T00:00:00Z',false,false,'2000-01-01T00:00:00Z'", groupID)},
	}
	activityArgs := fmt.Sprintf("false,true,ARRAY[%d]::bigint[],'2026-01-01T00:00:00.010000Z',%d", groupID, population.postIDOffset+population.posts+1)
	directPostID := population.postIDOffset + 2*population.postsPerTopic + 1
	directArgs := fmt.Sprintf("%d,false,false,ARRAY[]::bigint[]", directPostID)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		for _, shape := range searchShapes {
			searchPlan := explainPrepared(t, ctx, connection, "an02_search", "integer,boolean,boolean,bigint[],boolean,text,bigint,text,boolean,timestamptz,boolean,boolean,timestamptz", searchDiscoveryPage, shape.arguments, mode)
			expectedRows := int64(51)
			if !population.admission && shape.name == "rare-term" {
				expectedRows = population.topics / 997
			}
			candidate := requireAuthorizedSearchCandidate(t, mode, shape.name, expectedRows, searchPlan)
			if mode == "force_custom_plan" && (shape.name == "current-vector-author" || shape.name == "author") &&
				(!planUsesIndex(candidate, "topics_search_author_current_idx") || !planUsesIndex(candidate, "posts_search_author_current_idx")) {
				t.Fatalf("%s %s search plan lost author indexes: %s", mode, shape.name, searchPlan)
			}
			if mode == "force_custom_plan" && shape.name == "rare-term" &&
				(!planUsesIndex(candidate, "topics_search_vector_current_idx") || !planUsesIndex(candidate, "posts_search_vector_current_idx")) {
				t.Fatalf("%s rare-term search plan lost topic or post GIN index: %s", mode, searchPlan)
			}
			t.Logf("PLAN mode=%s query=search shape=%s\n%s", mode, shape.name, searchPlan)
		}
		activityPlan := explainPrepared(t, ctx, connection, "an02_activity", "boolean,boolean,bigint[],timestamptz,bigint", listRecentActivityAfter, activityArgs, mode)
		if !strings.Contains(activityPlan, `"Index Name":"posts_activity_current_idx"`) || !strings.Contains(activityPlan, `ROW(created_at, id) <`) {
			t.Fatalf("%s activity plan lost direct keyset index condition: %s", mode, activityPlan)
		}
		t.Logf("PLAN mode=%s query=activity\n%s", mode, activityPlan)
		directPlan := explainPrepared(t, ctx, connection, "an02_direct", "bigint,boolean,boolean,bigint[]", getDirectPost, directArgs, mode)
		if !strings.Contains(directPlan, `"Index Name":"posts_pkey"`) || !strings.Contains(directPlan, `(id =`) {
			t.Fatalf("%s direct-post plan lost primary-key start: %s", mode, directPlan)
		}
		t.Logf("PLAN mode=%s query=direct-post\n%s", mode, directPlan)
		t.Logf("%s search/activity/direct plans admitted", mode)
	}
	if population.admission {
		runDiscoveryCoexistenceEvidence(t, ctx, configured, connection, publicAreaID, ownerID, groupID, population)
		logDiscoveryResourceSnapshot(t, ctx, connection, "completed")
	}
}

type explainPlanDocument []struct {
	Plan explainPlanNode `json:"Plan"`
}

type explainPlanNode struct {
	NodeType     string            `json:"Node Type"`
	SubplanName  string            `json:"Subplan Name"`
	PlanRows     int64             `json:"Plan Rows"`
	ActualRows   int64             `json:"Actual Rows"`
	RelationName string            `json:"Relation Name"`
	IndexName    string            `json:"Index Name"`
	Filter       string            `json:"Filter"`
	RecheckCond  string            `json:"Recheck Cond"`
	Plans        []explainPlanNode `json:"Plans"`
}

func requireAuthorizedSearchCandidate(t *testing.T, mode, shape string, expectedRows int64, encoded string) explainPlanNode {
	t.Helper()
	var document explainPlanDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatalf("%s %s decode search plan: %v", mode, shape, err)
	}
	if len(document) != 1 {
		t.Fatalf("%s %s search plan documents = %d", mode, shape, len(document))
	}
	candidate := findPlanNode(&document[0].Plan, func(node *explainPlanNode) bool {
		return node.NodeType == "Limit" && node.SubplanName == "CTE candidate"
	})
	if candidate == nil {
		t.Fatalf("%s %s search plan lost candidate limit: %s", mode, shape, encoded)
	}
	if candidate.PlanRows != 51 || candidate.ActualRows != expectedRows {
		t.Fatalf("%s %s candidate fence = plan_rows=%d actual_rows=%d, want 51/%d: %s", mode, shape, candidate.PlanRows, candidate.ActualRows, expectedRows, encoded)
	}
	requiredRelations := []struct {
		relation string
		filter   string
	}{
		{relation: "areas", filter: "visibility"},
		{relation: "area_groups", filter: "group_id"},
	}
	for _, required := range requiredRelations {
		if !planUsesFilteredRelation(*candidate, required.relation, required.filter) {
			t.Fatalf("%s %s candidate lost authorized %s filter %q: %s", mode, shape, required.relation, required.filter, encoded)
		}
	}
	if !planUsesAuthorizedTopicRelation(*candidate) {
		t.Fatalf("%s %s candidate lost current visible topic relation: %s", mode, shape, encoded)
	}
	if !planUsesCurrentPostRelation(*candidate) {
		t.Fatalf("%s %s candidate lost current post relation: %s", mode, shape, encoded)
	}
	return *candidate
}

func findPlanNode(node *explainPlanNode, match func(*explainPlanNode) bool) *explainPlanNode {
	if match(node) {
		return node
	}
	for index := range node.Plans {
		if found := findPlanNode(&node.Plans[index], match); found != nil {
			return found
		}
	}
	return nil
}

func planUsesIndex(node explainPlanNode, indexName string) bool {
	return findPlanNode(&node, func(candidate *explainPlanNode) bool {
		return candidate.IndexName == indexName
	}) != nil
}

func planUsesFilteredRelation(node explainPlanNode, relation, filter string) bool {
	return findPlanNode(&node, func(candidate *explainPlanNode) bool {
		return candidate.RelationName == relation && strings.Contains(candidate.Filter, filter)
	}) != nil
}

func planUsesAuthorizedTopicRelation(node explainPlanNode) bool {
	return findPlanNode(&node, func(candidate *explainPlanNode) bool {
		if candidate.RelationName != "topics" || !strings.Contains(candidate.Filter, "state") {
			return false
		}
		return strings.Contains(candidate.Filter, "deleted_at") || strings.Contains(candidate.RecheckCond, "deleted_at") ||
			candidate.IndexName == "topics_search_author_current_idx" ||
			candidate.IndexName == "topics_search_vector_current_idx"
	}) != nil
}

func planUsesCurrentPostRelation(node explainPlanNode) bool {
	return findPlanNode(&node, func(candidate *explainPlanNode) bool {
		if candidate.RelationName != "posts" {
			return false
		}
		conditions := candidate.Filter + candidate.RecheckCond
		return (strings.Contains(conditions, "deleted_at") && strings.Contains(conditions, "redacted_at")) ||
			candidate.IndexName == "posts_search_author_current_idx" ||
			candidate.IndexName == "posts_search_vector_current_idx"
	}) != nil
}

func requestedDiscoveryPlanPopulation(t *testing.T) discoveryPlanPopulation {
	t.Helper()
	checkpoint := os.Getenv("GOTTH_BB_RUN_DISCOVERY_PLAN_EVIDENCE") == "1"
	admission := os.Getenv("GOTTH_BB_RUN_AN02_ADMISSION_EVIDENCE") == "1"
	if checkpoint && admission {
		t.Fatal("set only one discovery evidence mode")
	}
	if !checkpoint && !admission {
		t.Skip("set exactly one of GOTTH_BB_RUN_DISCOVERY_PLAN_EVIDENCE=1 or GOTTH_BB_RUN_AN02_ADMISSION_EVIDENCE=1")
	}
	if admission {
		return discoveryPlanPopulation{database: discoveryAdmissionTestDatabase, topics: 100_000, posts: 1_000_000, postsPerTopic: 10, postIDOffset: 1_000_000, timeout: 20 * time.Minute, admission: true}
	}
	return discoveryPlanPopulation{database: discoveryPlanTestDatabase, topics: 25_000, posts: 25_000, postsPerTopic: 1, postIDOffset: 100_000, timeout: 3 * time.Minute}
}

func logDiscoveryResourceSnapshot(t *testing.T, ctx context.Context, connection *pgx.Conn, label string) {
	t.Helper()
	var databaseBytes, topicsBytes, postsBytes, tempFiles, tempBytes int64
	if err := connection.QueryRow(ctx, `SELECT
    pg_database_size(current_database()),
    pg_total_relation_size('public.topics'),
    pg_total_relation_size('public.posts'),
    temp_files,
    temp_bytes
FROM pg_stat_database
WHERE datname = current_database()`).Scan(&databaseBytes, &topicsBytes, &postsBytes, &tempFiles, &tempBytes); err != nil {
		t.Fatal(err)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("RESOURCE label=%s database_bytes=%d topics_bytes=%d posts_bytes=%d temp_files=%d temp_bytes=%d go_heap_alloc=%d go_total_alloc=%d go_sys=%d process_rss_kib=%s", label, databaseBytes, topicsBytes, postsBytes, tempFiles, tempBytes, memory.HeapAlloc, memory.TotalAlloc, memory.Sys, processRSSKiB())
}

func processRSSKiB() string {
	contents, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
		}
	}
	return "unavailable"
}

func runDiscoveryCoexistenceEvidence(t *testing.T, ctx context.Context, configured *pgx.ConnConfig, observer *pgx.Conn, publicAreaID, ownerID, groupID int64, population discoveryPlanPopulation) {
	t.Helper()
	var baselineConnections int64
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`).Scan(&baselineConnections); err != nil {
		t.Fatal(err)
	}
	connections := make([]*pgx.Conn, 4)
	for index := range connections {
		connection, err := pgx.ConnectConfig(ctx, configured.Copy())
		if err != nil {
			t.Fatal(err)
		}
		connections[index] = connection
	}
	start := make(chan struct{})
	results := make(chan string, len(connections))
	var wait sync.WaitGroup
	run := func(name string, action func(context.Context, *pgx.Conn) error, connection *pgx.Conn) {
		defer wait.Done()
		<-start
		started := time.Now()
		err := action(ctx, connection)
		results <- fmt.Sprintf("%s duration=%s error=%v", name, time.Since(started), err)
	}
	searchParameters := SearchDiscoveryPageParams{
		IsMember: true, GroupIds: []int64{groupID}, HasQuery: true, ParsedQuery: "'common'", PageOffset: 0,
	}
	activityParameters := ListRecentActivityAfterParams{
		IsMember: true, GroupIds: []int64{groupID}, CursorCreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 1, 1, 0, 0, 0, 10_000_000, time.UTC), Valid: true}, CursorPostID: population.postIDOffset + population.posts + 1,
	}
	actions := []struct {
		name   string
		action func(context.Context, *pgx.Conn) error
	}{
		{name: "discovery-search", action: func(ctx context.Context, connection *pgx.Conn) error {
			rows, err := New(connection).SearchDiscoveryPage(ctx, searchParameters)
			if err == nil && len(rows) == 0 {
				return fmt.Errorf("search returned no rows")
			}
			return err
		}},
		{name: "discovery-activity", action: func(ctx context.Context, connection *pgx.Conn) error {
			rows, err := New(connection).ListRecentActivityAfter(ctx, activityParameters)
			if err == nil && len(rows) != 26 {
				return fmt.Errorf("activity returned %d rows", len(rows))
			}
			return err
		}},
		{name: "ordinary-read", action: func(ctx context.Context, connection *pgx.Conn) error {
			rows, err := connection.Query(ctx, `SELECT id FROM public.topics WHERE deleted_at IS NULL ORDER BY last_activity_at DESC, id DESC LIMIT 25`)
			if err != nil {
				return err
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				count++
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if count != 25 {
				return fmt.Errorf("ordinary read returned %d rows", count)
			}
			return nil
		}},
		{name: "publication", action: func(ctx context.Context, connection *pgx.Conn) error {
			tx, err := connection.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			created, err := New(tx).CreateTopicAndFirstPost(ctx, CreateTopicAndFirstPostParams{
				AreaID: publicAreaID, AuthorID: ownerID, Title: "Admission publication", AtTime: pgtype.Timestamptz{Time: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC), Valid: true},
				TopicSearchText: "admission publication", SearchProjectionVersion: pgtype.Text{String: "search-v1-pg17-simple-u15-p2", Valid: true},
				MarkdownSource: "admission publication", RenderedHtml: "<p>admission publication</p>", RendererVersion: "goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2", PostSearchText: "admission publication",
			})
			if err != nil {
				return fmt.Errorf("publication query: %w", err)
			}
			if created.TopicID <= population.topics || created.PostID <= population.postIDOffset+population.posts {
				return fmt.Errorf("publication returned invalid identities: %+v", created)
			}
			return tx.Commit(ctx)
		}},
	}
	for index := range actions {
		wait.Add(1)
		go run(actions[index].name, actions[index].action, connections[index])
	}
	close(start)
	wait.Wait()
	close(results)
	for result := range results {
		t.Logf("COEXISTENCE %s", result)
		if !strings.HasSuffix(result, "error=<nil>") {
			t.Fatal(result)
		}
	}
	for _, connection := range connections {
		if err := connection.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var finalConnections int64
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`).Scan(&finalConnections); err != nil {
		t.Fatal(err)
	}
	if finalConnections != baselineConnections {
		t.Fatalf("connection cleanup baseline=%d final=%d", baselineConnections, finalConnections)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(observer).SearchDiscoveryPage(canceled, searchParameters); err == nil {
		t.Fatal("canceled discovery query returned no error")
	}
	t.Logf("COEXISTENCE connection_cleanup baseline=%d final=%d cancellation=pass", baselineConnections, finalConnections)
}

func explainPrepared(t *testing.T, ctx context.Context, connection *pgx.Conn, name, parameterTypes, query, arguments, mode string) string {
	t.Helper()
	if err := connection.DeallocateAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, "SET plan_cache_mode = "+mode); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, "PREPARE "+name+"("+parameterTypes+") AS "+query); err != nil {
		t.Fatal(err)
	}
	var plan []byte
	if err := connection.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE "+name+"("+arguments+")").Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, plan); err != nil {
		t.Fatalf("compact plan JSON: %v", err)
	}
	return compact.String()
}
