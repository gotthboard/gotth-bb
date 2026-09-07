//go:build integration

package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const discoveryPlanTestDatabase = "gotth_bb_an02_02_plan_test"

func TestDiscoveryPlansOnPostgreSQL17(t *testing.T) {
	if os.Getenv("GOTTH_BB_RUN_DISCOVERY_PLAN_EVIDENCE") != "1" {
		t.Skip("set GOTTH_BB_RUN_DISCOVERY_PLAN_EVIDENCE=1 for population/plan evidence")
	}
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+discoveryPlanTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+discoveryPlanTestDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+discoveryPlanTestDatabase+" WITH (FORCE)")
	})
	configured := adminConfig.Copy()
	configured.Database = discoveryPlanTestDatabase
	if err := migration.Apply(ctx, configured, migrations.Files()); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.ConnectConfig(ctx, configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	var ownerID, authorID, groupID, publicAreaID, groupAreaID int64
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
SELECT series, CASE WHEN series % 2 = 0 THEN $1::bigint ELSE $2::bigint END,
       CASE WHEN series % 100 = 0 THEN $3::bigint ELSE $4::bigint END,
       CASE WHEN series % 997 = 0 THEN 'rareterm' ELSE 'common term' END,
       'open', 100000 + series, 100000 + series, 0, 2,
       '2026-01-01T00:00:00Z'::timestamptz + series * interval '1 microsecond',
       '2026-01-01T00:00:00Z'::timestamptz + series * interval '1 microsecond',
       '2026-01-01T00:00:00Z'::timestamptz + series * interval '1 microsecond',
       to_tsvector('pg_catalog.simple'::regconfig, CASE WHEN series % 997 = 0 THEN 'rareterm' ELSE 'common term' END),
       'search-v1-pg17-simple-u15-p2'
FROM generate_series(1, 25000) AS series`, publicAreaID, groupAreaID, authorID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.posts
    (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, revision, created_at, updated_at, thread_path, search_vector, search_projection_version)
OVERRIDING SYSTEM VALUE
SELECT 100000 + series, series, CASE WHEN series % 100 = 0 THEN $1::bigint ELSE $2::bigint END,
       1, 'common body', '<p>common body</p>', 'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2', 1,
       '2026-01-01T00:00:00Z'::timestamptz + series * interval '1 microsecond',
       '2026-01-01T00:00:00Z'::timestamptz + series * interval '1 microsecond',
       ARRAY[1]::integer[], to_tsvector('pg_catalog.simple'::regconfig, 'common body'), 'search-v1-pg17-simple-u15-p2'
FROM generate_series(1, 25000) AS series`, authorID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `SET session_replication_role = origin; ANALYZE public.topics; ANALYZE public.posts; ANALYZE public.areas; ANALYZE public.area_groups`); err != nil {
		t.Fatal(err)
	}
	t.Logf("population rows topics=25000 posts=25000 duration=%s", time.Since(populationStart))

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
	activityArgs := fmt.Sprintf("false,true,ARRAY[%d]::bigint[],'2026-01-01T00:00:00.025000Z',125000", groupID)
	directArgs := "100002,false,false,ARRAY[]::bigint[]"
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		for _, shape := range searchShapes {
			searchPlan := explainPrepared(t, ctx, connection, "an02_search", "integer,boolean,boolean,bigint[],boolean,text,bigint,text,boolean,timestamptz,boolean,boolean,timestamptz", searchDiscoveryPage, shape.arguments, mode)
			if !strings.Contains(searchPlan, `"Node Type":"Limit"`) || !strings.Contains(searchPlan, `"Relation Name":"areas"`) || !strings.Contains(searchPlan, `"Relation Name":"topics"`) || !strings.Contains(searchPlan, `"Relation Name":"posts"`) {
				t.Fatalf("%s %s search plan lost bounded authorized relation tree: %s", mode, shape.name, searchPlan)
			}
			if mode == "force_custom_plan" && (shape.name == "current-vector-author" || shape.name == "author") &&
				(!strings.Contains(searchPlan, `"Index Name":"topics_search_author_current_idx"`) || !strings.Contains(searchPlan, `"Index Name":"posts_search_author_current_idx"`)) {
				t.Fatalf("%s %s search plan lost author indexes: %s", mode, shape.name, searchPlan)
			}
			if mode == "force_custom_plan" && shape.name == "rare-term" && !strings.Contains(searchPlan, `"Index Name":"topics_search_vector_current_idx"`) {
				t.Fatalf("%s rare-term search plan lost GIN index: %s", mode, searchPlan)
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
