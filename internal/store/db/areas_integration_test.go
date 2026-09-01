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

const areaAccessTestDatabase = "gotth_bb_alpha1_area_access_test"

func TestListVisibleAreasOnPostgreSQL17(t *testing.T) {
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
		if area.groupID != 0 {
			if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by) VALUES ($1, $2, $3)`, areaID, area.groupID, ownerID); err != nil {
				t.Fatalf("map area %q: %v", area.slug, err)
			}
		}
	}
	queries := New(connection)
	for _, test := range []struct {
		name       string
		parameters ListVisibleAreasParams
		wantSlugs  []string
	}{
		{name: "visitor", parameters: ListVisibleAreasParams{}, wantSlugs: []string{"public"}},
		{name: "member empty groups", parameters: ListVisibleAreasParams{IsMember: true, GroupIds: []int64{}}, wantSlugs: []string{"public", "members"}},
		{name: "matching member", parameters: ListVisibleAreasParams{IsMember: true, GroupIds: []int64{matchingGroupID}}, wantSlugs: []string{"public", "members", "matching"}},
		{name: "other member", parameters: ListVisibleAreasParams{IsMember: true, GroupIds: []int64{otherGroupID}}, wantSlugs: []string{"public", "members", "other"}},
		{name: "staff", parameters: ListVisibleAreasParams{IsStaff: true}, wantSlugs: []string{"public", "members", "matching", "other", "staff-only"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			areas, err := queries.ListVisibleAreas(ctx, test.parameters)
			if err != nil || len(areas) != len(test.wantSlugs) {
				t.Fatalf("ListVisibleAreas() = (%+v, %v), want %v", areas, err, test.wantSlugs)
			}
			for index := range areas {
				if areas[index].Slug != test.wantSlugs[index] {
					t.Fatalf("area %d slug = %q, want %q", index, areas[index].Slug, test.wantSlugs[index])
				}
			}
		})
	}
}
