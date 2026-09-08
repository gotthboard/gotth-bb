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

const unreadAdmissionTestDatabase = "gotth_bb_an03_04_admission_test"

const administrationAdmissionTestDatabase = "gotth_bb_an04_04_admission_test"

type discoveryPlanPopulation struct {
	database       string
	topics         int64
	posts          int64
	postsPerTopic  int64
	postIDOffset   int64
	timeout        time.Duration
	admission      bool
	unread         bool
	administration bool
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
	if population.unread && !population.administration {
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
	if population.administration {
		populateAdministrationCheckpoint(t, ctx, connection, ownerID, authorID, groupID)
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
	if population.unread {
		if _, err := connection.Exec(ctx, `UPDATE public.posts
SET author_id = $1
WHERE topic_id = 3
   OR (topic_id = 6 AND post_number = $2)`, readerID, population.postsPerTopic); err != nil {
			t.Fatal(err)
		}
	}
	if population.administration {
		populateAdministrationReportsAndAudit(t, ctx, connection, ownerID, authorID, population.postIDOffset)
		verifyAdministrationCheckpoint(t, ctx, connection, ownerID)
		logAdministrationResourceSnapshot(t, ctx, connection, "administration-populated")
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
		if !population.administration {
			runUnreadMigrationEvidence(t, ctx, configured, connection)
		}
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
	if population.unread {
		var ownOnly, newestOwn, olderOther int64
		if err := connection.QueryRow(ctx, `SELECT
    count(*) FILTER (WHERE topic_id = 3 AND author_id = $1),
    count(*) FILTER (WHERE topic_id = 6 AND post_number = $2 AND author_id = $1),
    count(*) FILTER (WHERE topic_id = 6 AND post_number < $2 AND author_id <> $1)
FROM public.posts
WHERE topic_id IN (3, 6)`, readerID, population.postsPerTopic).Scan(&ownOnly, &newestOwn, &olderOther); err != nil {
			t.Fatal(err)
		}
		if ownOnly != population.postsPerTopic || newestOwn != 1 || olderOther != population.postsPerTopic-1 {
			t.Fatalf("unread actor-exclusion population own_only=%d newest_own=%d older_other=%d", ownOnly, newestOwn, olderOther)
		}
		t.Logf("POPULATION unread_actor_exclusion own_only_topic=3 posts=%d newest_own_topic=6 newest_own=%d older_other=%d", ownOnly, newestOwn, olderOther)
	}
	t.Logf("population rows topics=%d posts=%d visibility_public/authenticated/groups=%d/%d/%d hidden_topics=%d deleted_topics=%d deleted_posts=%d redacted_posts=%d rare_topics/posts=%d/%d activity_timestamps=%d duration=%s", topicRows, postRows, publicTopics, authenticatedTopics, groupTopics, hiddenTopics, deletedTopics, deletedPosts, redactedPosts, rareTopics, rarePosts, activityTimestamps, time.Since(populationStart))
	logDiscoveryResourceSnapshot(t, ctx, connection, "populated")
	deepUnreadTopicID, deepUnreadPostIDOffset := int64(0), int64(0)
	if population.admission && population.unread {
		deepUnreadTopicID = population.topics + 1
		deepUnreadPostIDOffset = population.postIDOffset + population.posts
		populateDeepUnreadTopic(t, ctx, connection, deepUnreadTopicID, deepUnreadPostIDOffset, groupAreaID, ownerID)
		logDiscoveryResourceSnapshot(t, ctx, connection, "deep-unread-populated")
	}

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
			if population.admission {
				runFirstUnreadPlanEvidence(t, ctx, connection, mode, deepUnreadTopicID, deepUnreadPostIDOffset, readerID, groupID)
			}
		}
		if population.administration {
			runAdministrationPlanEvidence(t, ctx, connection, mode, ownerID, authorID, groupAreaID)
		}
	}
	if population.admission {
		runDiscoveryCoexistenceEvidence(t, ctx, configured, connection, publicAreaID, ownerID, readerID, groupID, population)
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

func requireFirstUnreadAuthorizationPlan(t *testing.T, mode, boundary, encoded string, wantBoundedRows int64) {
	t.Helper()
	var document explainPlanDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil || len(document) != 1 {
		t.Fatalf("%s %s decode first-unread plan: documents=%d error=%v", mode, boundary, len(document), err)
	}
	root := document[0].Plan
	authorized := findPlanNode(&root, func(node *explainPlanNode) bool { return node.SubplanName == "CTE authorized_topic" })
	state := findPlanNode(&root, func(node *explainPlanNode) bool { return node.SubplanName == "CTE topic_state" })
	target := findPlanNode(&root, func(node *explainPlanNode) bool { return node.SubplanName == "CTE target" })
	bounded := findPlanNode(&root, func(node *explainPlanNode) bool { return node.SubplanName == "CTE bounded_thread" })
	if authorized == nil || state == nil || target == nil || bounded == nil ||
		!planUsesConditionedRelation(*authorized, "areas", "visibility") ||
		!planUsesConditionedRelation(*authorized, "topics", "deleted_at") ||
		!planUsesConditionedRelation(*authorized, "topics", "state") {
		t.Fatalf("%s %s first-unread plan lost authorization fence: %s", mode, boundary, encoded)
	}
	groupMembership := findPlanNode(authorized, func(node *explainPlanNode) bool {
		return node.RelationName == "area_groups" && strings.Contains(node.Filter+node.RecheckCond+node.IndexCond, "group_id")
	})
	if groupMembership == nil || groupMembership.Parent != "SubPlan" {
		t.Fatalf("%s %s first-unread plan lost non-multiplying group authorization: %s", mode, boundary, encoded)
	}
	if findPlanNode(state, func(node *explainPlanNode) bool { return node.RelationName == "topic_reads" }) == nil ||
		!planUsesConditionedRelation(*state, "posts", "author_id") || !planUsesIndex(*state, "posts_topic_unread_visible_idx") ||
		!planUsesConditionedRelation(*target, "posts", "author_id") || !planUsesIndex(*target, "posts_topic_unread_visible_idx") {
		t.Fatalf("%s %s first-unread plan lost marker or actor-excluding indexed target work: %s", mode, boundary, encoded)
	}
	if bounded.NodeType != "Limit" || bounded.ActualRows != wantBoundedRows || bounded.PlanRows <= 0 || bounded.PlanRows > 250001 {
		t.Fatalf("%s %s first-unread bounded tree = type %q plan_rows %d actual_rows %d, want Limit/<=250001/%d: %s",
			mode, boundary, bounded.NodeType, bounded.PlanRows, bounded.ActualRows, wantBoundedRows, encoded)
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
	ActualLoops  int64             `json:"Actual Loops"`
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
		if !planUsesConditionedRelation(*candidate, required.relation, required.filter) {
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

func populateAdministrationCheckpoint(t *testing.T, ctx context.Context, connection *pgx.Conn, ownerID, targetID, firstGroupID int64) {
	t.Helper()
	started := time.Now()
	if _, err := connection.Exec(ctx, `INSERT INTO public.users
    (display_name, role, suspended_at, suspended_until, suspension_reason, created_at, updated_at, last_login_at)
SELECT 'Admission Account ' || value,
       CASE value % 100 WHEN 0 THEN 'moderator' WHEN 1 THEN 'administrator' ELSE 'member' END,
       CASE WHEN value % 127 = 0 THEN '2026-01-01T00:00:00Z'::timestamptz END,
       CASE WHEN value % 127 = 0 THEN '2027-01-01T00:00:00Z'::timestamptz END,
       CASE WHEN value % 127 = 0 THEN 'admission suspension' END,
       '2025-01-01T00:00:00Z'::timestamptz,
       '2025-01-01T00:00:00Z'::timestamptz,
       '2025-01-01T00:00:00Z'::timestamptz
FROM generate_series(1, 24997) AS value`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.forum_groups (name, created_by)
SELECT 'Admission Group ' || value, $1
FROM generate_series(1, 24999) AS value`, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.forum_group_members (group_id, user_id, granted_by)
SELECT id, $1, $2 FROM public.forum_groups WHERE id % 2 = 0`, targetID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.areas
    (slug, name, description, display_order, visibility, posting_mode, created_by, updated_by)
SELECT 'admission-area-' || value,
       'Admission Area ' || value,
       'Representative AN-04 administration checkpoint',
       value + 3,
       CASE value % 3 WHEN 0 THEN 'public' WHEN 1 THEN 'authenticated' ELSE 'groups' END,
       CASE value % 5 WHEN 0 THEN 'archived' WHEN 1 THEN 'read_only' ELSE 'normal' END,
       $1, $1
FROM generate_series(1, 24997) AS value`, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by)
SELECT area.id, $1, $2
FROM public.areas AS area
WHERE area.visibility = 'groups'
  AND NOT EXISTS (SELECT 1 FROM public.area_groups AS mapping WHERE mapping.area_id = area.id)`, firstGroupID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `ANALYZE public.users; ANALYZE public.forum_groups; ANALYZE public.forum_group_members; ANALYZE public.areas; ANALYZE public.area_groups`); err != nil {
		t.Fatal(err)
	}
	var users, groups, areas, mappings int64
	if err := connection.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM public.users),
    (SELECT count(*) FROM public.forum_groups),
    (SELECT count(*) FROM public.areas),
    (SELECT count(*) FROM public.area_groups)`).Scan(&users, &groups, &areas, &mappings); err != nil {
		t.Fatal(err)
	}
	if users != 25_000 || groups != 25_000 || areas != 25_000 || mappings == 0 {
		t.Fatalf("administration checkpoint identities users=%d groups=%d areas=%d mappings=%d", users, groups, areas, mappings)
	}
	t.Logf("POPULATION administration users=%d groups=%d areas=%d mappings=%d duration=%s", users, groups, areas, mappings, time.Since(started))
}

func populateAdministrationReportsAndAudit(t *testing.T, ctx context.Context, connection *pgx.Conn, ownerID, targetID, postIDOffset int64) {
	t.Helper()
	if _, err := connection.Exec(ctx, `INSERT INTO public.reports
    (reported_by, topic_id, post_id, user_id, reason, status, assigned_to, resolution, resolved_by, created_at, updated_at, resolved_at)
VALUES
    ($1, 1, NULL, NULL, 'open admission report', 'open', NULL, NULL, NULL, '2026-01-01T00:00:03Z', '2026-01-01T00:00:03Z', NULL),
    ($1, NULL, $3 + 2, NULL, 'review admission report', 'in_review', $1, NULL, NULL, '2026-01-01T00:00:04Z', '2026-01-01T00:00:04Z', NULL),
    ($1, NULL, NULL, $2, 'resolved admission report', 'resolved', $1, 'resolved', $1, '2026-01-01T00:00:05Z', '2026-01-01T00:00:06Z', '2026-01-01T00:00:06Z'),
    ($1, 2, NULL, NULL, 'dismissed admission report', 'dismissed', $1, 'dismissed', $1, '2026-01-01T00:00:07Z', '2026-01-01T00:00:08Z', '2026-01-01T00:00:08Z')`, ownerID, targetID, postIDOffset); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.moderation_actions
    (actor_kind, actor_user_id, target_type, target_report_id, action_type, reason, previous_state, resulting_state, request_id, created_at)
SELECT 'forum_user', $1, 'report', report.id,
       CASE report.status WHEN 'in_review' THEN 'assign_report' WHEN 'resolved' THEN 'resolve_report' ELSE 'dismiss_report' END,
       CASE report.status WHEN 'in_review' THEN NULL ELSE report.status END,
       jsonb_build_object('status', 'open'), jsonb_build_object('status', report.status),
       ('00000000-0000-4000-8000-' || lpad(report.id::text, 12, '0'))::uuid,
       report.updated_at
FROM public.reports AS report
WHERE report.status <> 'open'`, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `ANALYZE public.reports; ANALYZE public.moderation_actions`); err != nil {
		t.Fatal(err)
	}
}

func verifyAdministrationCheckpoint(t *testing.T, ctx context.Context, connection *pgx.Conn, ownerID int64) {
	t.Helper()
	dashboard, err := New(connection).LoadAdministrationDashboard(ctx, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if !dashboard.ActorPresent || dashboard.UsersTotal != 25_000 ||
		dashboard.OpenReports != 1 || dashboard.InReviewReports != 1 || dashboard.Members+dashboard.Moderators+dashboard.Administrators != dashboard.UsersTotal {
		t.Fatalf("administration dashboard checkpoint = %+v", dashboard)
	}
	var expectedTopics, expectedPosts int64
	if err := connection.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM public.topics WHERE deleted_at IS NULL),
    (SELECT count(*) FROM public.posts WHERE deleted_at IS NULL AND redacted_at IS NULL)`).Scan(&expectedTopics, &expectedPosts); err != nil {
		t.Fatal(err)
	}
	if dashboard.Topics != expectedTopics || dashboard.Posts != expectedPosts {
		t.Fatalf("administration dashboard content topics/posts=%d/%d want=%d/%d", dashboard.Topics, dashboard.Posts, expectedTopics, expectedPosts)
	}
	var states, audits int64
	if err := connection.QueryRow(ctx, `SELECT
    (SELECT count(DISTINCT status) FROM public.reports),
    (SELECT count(*) FROM public.moderation_actions WHERE target_type = 'report')`).Scan(&states, &audits); err != nil {
		t.Fatal(err)
	}
	if states != 4 || audits != 3 {
		t.Fatalf("administration report checkpoint states=%d audits=%d", states, audits)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(connection).LoadAdministrationDashboard(canceled, ownerID); err == nil {
		t.Fatal("canceled administration dashboard returned no error")
	}
	t.Logf("CHECKPOINT administration dashboard=%+v report_states=%d report_audits=%d cancellation=pass", dashboard, states, audits)
}

func runAdministrationPlanEvidence(t *testing.T, ctx context.Context, connection *pgx.Conn, mode string, ownerID, targetID, areaID int64) {
	t.Helper()
	observedAt := "2026-09-08T16:00:00Z"
	accountPlan := explainPrepared(t, ctx, connection, "an04_accounts", "timestamptz,bigint,bigint,integer", listAccountsForAdministration,
		fmt.Sprintf("'%s',%d,0,51", observedAt, ownerID), mode)
	requireAdministrationPlan(t, mode, "accounts", accountPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`, `"Actual Rows":51`})
	deniedAccountPlan := explainPrepared(t, ctx, connection, "an04_accounts_denied", "timestamptz,bigint,bigint,integer", listAccountsForAdministration,
		fmt.Sprintf("'%s',%d,0,51", observedAt, targetID), mode)
	requireDeniedAdministrationPlan(t, mode, "accounts", deniedAccountPlan, "users")
	detailPlan := explainPrepared(t, ctx, connection, "an04_account_detail", "timestamptz,bigint,bigint", loadAccountForAdministration,
		fmt.Sprintf("'%s',%d,%d", observedAt, ownerID, targetID), mode)
	requireAdministrationPlan(t, mode, "account-detail", detailPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`})
	deniedDetailPlan := explainPrepared(t, ctx, connection, "an04_account_detail_denied", "timestamptz,bigint,bigint", loadAccountForAdministration,
		fmt.Sprintf("'%s',%d,%d", observedAt, targetID, ownerID), mode)
	requireDeniedAdministrationPlan(t, mode, "account-detail", deniedDetailPlan, "users")
	groupsPlan := explainPrepared(t, ctx, connection, "an04_groups", "bigint,timestamptz,bigint,integer", listGroupsForAdministration,
		fmt.Sprintf("%d,'%s',0,51", ownerID, observedAt), mode)
	requireAdministrationPlan(t, mode, "groups", groupsPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"forum_groups_pkey"`, `"Actual Rows":51`})
	deniedGroupsPlan := explainPrepared(t, ctx, connection, "an04_groups_denied", "bigint,timestamptz,bigint,integer", listGroupsForAdministration,
		fmt.Sprintf("%d,'%s',0,51", targetID, observedAt), mode)
	requireDeniedAdministrationPlan(t, mode, "groups", deniedGroupsPlan, "forum_groups")
	membershipPlan := explainPrepared(t, ctx, connection, "an04_account_groups", "bigint,timestamptz,bigint,bigint,integer", listAccountGroupsForAdministration,
		fmt.Sprintf("%d,'%s',%d,0,51", ownerID, observedAt, targetID), mode)
	requireAdministrationPlan(t, mode, "account-groups", membershipPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"forum_groups_pkey"`, `"Index Name":"forum_group_members_user_group_idx"`, `"Actual Loops":51`, `"Actual Rows":51`})
	deniedMembershipPlan := explainPrepared(t, ctx, connection, "an04_account_groups_denied", "bigint,timestamptz,bigint,bigint,integer", listAccountGroupsForAdministration,
		fmt.Sprintf("%d,'%s',%d,0,51", targetID, observedAt, ownerID), mode)
	requireDeniedAdministrationPlan(t, mode, "account-groups", deniedMembershipPlan, "users", "forum_groups", "forum_group_members")
	areaPlan := explainPrepared(t, ctx, connection, "an04_areas", "bigint,timestamptz,integer,bigint,integer", listAreasForAdministrationPage,
		fmt.Sprintf("%d,'%s',0,0,51", ownerID, observedAt), mode)
	requireAdministrationPlan(t, mode, "areas", areaPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`, `"Index Name":"areas_display_idx"`, `"Actual Rows":51`})
	deniedAreaPlan := explainPrepared(t, ctx, connection, "an04_areas_denied", "bigint,timestamptz,integer,bigint,integer", listAreasForAdministrationPage,
		fmt.Sprintf("%d,'%s',0,0,51", targetID, observedAt), mode)
	requireDeniedAdministrationPlan(t, mode, "areas", deniedAreaPlan, "areas")
	areaDetailPlan := explainPrepared(t, ctx, connection, "an04_area_detail", "bigint,timestamptz,bigint", loadAreaForAdministrationPage,
		fmt.Sprintf("%d,'%s',%d", ownerID, observedAt, areaID), mode)
	requireAdministrationPlan(t, mode, "area-detail", areaDetailPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`, `"Index Name":"areas_pkey"`})
	deniedAreaDetailPlan := explainPrepared(t, ctx, connection, "an04_area_detail_denied", "bigint,timestamptz,bigint", loadAreaForAdministrationPage,
		fmt.Sprintf("%d,'%s',%d", targetID, observedAt, areaID), mode)
	requireDeniedAdministrationPlan(t, mode, "area-detail", deniedAreaDetailPlan, "areas")
	areaGroupsPlan := explainPrepared(t, ctx, connection, "an04_area_groups", "bigint,timestamptz,bigint,bigint,integer", listAreaGroupsForAdministrationPage,
		fmt.Sprintf("%d,'%s',%d,0,51", ownerID, observedAt, areaID), mode)
	requireAdministrationPlan(t, mode, "area-groups", areaGroupsPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"forum_groups_pkey"`, `"Index Name":"area_groups_pkey"`, `area_id =`, `"Actual Rows":51`})
	deniedAreaGroupsPlan := explainPrepared(t, ctx, connection, "an04_area_groups_denied", "bigint,timestamptz,bigint,bigint,integer", listAreaGroupsForAdministrationPage,
		fmt.Sprintf("%d,'%s',%d,0,51", targetID, observedAt, areaID), mode)
	requireDeniedAdministrationPlan(t, mode, "area-groups", deniedAreaGroupsPlan, "areas", "forum_groups", "area_groups")
	dashboardPlan := explainPrepared(t, ctx, connection, "an04_dashboard", "bigint", loadAdministrationDashboard, fmt.Sprintf("%d", ownerID), mode)
	requireAdministrationPlan(t, mode, "dashboard", dashboardPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`, `"Relation Name":"topics"`, `"Relation Name":"posts"`, `"Relation Name":"reports"`})
	deniedDashboardPlan := explainPrepared(t, ctx, connection, "an04_dashboard_denied", "bigint", loadAdministrationDashboard, fmt.Sprintf("%d", targetID), mode)
	requireDeniedAdministrationPlan(t, mode, "dashboard", deniedDashboardPlan, "users", "topics", "posts", "reports")
}

func logAdministrationResourceSnapshot(t *testing.T, ctx context.Context, connection *pgx.Conn, label string) {
	t.Helper()
	var usersBytes, groupsBytes, areasBytes, reportsBytes, auditsBytes int64
	if err := connection.QueryRow(ctx, `SELECT
    pg_total_relation_size('public.users'),
    pg_total_relation_size('public.forum_groups'),
    pg_total_relation_size('public.areas'),
    pg_total_relation_size('public.reports'),
    pg_total_relation_size('public.moderation_actions')`).Scan(&usersBytes, &groupsBytes, &areasBytes, &reportsBytes, &auditsBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("RESOURCE label=%s users_bytes=%d groups_bytes=%d areas_bytes=%d reports_bytes=%d audits_bytes=%d", label, usersBytes, groupsBytes, areasBytes, reportsBytes, auditsBytes)
}

func requestedDiscoveryPlanPopulation(t *testing.T) discoveryPlanPopulation {
	t.Helper()
	checkpoint := os.Getenv("GOTTH_BB_RUN_DISCOVERY_PLAN_EVIDENCE") == "1"
	admission := os.Getenv("GOTTH_BB_RUN_AN02_ADMISSION_EVIDENCE") == "1"
	unread := os.Getenv("GOTTH_BB_RUN_AN03_READ_PLAN_EVIDENCE") == "1"
	unreadAdmission := os.Getenv("GOTTH_BB_RUN_AN03_ADMISSION_EVIDENCE") == "1"
	administrationAdmission := os.Getenv("GOTTH_BB_RUN_AN04_ADMISSION_EVIDENCE") == "1"
	if boolCount(checkpoint, admission, unread, unreadAdmission, administrationAdmission) > 1 {
		t.Fatal("set only one discovery evidence mode")
	}
	if !checkpoint && !admission && !unread && !unreadAdmission && !administrationAdmission {
		t.Skip("set exactly one plan evidence mode")
	}
	if administrationAdmission {
		return discoveryPlanPopulation{database: administrationAdmissionTestDatabase, topics: 100_000, posts: 1_000_000, postsPerTopic: 10, postIDOffset: 1_000_000, timeout: 30 * time.Minute, admission: true, unread: true, administration: true}
	}
	if unreadAdmission {
		return discoveryPlanPopulation{database: unreadAdmissionTestDatabase, topics: 100_000, posts: 1_000_000, postsPerTopic: 10, postIDOffset: 1_000_000, timeout: 25 * time.Minute, admission: true, unread: true}
	}
	if admission {
		return discoveryPlanPopulation{database: discoveryAdmissionTestDatabase, topics: 100_000, posts: 1_000_000, postsPerTopic: 10, postIDOffset: 1_000_000, timeout: 20 * time.Minute, admission: true}
	}
	if unread {
		return discoveryPlanPopulation{database: unreadPlanTestDatabase, topics: 25_000, posts: 250_000, postsPerTopic: 10, postIDOffset: 1_000_000, timeout: 10 * time.Minute, unread: true}
	}
	return discoveryPlanPopulation{database: discoveryPlanTestDatabase, topics: 25_000, posts: 25_000, postsPerTopic: 1, postIDOffset: 100_000, timeout: 3 * time.Minute}
}

func populateDeepUnreadTopic(t *testing.T, ctx context.Context, connection *pgx.Conn, topicID, postIDOffset, areaID, authorID int64) {
	t.Helper()
	const nodes int64 = 250001
	started := time.Now()
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics
    (id, area_id, author_id, title, state, first_post_id, latest_post_id, reply_count, next_post_number,
     created_at, updated_at, last_activity_at, search_vector, search_projection_version)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, 'AN-03 deep unread boundary', 'open', $4, $5, $6, $7,
        '2025-12-31T00:00:00Z', '2025-12-31T00:00:00Z', '2025-12-31T00:00:00Z',
        to_tsvector('pg_catalog.simple'::regconfig, 'deep unread boundary'), 'search-v1-pg17-simple-u15-p2')`,
		topicID, areaID, authorID, postIDOffset+1, postIDOffset+nodes, nodes-1, nodes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.posts
    (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, revision,
     created_at, updated_at, parent_post_id, thread_path, search_vector, search_projection_version)
OVERRIDING SYSTEM VALUE
SELECT $1 + series, $2, $3, series::integer, 'deep body', '<p>deep body</p>',
       'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2', 1,
       '2025-12-31T00:00:00Z'::timestamptz + series * interval '1 microsecond',
       '2025-12-31T00:00:00Z'::timestamptz + series * interval '1 microsecond',
       CASE WHEN series = 1 THEN NULL WHEN series <= 32 THEN $1 + series - 1 ELSE $1 + 1 END,
       CASE
           WHEN series = 1 THEN ARRAY[1]::integer[]
           WHEN series <= 32 THEN ARRAY(SELECT value::integer FROM generate_series(1, series) AS value)
           ELSE ARRAY[1, series::integer]
       END,
       to_tsvector('pg_catalog.simple'::regconfig, 'deep body'), 'search-v1-pg17-simple-u15-p2'
FROM generate_series(1, $4) AS series`, postIDOffset, topicID, authorID, nodes); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT
setval(pg_get_serial_sequence('public.topics', 'id'), $1, true),
setval(pg_get_serial_sequence('public.posts', 'id'), $2, true)`, topicID, postIDOffset+nodes); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `ANALYZE public.topics; ANALYZE public.posts`); err != nil {
		t.Fatal(err)
	}
	var topicNodes, maxDepth int64
	if err := connection.QueryRow(ctx, `SELECT count(*), max(cardinality(thread_path)) FROM public.posts WHERE topic_id = $1`, topicID).Scan(&topicNodes, &maxDepth); err != nil || topicNodes != nodes || maxDepth != 32 {
		t.Fatalf("deep unread population nodes=%d max_depth=%d error=%v", topicNodes, maxDepth, err)
	}
	t.Logf("POPULATION deep_unread_topic=%d nodes=%d max_depth=%d post_id_range=%d..%d duration=%s", topicID, nodes, maxDepth, postIDOffset+1, postIDOffset+nodes, time.Since(started))
}

