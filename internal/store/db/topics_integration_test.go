//go:build integration

package db

import (
	"context"
	"os"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const topicListTestDatabase = "gotth_bb_alpha1_topic_list_test"

func TestVisibleTopicListOnPostgreSQL17(t *testing.T) {
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+topicListTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale topic-list database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+topicListTestDatabase); err != nil {
		t.Fatalf("create topic-list database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+topicListTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = topicListTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect topic-list database: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	var ownerID, authorID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Owner', 'administrator') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Topic Author') RETURNING id`).Scan(&authorID); err != nil {
		t.Fatalf("insert author: %v", err)
	}
	var matchingGroupID, otherGroupID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Matching', $1) RETURNING id`, ownerID).Scan(&matchingGroupID); err != nil {
		t.Fatalf("insert matching group: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Other', $1) RETURNING id`, ownerID).Scan(&otherGroupID); err != nil {
		t.Fatalf("insert other group: %v", err)
	}
	areaIDs := make(map[string]int64)
	for _, area := range []struct {
		slug       string
		visibility string
		groupID    int64
	}{
		{slug: "public", visibility: "public"},
		{slug: "members", visibility: "authenticated"},
		{slug: "matching", visibility: "groups", groupID: matchingGroupID},
		{slug: "empty", visibility: "public"},
	} {
		var areaID int64
		if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, visibility, created_by, updated_by) VALUES ($1, $1, $2, $3, $3) RETURNING id`, area.slug, area.visibility, ownerID).Scan(&areaID); err != nil {
			t.Fatalf("insert area %q: %v", area.slug, err)
		}
		areaIDs[area.slug] = areaID
		if area.groupID != 0 {
			if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by) VALUES ($1, $2, $3)`, areaID, area.groupID, ownerID); err != nil {
				t.Fatalf("map area %q: %v", area.slug, err)
			}
		}
	}

	createdAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	pinnedAt := createdAt.Add(10 * time.Hour)
	insertTopicListFixture(t, ctx, connection, 101, 1001, areaIDs["public"], authorID, "Pinned", "locked", 2, createdAt, createdAt.Add(2*time.Hour), &pinnedAt, nil)
	insertTopicListFixture(t, ctx, connection, 102, 1101, areaIDs["public"], authorID, "Hidden", "hidden", 0, createdAt, createdAt.Add(9*time.Hour), nil, nil)
	insertTopicListFixture(t, ctx, connection, 103, 1201, areaIDs["public"], authorID, "Recent", "archived", 1, createdAt, createdAt.Add(8*time.Hour), nil, nil)
	insertTopicListFixture(t, ctx, connection, 104, 1301, areaIDs["public"], authorID, "Tie lower", "open", 0, createdAt, createdAt.Add(7*time.Hour), nil, nil)
	insertTopicListFixture(t, ctx, connection, 105, 1401, areaIDs["public"], authorID, "Tie higher", "open", 0, createdAt, createdAt.Add(7*time.Hour), nil, nil)
	deletedAt := createdAt.Add(11 * time.Hour)
	insertTopicListFixture(t, ctx, connection, 106, 1501, areaIDs["public"], authorID, "Deleted", "open", 0, createdAt, createdAt.Add(10*time.Hour), nil, &deletedAt)
	insertTopicListFixture(t, ctx, connection, 201, 2001, areaIDs["members"], authorID, "Members topic", "open", 0, createdAt, createdAt.Add(time.Hour), nil, nil)
	insertTopicListFixture(t, ctx, connection, 301, 3001, areaIDs["matching"], authorID, "Group topic", "open", 0, createdAt, createdAt.Add(time.Hour), nil, nil)

	queries := New(connection)
	visitorPage, err := queries.ListVisibleTopicsByAreaSlug(ctx, ListVisibleTopicsByAreaSlugParams{AreaSlug: "public", PageLimit: 2})
	if err != nil || len(visitorPage) != 2 {
		t.Fatalf("visitor public page = (%+v, %v), want two rows", visitorPage, err)
	}
	if visitorPage[0].TopicID != 101 || visitorPage[0].ReplyCount != 2 || visitorPage[0].State != "locked" ||
		visitorPage[1].TopicID != 103 || visitorPage[1].State != "archived" {
		t.Fatalf("visitor public ordering/state = %+v", visitorPage)
	}
	for _, topic := range visitorPage {
		if topic.TotalVisibleTopics != 4 || topic.AuthorDisplayName != "Topic Author" {
			t.Fatalf("visitor topic summary = %+v, want total four and author", topic)
		}
	}
	visitorSecondPage, err := queries.ListVisibleTopicsByAreaSlug(ctx, ListVisibleTopicsByAreaSlugParams{AreaSlug: "public", PageOffset: 2, PageLimit: 2})
	if err != nil || len(visitorSecondPage) != 2 || visitorSecondPage[0].TopicID != 105 || visitorSecondPage[1].TopicID != 104 {
		t.Fatalf("visitor second page = (%+v, %v), want IDs 105,104", visitorSecondPage, err)
	}

	staffPage, err := queries.ListVisibleTopicsByAreaSlug(ctx, ListVisibleTopicsByAreaSlugParams{AreaSlug: "public", IsStaff: true, PageLimit: 10})
	if err != nil || len(staffPage) != 5 || staffPage[0].TopicID != 101 || staffPage[1].TopicID != 102 || staffPage[0].TotalVisibleTopics != 5 {
		t.Fatalf("staff public page = (%+v, %v), want hidden included and deleted excluded", staffPage, err)
	}

	for _, test := range []struct {
		name       string
		parameters ListVisibleTopicsByAreaSlugParams
		wantIDs    []int64
	}{
		{name: "visitor cannot read members", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "members", PageLimit: 10}},
		{name: "member reads members", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "members", IsMember: true, PageLimit: 10}, wantIDs: []int64{201}},
		{name: "nonmember cannot inject group", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "matching", GroupIds: []int64{matchingGroupID}, PageLimit: 10}},
		{name: "wrong group cannot read", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "matching", IsMember: true, GroupIds: []int64{otherGroupID}, PageLimit: 10}},
		{name: "matching group reads", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "matching", IsMember: true, GroupIds: []int64{matchingGroupID}, PageLimit: 10}, wantIDs: []int64{301}},
		{name: "staff reads restricted", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "matching", IsStaff: true, PageLimit: 10}, wantIDs: []int64{301}},
		{name: "visible empty area", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "empty", PageLimit: 10}},
		{name: "missing area", parameters: ListVisibleTopicsByAreaSlugParams{AreaSlug: "missing", PageLimit: 10}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			topics, listErr := queries.ListVisibleTopicsByAreaSlug(ctx, test.parameters)
			if listErr != nil || len(topics) != len(test.wantIDs) {
				t.Fatalf("ListVisibleTopicsByAreaSlug() = (%+v, %v), want IDs %v", topics, listErr, test.wantIDs)
			}
			for index := range topics {
				if topics[index].TopicID != test.wantIDs[index] {
					t.Fatalf("topic %d ID = %d, want %d", index, topics[index].TopicID, test.wantIDs[index])
				}
			}
		})
	}
}

