//go:build integration

package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const areaAccessTestDatabase = "gotth_bb_alpha1_area_access_test"

func TestAreaVisibilityQueriesOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+areaAccessTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale area-access database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+areaAccessTestDatabase); err != nil {
		t.Fatalf("create area-access database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+areaAccessTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = areaAccessTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect area-access database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	var ownerID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Area Owner', 'administrator') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatalf("insert area owner: %v", err)
	}
	var matchingGroupID, otherGroupID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Matching', $1) RETURNING id`, ownerID).Scan(&matchingGroupID); err != nil {
		t.Fatalf("insert matching group: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Other', $1) RETURNING id`, ownerID).Scan(&otherGroupID); err != nil {
		t.Fatalf("insert other group: %v", err)
	}
	areaIDs := make(map[string]int64, 5)
	for _, area := range []struct {
		slug       string
		name       string
		order      int
		visibility string
		groupID    int64
	}{
		{slug: "public", name: "Public", order: 1, visibility: "public"},
		{slug: "members", name: "Members", order: 2, visibility: "authenticated"},
		{slug: "matching", name: "Matching", order: 3, visibility: "groups", groupID: matchingGroupID},
		{slug: "other", name: "Other", order: 4, visibility: "groups", groupID: otherGroupID},
		{slug: "staff-only", name: "Staff only", order: 5, visibility: "groups"},
	} {
		var areaID int64
		if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, display_order, visibility, created_by, updated_by) VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
			area.slug, area.name, area.order, area.visibility, ownerID).Scan(&areaID); err != nil {
			t.Fatalf("insert area %q: %v", area.slug, err)
		}
		areaIDs[area.slug] = areaID
		if area.groupID != 0 {
			if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by) VALUES ($1, $2, $3)`, areaID, area.groupID, ownerID); err != nil {
				t.Fatalf("map area %q: %v", area.slug, err)
			}
		}
	}
	fixture, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin area-summary fixture: %v", err)
	}
	fixtureTime := time.Date(2026, time.September, 2, 20, 0, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO public.topics
            (id, area_id, author_id, title, state, first_post_id, latest_post_id, reply_count, next_post_number, created_at, updated_at, last_activity_at)
            VALUES (101, $1, $2, 'Visible topic', 'open', 1001, 1003, 2, 4, $3, $3, $4)`, args: []any{areaIDs["public"], ownerID, fixtureTime, fixtureTime.Add(2 * time.Minute)}},
		{query: `INSERT INTO public.posts
            (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, created_at, updated_at)
            OVERRIDING SYSTEM VALUE VALUES
            (1001, 101, $1, 1, 'first', '<p>first</p>', 'test', $2, $2),
            (1002, 101, $1, 2, 'second', '<p>second</p>', 'test', $3, $3)`, args: []any{ownerID, fixtureTime, fixtureTime.Add(time.Minute)}},
		{query: `INSERT INTO public.posts
            (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, created_at, updated_at, deleted_at, deleted_by, deletion_reason)
            OVERRIDING SYSTEM VALUE VALUES
            (1003, 101, $1, 3, 'deleted', '<p>deleted</p>', 'test', $2, $2, $2, $1, 'fixture deletion')`, args: []any{ownerID, fixtureTime.Add(2 * time.Minute)}},
		{query: `INSERT INTO public.topics
            (id, area_id, author_id, title, state, first_post_id, latest_post_id, reply_count, next_post_number, created_at, updated_at, last_activity_at)
            VALUES (102, $1, $2, 'Hidden topic', 'hidden', 1004, 1004, 0, 2, $3, $3, $3)`, args: []any{areaIDs["public"], ownerID, fixtureTime.Add(3 * time.Minute)}},
		{query: `INSERT INTO public.posts
            (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, created_at, updated_at)
            OVERRIDING SYSTEM VALUE VALUES
            (1004, 102, $1, 1, 'hidden', '<p>hidden</p>', 'test', $2, $2)`, args: []any{ownerID, fixtureTime.Add(3 * time.Minute)}},
		{query: `INSERT INTO public.topics
            (id, area_id, author_id, title, state, first_post_id, latest_post_id, reply_count, next_post_number, created_at, updated_at, last_activity_at, deleted_at)
            VALUES (103, $1, $2, 'Deleted topic', 'open', 1005, 1005, 0, 2, $3, $3, $3, $3)`, args: []any{areaIDs["public"], ownerID, fixtureTime.Add(4 * time.Minute)}},
		{query: `INSERT INTO public.posts
            (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, created_at, updated_at)
            OVERRIDING SYSTEM VALUE VALUES
            (1005, 103, $1, 1, 'deleted topic', '<p>deleted topic</p>', 'test', $2, $2)`, args: []any{ownerID, fixtureTime.Add(4 * time.Minute)}},
	}
	for index, statement := range statements {
		if _, err := fixture.Exec(ctx, statement.query, statement.args...); err != nil {
			_ = fixture.Rollback(ctx)
			t.Fatalf("insert area-summary fixture %d: %v", index, err)
		}
	}
	if err := fixture.Commit(ctx); err != nil {
		t.Fatalf("commit area-summary fixture: %v", err)
	}
	queries := New(connection)
	for _, test := range []struct {
		name      string
		isStaff   bool
		isMember  bool
		groupIDs  []int64
		wantSlugs []string
	}{
		{name: "visitor", wantSlugs: []string{"public"}},
		{name: "visitor cannot inject group authority", groupIDs: []int64{matchingGroupID}, wantSlugs: []string{"public"}},
		{name: "member empty groups", isMember: true, groupIDs: []int64{}, wantSlugs: []string{"public", "members"}},
		{name: "matching member", isMember: true, groupIDs: []int64{matchingGroupID}, wantSlugs: []string{"public", "members", "matching"}},
		{name: "other member", isMember: true, groupIDs: []int64{otherGroupID}, wantSlugs: []string{"public", "members", "other"}},
		{name: "staff", isStaff: true, wantSlugs: []string{"public", "members", "matching", "other", "staff-only"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			areas, err := queries.ListVisibleAreas(ctx, ListVisibleAreasParams{
				IsStaff: test.isStaff, IsMember: test.isMember, GroupIds: test.groupIDs,
			})
			if err != nil || len(areas) != len(test.wantSlugs) {
				t.Fatalf("ListVisibleAreas() = (%+v, %v), want %v", areas, err, test.wantSlugs)
			}
			for index := range areas {
				if areas[index].Slug != test.wantSlugs[index] {
					t.Fatalf("area %d slug = %q, want %q", index, areas[index].Slug, test.wantSlugs[index])
				}
			}
			summaries, err := queries.ListVisibleAreaSummaries(ctx, ListVisibleAreaSummariesParams{
				IsStaff: test.isStaff, IsMember: test.isMember, GroupIds: test.groupIDs,
			})
			if err != nil || len(summaries) != len(test.wantSlugs) {
				t.Fatalf("ListVisibleAreaSummaries() = (%+v, %v), want %v", summaries, err, test.wantSlugs)
			}
			for index, summary := range summaries {
				if summary.Slug != test.wantSlugs[index] {
					t.Fatalf("summary %d slug = %q, want %q", index, summary.Slug, test.wantSlugs[index])
				}
				if summary.Slug != "public" {
					if summary.TopicCount != 0 || summary.PostCount != 0 || summary.LatestPostID.Valid {
						t.Fatalf("empty area summary = %+v", summary)
					}
					continue
				}
				wantTopics, wantPosts, wantLatestID, wantLatestTitle := int64(1), int64(2), int64(1002), "Visible topic"
				if test.isStaff {
					wantTopics, wantPosts, wantLatestID, wantLatestTitle = 2, 3, 1004, "Hidden topic"
				}
				if summary.TopicCount != wantTopics || summary.PostCount != wantPosts || !summary.LatestPostID.Valid || summary.LatestPostID.Int64 != wantLatestID ||
					!summary.LatestTopicTitle.Valid || summary.LatestTopicTitle.String != wantLatestTitle || !summary.LatestPostCreatedAt.Valid {
					t.Fatalf("public summary = %+v, want topics=%d posts=%d latest=%d/%q", summary, wantTopics, wantPosts, wantLatestID, wantLatestTitle)
				}
			}
			visible := make(map[string]bool, len(test.wantSlugs))
			for _, slug := range test.wantSlugs {
				visible[slug] = true
			}
			for _, slug := range []string{"public", "members", "matching", "other", "staff-only", "missing"} {
				area, lookupErr := queries.GetVisibleAreaBySlug(ctx, GetVisibleAreaBySlugParams{
					Slug: slug, IsStaff: test.isStaff, IsMember: test.isMember, GroupIds: test.groupIDs,
				})
				if visible[slug] {
					if lookupErr != nil || area.Slug != slug {
						t.Fatalf("GetVisibleAreaBySlug(%q) = (%+v, %v), want visible area", slug, area, lookupErr)
					}
					continue
				}
				if !errors.Is(lookupErr, pgx.ErrNoRows) || area != (Area{}) {
					t.Fatalf("GetVisibleAreaBySlug(%q) = (%+v, %v), want zero/pgx.ErrNoRows", slug, area, lookupErr)
				}
			}
		})
	}
}