func runFirstUnreadPlanEvidence(t *testing.T, ctx context.Context, connection *pgx.Conn, mode string, topicID, postIDOffset, readerID, groupID int64) {
	t.Helper()
	var indexDefinition string
	if err := connection.QueryRow(ctx, `SELECT pg_get_indexdef('public.posts_topic_unread_visible_idx'::regclass)`).Scan(&indexDefinition); err != nil || !strings.Contains(indexDefinition, "INCLUDE (author_id)") {
		t.Fatalf("unread index payload definition=%q error=%v", indexDefinition, err)
	}
	boundaries := []struct {
		name            string
		marker          int32
		targetOrdinal   int64
		wantBoundedRows int64
	}{
		{name: "target-1", targetOrdinal: 1, wantBoundedRows: 250001},
		{name: "target-25", marker: 24, targetOrdinal: 25, wantBoundedRows: 250001},
		{name: "target-26", marker: 25, targetOrdinal: 26, wantBoundedRows: 250001},
		{name: "target-250000", marker: 249999, targetOrdinal: 250000, wantBoundedRows: 250001},
		{name: "target-250001", marker: 250000, targetOrdinal: 250001, wantBoundedRows: 250001},
		{name: "no-target", marker: 250001},
	}
	for _, boundary := range boundaries {
		if boundary.marker == 0 {
			if _, err := connection.Exec(ctx, `DELETE FROM public.topic_reads WHERE user_id = $1 AND topic_id = $2`, readerID, topicID); err != nil {
				t.Fatal(err)
			}
		} else if _, err := connection.Exec(ctx, `INSERT INTO public.topic_reads (user_id, topic_id, last_read_post_number, read_at)
VALUES ($1, $2, $3, '2026-01-01T00:00:02Z')
ON CONFLICT (user_id, topic_id) DO UPDATE SET last_read_post_number = EXCLUDED.last_read_post_number, read_at = EXCLUDED.read_at`, readerID, topicID, boundary.marker); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		row, err := New(connection).GetFirstUnreadTarget(ctx, GetFirstUnreadTargetParams{TopicID: topicID, GroupIds: []int64{groupID}, ActorUserID: readerID})
		if err != nil || row.TopicID != topicID || row.ReadHead != 250001 {
			t.Fatalf("%s %s first-unread result topic=%d head=%d error=%v", mode, boundary.name, row.TopicID, row.ReadHead, err)
		}
		if boundary.targetOrdinal == 0 {
			if row.TargetPostID.Valid || row.TargetPostNumber.Valid || row.TargetNodeOrdinal.Valid {
				t.Fatalf("%s %s unexpectedly returned target: %+v", mode, boundary.name, row)
			}
		} else if !row.TargetPostID.Valid || row.TargetPostID.Int64 != postIDOffset+boundary.targetOrdinal ||
			!row.TargetPostNumber.Valid || int64(row.TargetPostNumber.Int32) != boundary.targetOrdinal ||
			!row.TargetNodeOrdinal.Valid || row.TargetNodeOrdinal.Int64 != boundary.targetOrdinal {
			t.Fatalf("%s %s target mismatch: %+v", mode, boundary.name, row)
		}
		arguments := fmt.Sprintf("%d,false,ARRAY[%d]::bigint[],%d", topicID, groupID, readerID)
		plan := explainPrepared(t, ctx, connection, "an03_first_unread", "bigint,boolean,bigint[],bigint", getFirstUnreadTarget, arguments, mode)
		requireFirstUnreadAuthorizationPlan(t, mode, boundary.name, plan, boundary.wantBoundedRows)
		t.Logf("PLAN mode=%s actor=group query=first-unread boundary=%s duration=%s\n%s", mode, boundary.name, time.Since(started), plan)
	}
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

func runDiscoveryCoexistenceEvidence(t *testing.T, ctx context.Context, configured *pgx.ConnConfig, observer *pgx.Conn, publicAreaID, ownerID, readerID, groupID int64, population discoveryPlanPopulation) {
	t.Helper()
	var baselineConnections int64
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`).Scan(&baselineConnections); err != nil {
		t.Fatal(err)
	}
	searchParameters := SearchDiscoveryPageParams{
		IsMember: true, GroupIds: []int64{groupID}, HasQuery: true, ParsedQuery: "'common'", PageOffset: 0,
	}
	activityParameters := ListRecentActivityAfterParams{
		IsMember: true, GroupIds: []int64{groupID}, CursorCreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 1, 1, 0, 0, 0, 10_000_000, time.UTC), Valid: true}, CursorPostID: population.postIDOffset + population.posts + 1,
	}
	type coexistenceAction struct {
		name   string
		action func(context.Context, *pgx.Conn) error
	}
	actions := []coexistenceAction{
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
	if population.unread {
		actions = append(actions,
			coexistenceAction{name: "first-unread", action: func(ctx context.Context, connection *pgx.Conn) error {
				row, err := New(connection).GetFirstUnreadTarget(ctx, GetFirstUnreadTargetParams{
					TopicID: 2, GroupIds: []int64{groupID}, ActorUserID: readerID,
				})
				if err != nil {
					return err
				}
				targetPresent := row.TargetPostID.Valid && row.TargetPostNumber.Valid && row.TargetNodeOrdinal.Valid
				targetAbsent := !row.TargetPostID.Valid && !row.TargetPostNumber.Valid && !row.TargetNodeOrdinal.Valid
				markerPresent := row.LastReadPostNumber.Valid && row.ReadAt.Valid
				markerAbsent := !row.LastReadPostNumber.Valid && !row.ReadAt.Valid
				marker := int32(0)
				if markerPresent {
					marker = row.LastReadPostNumber.Int32
				}
				if row.TopicID != 2 || row.NextPostNumber < 2 || row.ReadHead <= 0 || row.ReadHead >= row.NextPostNumber ||
					(!targetPresent && !targetAbsent) || (!markerPresent && !markerAbsent) ||
					markerPresent && (marker <= 0 || marker >= row.NextPostNumber || row.ReadAt.InfinityModifier != pgtype.Finite) ||
					targetAbsent && row.ReadHead > marker ||
					targetPresent && (row.TargetPostID.Int64 <= 0 || row.TargetPostNumber.Int32 <= marker ||
						row.TargetPostNumber.Int32 > row.ReadHead || row.TargetNodeOrdinal.Int64 <= 0 || row.TargetNodeOrdinal.Int64 > 250001) {
					return fmt.Errorf("first-unread returned invalid state: %+v", row)
				}
				return nil
			}},
			coexistenceAction{name: "mark-read", action: func(ctx context.Context, connection *pgx.Conn) error {
				tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				queries := New(tx)
				if err := queries.ConfigureMarkTopicReadTransaction(ctx); err != nil {
					return err
				}
				row, err := queries.MarkTopicReadBoundary(ctx, MarkTopicReadBoundaryParams{
					TopicID: 2, GroupIds: []int64{groupID}, ActorUserID: readerID,
				})
				if err != nil {
					return err
				}
				if row.TopicID != 2 || row.SelectedPostNumber <= 0 || row.SelectedPostNumber >= row.NextPostNumber {
					return fmt.Errorf("mark-read returned invalid boundary: %+v", row)
				}
				return tx.Commit(ctx)
			}},
		)
	}
	connections := make([]*pgx.Conn, len(actions))
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
