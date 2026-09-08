//go:build integration

package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const discoveryPlanTestDatabase = "gotth_bb_an02_02_plan_test"

const discoveryAdmissionTestDatabase = "gotth_bb_an02_04_admission_test"

const unreadPlanTestDatabase = "gotth_bb_an03_01_plan_test"

type discoveryPlanPopulation struct {
	database      string
	topics        int64
	posts         int64
	postsPerTopic int64
	postIDOffset  int64
	timeout       time.Duration
	admission     bool
	unread        bool
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
	schemaFiles := fs.FS(migrations.Files())
	if population.unread {
		schemaFiles = migrationPrefix(t, 8)
	}
	if err := migration.Apply(ctx, configured, schemaFiles); err != nil {
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

	var ownerID, authorID, readerID, groupID, publicAreaID, authenticatedAreaID, groupAreaID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Owner', 'administrator') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Author') RETURNING id`).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Reader') RETURNING id`).Scan(&readerID); err != nil {
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
	if population.unread {
		if _, err := connection.Exec(ctx, `INSERT INTO public.topic_reads (user_id, topic_id, last_read_post_number, read_at)
SELECT $1, topic.id, 5, '2026-01-01T00:00:02Z'::timestamptz
FROM public.topics AS topic
WHERE topic.id % 2 = 0`, readerID); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, `ANALYZE public.topic_reads`); err != nil {
			t.Fatal(err)
		}
		runUnreadMigrationEvidence(t, ctx, configured, connection)
		if _, err := connection.Exec(ctx, `ANALYZE public.posts; ANALYZE public.topic_reads`); err != nil {
			t.Fatal(err)
		}
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
			if !population.admission && !population.unread && shape.name == "rare-term" {
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
		if population.unread {
			visitorBoardPlan := explainPrepared(t, ctx, connection, "an03_visitor_board", "boolean,boolean,bigint[]", listVisibleAreaSummaries, "false,false,ARRAY[]::bigint[]", mode)
			requireVisitorReadPlan(t, mode, "visitor-board", visitorBoardPlan)
			t.Logf("PLAN mode=%s actor=visitor query=board\n%s", mode, visitorBoardPlan)
			visitorAreaPlan := explainPrepared(t, ctx, connection, "an03_visitor_area", "text,boolean,boolean,bigint[],integer,integer", listVisibleTopicsByAreaSlug, "'public',false,false,ARRAY[]::bigint[],0,25", mode)
			requireVisitorReadPlan(t, mode, "visitor-area", visitorAreaPlan)
			t.Logf("PLAN mode=%s actor=visitor query=area\n%s", mode, visitorAreaPlan)

			authenticatedShapes := []struct {
				name                string
				isStaff             bool
				groups              string
				areaSlug            string
				requireGroupSubplan bool
			}{
				{name: "member", groups: "ARRAY[]::bigint[]", areaSlug: "authenticated"},
				{name: "group", groups: fmt.Sprintf("ARRAY[%d]::bigint[]", groupID), areaSlug: "restricted", requireGroupSubplan: true},
				{name: "staff", isStaff: true, groups: "ARRAY[]::bigint[]", areaSlug: "restricted"},
			}
			for _, shape := range authenticatedShapes {
				areaArguments := fmt.Sprintf("%t,%s,%d", shape.isStaff, shape.groups, readerID)
				areaPlan := explainPrepared(t, ctx, connection, "an03_board_"+shape.name, "boolean,bigint[],bigint", listAuthenticatedVisibleAreaSummaries, areaArguments, mode)
				requireUnreadAuthorizationPlan(t, mode, shape.name+"-board", "visible_areas", areaPlan, false, shape.isStaff, shape.requireGroupSubplan)
				t.Logf("PLAN mode=%s actor=%s query=board\n%s", mode, shape.name, areaPlan)
				topicArguments := fmt.Sprintf("%d,'%s',%t,%s,0,25", readerID, shape.areaSlug, shape.isStaff, shape.groups)
				topicPlan := explainPrepared(t, ctx, connection, "an03_area_"+shape.name, "bigint,text,boolean,bigint[],integer,integer", listAuthenticatedVisibleTopicsByAreaSlug, topicArguments, mode)
				requireUnreadAuthorizationPlan(t, mode, shape.name+"-area", "visible_area", topicPlan, true, shape.isStaff, shape.requireGroupSubplan)
				t.Logf("PLAN mode=%s actor=%s query=area\n%s", mode, shape.name, topicPlan)
			}
			markArguments := fmt.Sprintf("2,false,ARRAY[%d]::bigint[],%d", groupID, readerID)
			markPlan := explainPrepared(t, ctx, connection, "an03_mark_read", "bigint,boolean,bigint[],bigint", markTopicReadBoundary, markArguments, mode)
			requireMarkReadAuthorizationPlan(t, mode, markPlan)
			t.Logf("PLAN mode=%s actor=member query=mark-read\n%s", mode, markPlan)
		}
	}
	if population.admission {
		runDiscoveryCoexistenceEvidence(t, ctx, configured, connection, publicAreaID, ownerID, groupID, population)
		logDiscoveryResourceSnapshot(t, ctx, connection, "completed")
	}
}