func insertTopicListFixture(t *testing.T, ctx context.Context, connection *pgx.Conn, topicID, firstPostID, areaID, authorID int64, title, state string, replyCount int, createdAt, lastActivityAt time.Time, pinnedAt, deletedAt *time.Time) {
	t.Helper()
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin topic fixture %d: %v", topicID, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	latestPostID := firstPostID + int64(replyCount)
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics (id, area_id, author_id, title, state, pinned_at, first_post_id, latest_post_id, reply_count, next_post_number, created_at, updated_at, last_activity_at, deleted_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12, $13)`,
		topicID, areaID, authorID, title, state, pinnedAt, firstPostID, latestPostID, replyCount, replyCount+2, createdAt, lastActivityAt, deletedAt); err != nil {
		t.Fatalf("insert topic fixture %d: %v", topicID, err)
	}
	for postNumber := 1; postNumber <= replyCount+1; postNumber++ {
		postID := firstPostID + int64(postNumber-1)
		if _, err := tx.Exec(ctx, `INSERT INTO public.posts (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version, created_at, updated_at) VALUES ($1, $2, $3, $4, 'source', '<p>source</p>', 'test-v1', $5, $5)`,
			postID, topicID, authorID, postNumber, createdAt); err != nil {
			t.Fatalf("insert post fixture %d/%d: %v", topicID, postNumber, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit topic fixture %d: %v", topicID, err)
	}
}