func requireMarkReadAuthorizationPlan(t *testing.T, mode, encoded string) {
	t.Helper()
	var document explainPlanDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil || len(document) != 1 {
		t.Fatalf("%s decode mark-read plan: documents=%d error=%v", mode, len(document), err)
	}
	root := document[0].Plan
	authorized := findPlanNode(&root, func(node *explainPlanNode) bool {
		return node.SubplanName == "CTE authorized_topic"
	})
	boundary := findPlanNode(&root, func(node *explainPlanNode) bool {
		return node.SubplanName == "CTE boundary"
	})
	if authorized == nil || boundary == nil ||
		!planUsesConditionedRelation(*authorized, "areas", "visibility") ||
		!planUsesConditionedRelation(*authorized, "topics", "deleted_at") ||
		!planUsesConditionedRelation(*authorized, "topics", "state") {
		t.Fatalf("%s mark-read plan lost materialized authorization fence: %s", mode, encoded)
	}
	groupMembership := findPlanNode(authorized, func(node *explainPlanNode) bool {
		return node.RelationName == "area_groups" && strings.Contains(node.Filter+node.RecheckCond+node.IndexCond, "group_id")
	})
	if groupMembership == nil || groupMembership.Parent != "SubPlan" {
		t.Fatalf("%s mark-read plan lost non-multiplying group-membership subplan: %s", mode, encoded)
	}
	if !planUsesConditionedRelation(*boundary, "posts", "author_id") ||
		!planUsesIndex(*boundary, "posts_topic_unread_visible_idx") {
		t.Fatalf("%s mark-read plan lost actor-excluding indexed boundary: %s", mode, encoded)
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
	IndexCond    string            `json:"Index Cond"`
	JoinType     string            `json:"Join Type"`
	Parent       string            `json:"Parent Relationship"`
	Plans        []explainPlanNode `json:"Plans"`
}

func requireVisitorReadPlan(t *testing.T, mode, query, encoded string) {
	t.Helper()
	var document explainPlanDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil || len(document) != 1 {
		t.Fatalf("%s %s decode visitor plan: documents=%d error=%v", mode, query, len(document), err)
	}
	root := document[0].Plan
	if !planUsesConditionedRelation(root, "areas", "visibility") ||
		!planUsesConditionedRelation(root, "topics", "deleted_at") ||
		!planUsesConditionedRelation(root, "topics", "state") {
		t.Fatalf("%s %s plan lost visitor authorization filters: %s", mode, query, encoded)
	}
	if findPlanNode(&root, func(node *explainPlanNode) bool { return node.RelationName == "topic_reads" }) != nil {
		t.Fatalf("%s %s visitor plan accessed private read markers: %s", mode, query, encoded)
	}
}

func requireUnreadAuthorizationPlan(t *testing.T, mode, query, areaCTE string, encoded string, requirePostIndex, isStaff, requireGroupSubplan bool) {
	t.Helper()
	var document explainPlanDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil || len(document) != 1 {
		t.Fatalf("%s %s decode unread plan: documents=%d error=%v", mode, query, len(document), err)
	}
	root := document[0].Plan
	area := findPlanNode(&root, func(node *explainPlanNode) bool { return node.SubplanName == "CTE "+areaCTE })
	topics := findPlanNode(&root, func(node *explainPlanNode) bool { return node.SubplanName == "CTE visible_topics" })
	if area == nil || topics == nil || !planUsesConditionedRelation(*topics, "topics", "deleted_at") {
		t.Fatalf("%s %s plan lost materialized authorization fence: %s", mode, query, encoded)
	}
	if !isStaff && (!planUsesConditionedRelation(*area, "areas", "visibility") || !planUsesConditionedRelation(*topics, "topics", "state")) {
		t.Fatalf("%s %s nonstaff plan lost visibility or hidden-topic authorization: %s", mode, query, encoded)
	}
	if requireGroupSubplan {
		groupMembership := findPlanNode(area, func(node *explainPlanNode) bool {
			return node.RelationName == "area_groups" && strings.Contains(node.Filter+node.RecheckCond+node.IndexCond, "group_id")
		})
		if groupMembership == nil || groupMembership.Parent != "SubPlan" {
			t.Fatalf("%s %s plan lost non-multiplying group-membership subplan: %s", mode, query, encoded)
		}
	}
	if !planUsesConditionedRelation(root, "posts", "author_id") || findPlanNode(&root, func(node *explainPlanNode) bool { return node.RelationName == "topic_reads" }) == nil {
		t.Fatalf("%s %s plan lost eligible-post or marker read: %s", mode, query, encoded)
	}
	if requirePostIndex && !planUsesIndex(root, "posts_topic_unread_visible_idx") {
		t.Fatalf("%s %s plan lost unread visible-post index: %s", mode, query, encoded)
	}
}

func planUsesConditionedRelation(node explainPlanNode, relation, condition string) bool {
	return findPlanNode(&node, func(candidate *explainPlanNode) bool {
		return candidate.RelationName == relation && strings.Contains(candidate.Filter+candidate.RecheckCond+candidate.IndexCond, condition)
	}) != nil
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
	planRowsBounded := candidate.PlanRows > 0 && candidate.PlanRows <= 51
	if expectedRows == 51 {
		planRowsBounded = candidate.PlanRows == 51
	}
	if !planRowsBounded || candidate.ActualRows != expectedRows {
		t.Fatalf("%s %s candidate fence = plan_rows=%d actual_rows=%d, want bounded/%d: %s", mode, shape, candidate.PlanRows, candidate.ActualRows, expectedRows, encoded)
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
	unread := os.Getenv("GOTTH_BB_RUN_AN03_READ_PLAN_EVIDENCE") == "1"
	if boolCount(checkpoint, admission, unread) > 1 {
		t.Fatal("set only one discovery evidence mode")
	}
	if !checkpoint && !admission && !unread {
		t.Skip("set exactly one plan evidence mode")
	}
	if admission {
		return discoveryPlanPopulation{database: discoveryAdmissionTestDatabase, topics: 100_000, posts: 1_000_000, postsPerTopic: 10, postIDOffset: 1_000_000, timeout: 20 * time.Minute, admission: true}
	}
	if unread {
		return discoveryPlanPopulation{database: unreadPlanTestDatabase, topics: 25_000, posts: 250_000, postsPerTopic: 10, postIDOffset: 1_000_000, timeout: 10 * time.Minute, unread: true}
	}
	return discoveryPlanPopulation{database: discoveryPlanTestDatabase, topics: 25_000, posts: 25_000, postsPerTopic: 1, postIDOffset: 100_000, timeout: 3 * time.Minute}
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func migrationPrefix(t *testing.T, count int) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(migrations.Files(), ".")
	if err != nil || count < 0 || count > len(entries) {
		t.Fatalf("read migration prefix: count=%d entries=%d error=%v", count, len(entries), err)
	}
	prefix := fstest.MapFS{}
	for _, entry := range entries[:count] {
		body, readErr := fs.ReadFile(migrations.Files(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		prefix[entry.Name()] = &fstest.MapFile{Data: body}
	}
	return prefix
}

func runUnreadMigrationEvidence(t *testing.T, ctx context.Context, configured *pgx.ConnConfig, observer *pgx.Conn) {
	t.Helper()
	blocker, err := pgx.ConnectConfig(ctx, configured.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close(context.Background()) }()
	block, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := block.Exec(ctx, `LOCK TABLE public.posts IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var postsBytes, markerBytes, blocksReadBefore, blocksHitBefore, tempBytesBefore int64
	if err := observer.QueryRow(ctx, `SELECT
    pg_total_relation_size('public.posts'), pg_total_relation_size('public.topic_reads'),
    blks_read, blks_hit, temp_bytes
FROM pg_stat_database WHERE datname = current_database()`).Scan(&postsBytes, &markerBytes, &blocksReadBefore, &blocksHitBefore, &tempBytesBefore); err != nil {
		t.Fatal(err)
	}
	migrationConfig := configured.Copy()
	if migrationConfig.RuntimeParams == nil {
		migrationConfig.RuntimeParams = make(map[string]string)
	}
	migrationConfig.RuntimeParams["application_name"] = "gotth_bb_an03_01_migration_evidence"
	started := time.Now()
	result := make(chan error, 1)
	go func() { result <- migration.Apply(ctx, migrationConfig, migrations.Files()) }()
	waitObserved := false
	lockMode := ""
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !waitObserved; {
		if err := observer.QueryRow(ctx, `SELECT lock_row.mode
FROM pg_catalog.pg_locks AS lock_row
JOIN pg_catalog.pg_stat_activity AS activity ON activity.pid = lock_row.pid
WHERE activity.application_name = 'gotth_bb_an03_01_migration_evidence'
  AND lock_row.relation = 'public.posts'::regclass
  AND NOT lock_row.granted
LIMIT 1`).Scan(&lockMode); err == nil {
			waitObserved = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := block.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if migrationErr := <-result; migrationErr != nil {
		t.Fatal(migrationErr)
	}
	if !waitObserved || lockMode != "ShareLock" {
		t.Fatalf("unread migration lock wait = observed %t mode %q, want ShareLock wait", waitObserved, lockMode)
	}
	var migrationHead, markerCount int64
	var indexValid, constraintValid bool
	var blocksReadAfter, blocksHitAfter, tempBytesAfter int64
	if err := observer.QueryRow(ctx, `SELECT
    (SELECT max(version) FROM public.gotth_schema_migrations),
    (SELECT count(*) FROM public.topic_reads),
    (SELECT indisvalid FROM pg_catalog.pg_index WHERE indexrelid = 'public.posts_topic_unread_visible_idx'::regclass),
    (SELECT convalidated FROM pg_catalog.pg_constraint WHERE conrelid = 'public.topic_reads'::regclass AND conname = 'topic_reads_read_at_finite'),
    blks_read, blks_hit, temp_bytes
FROM pg_stat_database WHERE datname = current_database()`).Scan(
		&migrationHead, &markerCount, &indexValid, &constraintValid, &blocksReadAfter, &blocksHitAfter, &tempBytesAfter,
	); err != nil {
		t.Fatal(err)
	}
	if migrationHead != 9 || markerCount == 0 || !indexValid || !constraintValid {
		t.Fatalf("unread migration result = head=%d markers=%d index=%t constraint=%t", migrationHead, markerCount, indexValid, constraintValid)
	}
	t.Logf("MIGRATION unread elapsed=%s posts_bytes=%d topic_reads_bytes=%d lock_mode=%s wait_observed=%t blocks_read_delta=%d blocks_hit_delta=%d temp_bytes_delta=%d markers=%d",
		time.Since(started), postsBytes, markerBytes, lockMode, waitObserved,
		blocksReadAfter-blocksReadBefore, blocksHitAfter-blocksHitBefore, tempBytesAfter-tempBytesBefore, markerCount)
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
