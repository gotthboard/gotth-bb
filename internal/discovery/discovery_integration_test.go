//go:build integration

package discovery

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	forumservice "github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
)

const discoveryTestDatabase = "gotth_bb_an02_02_discovery_test"

var discoveryPublicationPolicy = func() abuse.PublicationPolicy {
	policy, err := abuse.NewPublicationPolicy(100_000, 100_000, 24*time.Hour, time.Minute)
	if err != nil {
		panic(err)
	}
	return policy
}()

var discoveryDestinationPolicy = abuse.NewEmptyDestinationPolicy()

func TestDiscoveryAuthorizationCursorAndDirectPostOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+discoveryTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+discoveryTestDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+discoveryTestDatabase+" WITH (FORCE)")
	})
	configured := adminConfig.Copy()
	configured.Database = discoveryTestDatabase
	if err := migration.Apply(ctx, configured, migrations.Files()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	connection, err := pgx.ConnectConfig(ctx, configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	ownerID := insertDiscoveryUser(t, ctx, connection, "Owner", "administrator")
	memberID := insertDiscoveryUser(t, ctx, connection, "Member", "member")
	moderatorID := insertDiscoveryUser(t, ctx, connection, "Moderator", "moderator")
	groupID := insertDiscoveryGroup(t, ctx, connection, ownerID)
	if _, err := connection.Exec(ctx, `INSERT INTO public.forum_group_members (group_id, user_id, granted_by) VALUES ($1, $2, $3)`, groupID, memberID, ownerID); err != nil {
		t.Fatalf("insert discovery group membership: %v", err)
	}
	publicArea := insertDiscoveryArea(t, ctx, connection, ownerID, "public", "public", 0)
	authArea := insertDiscoveryArea(t, ctx, connection, ownerID, "members", "authenticated", 0)
	groupArea := insertDiscoveryArea(t, ctx, connection, ownerID, "group", "groups", groupID)
	visitor := policy.AccessContext{}
	member := policy.AccessContext{Authenticated: true, UserID: memberID, Role: policy.RoleMember}
	groupMember := policy.AccessContext{Authenticated: true, UserID: memberID, Role: policy.RoleMember, GroupIDs: []int64{groupID}}
	staff := policy.AccessContext{Authenticated: true, UserID: ownerID, Role: policy.RoleAdministrator}
	moderator := policy.AccessContext{Authenticated: true, UserID: moderatorID, Role: policy.RoleModerator}

	base := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	publicTopic := createDiscoveryTopic(t, ctx, connection, member, publicArea, base, "Needle public", "needle public body")
	authTopic := createDiscoveryTopic(t, ctx, connection, member, authArea, base.Add(time.Second), "Needle members", "needle members body")
	groupTopic := createDiscoveryTopic(t, ctx, connection, groupMember, groupArea, base.Add(2*time.Second), "Needle group", "needle group body")
	hiddenTopic := createDiscoveryTopic(t, ctx, connection, member, publicArea, base.Add(3*time.Second), "Needle hidden", "needle hidden body")
	if _, err := connection.Exec(ctx, `UPDATE public.topics SET state = 'hidden' WHERE id = $1`, hiddenTopic.TopicID); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		actor policy.AccessContext
		want  int
	}{
		{name: "visitor", actor: visitor, want: 1},
		{name: "member", actor: member, want: 2},
		{name: "group member", actor: groupMember, want: 3},
		{name: "moderator", actor: moderator, want: 4},
		{name: "staff", actor: staff, want: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := ParseSearchRequest("q=needle")
			if err != nil {
				t.Fatal(err)
			}
			page, err := Search(ctx, connection, request, test.actor)
			if err != nil || len(page.Results) != test.want {
				t.Fatalf("Search() = (%+v, %v), want %d title results", page, err, test.want)
			}
			for _, result := range page.Results {
				if result.Kind != "topic" {
					t.Fatalf("root dedup returned %+v", result)
				}
			}
		})
	}

	bodyRequest, err := ParseSearchRequest("q=body")
	if err != nil {
		t.Fatal(err)
	}
	bodyPage, err := Search(ctx, connection, bodyRequest, visitor)
	if err != nil || len(bodyPage.Results) != 1 || bodyPage.Results[0].PostID != publicTopic.PostID || bodyPage.Results[0].Excerpt != "needle public body" {
		t.Fatalf("body Search() = (%+v, %v)", bodyPage, err)
	}

	ring := loadIntegrationKeyring(t)
	activity, err := RecentActivity(ctx, connection, nil, ring, visitor)
	if err != nil || len(activity.Results) != 1 || activity.Results[0].PostID != publicTopic.PostID || activity.NextCursor != "" {
		t.Fatalf("visitor RecentActivity() = (%+v, %v)", activity, err)
	}
	groupActivity, err := RecentActivity(ctx, connection, nil, ring, groupMember)
	if err != nil || len(groupActivity.Results) != 3 || groupActivity.Results[0].PostID != groupTopic.PostID || groupActivity.Results[1].PostID != authTopic.PostID {
		t.Fatalf("group RecentActivity() = (%+v, %v)", groupActivity, err)
	}

	direct, err := GetDirectPost(ctx, db.New(connection), publicTopic.PostID, visitor)
	if err != nil || direct.PostID != publicTopic.PostID || direct.VisibleText != "needle public body" {
		t.Fatalf("public GetDirectPost() = (%+v, %v)", direct, err)
	}
	if got, err := GetDirectPost(ctx, db.New(connection), authTopic.PostID, visitor); err == nil || got != (DirectPost{}) {
		t.Fatalf("restricted GetDirectPost() = (%+v, %v), want missing", got, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET search_vector = NULL, search_projection_version = NULL WHERE id = $1`, publicTopic.PostID); err != nil {
		t.Fatal(err)
	}
	projectionIndependent, err := GetDirectPost(ctx, db.New(connection), publicTopic.PostID, visitor)
	if err != nil || projectionIndependent.PostID != publicTopic.PostID {
		t.Fatalf("projection-independent GetDirectPost() = (%+v, %v)", projectionIndependent, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET search_vector = to_tsvector('pg_catalog.simple'::regconfig, 'needle public body'), search_projection_version = 'search-v1-pg17-simple-u15-p2' WHERE id = $1`, publicTopic.PostID); err != nil {
		t.Fatal(err)
	}
	dateBoundary := createDiscoveryTopic(t, ctx, connection, member, publicArea, base, "Boundary match", "boundary match")
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET created_at = $2, updated_at = $2 WHERE id = $1`, dateBoundary.PostID, base.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	dateRequest, err := ParseSearchRequest("q=boundary&from=2026-09-08&to=2026-09-08")
	if err != nil {
		t.Fatal(err)
	}
	datePage, err := Search(ctx, connection, dateRequest, visitor)
	if err != nil || len(datePage.Results) != 1 || datePage.Results[0].Kind != "post" || datePage.Results[0].PostID != dateBoundary.PostID {
		t.Fatalf("date-filtered root dedup = (%+v, %v), want root post", datePage, err)
	}

	for index := 0; index < 51; index++ {
		createDiscoveryTopic(t, ctx, connection, member, publicArea, base.Add(time.Duration(100+index)*time.Second), fmt.Sprintf("Fence %02d", index), "fence body")
	}
	fenceRequest, err := ParseSearchRequest("q=fence")
	if err != nil {
		t.Fatal(err)
	}
	firstFencePage, err := Search(ctx, connection, fenceRequest, visitor)
	if err != nil || len(firstFencePage.Results) != 25 || !firstFencePage.HasNextPage || !firstFencePage.BeyondWindow {
		t.Fatalf("first fence page = (%+v, %v)", firstFencePage, err)
	}
	fenceRequest.Page = 2
	secondFencePage, err := Search(ctx, connection, fenceRequest, visitor)
	if err != nil || len(secondFencePage.Results) != 25 || secondFencePage.HasNextPage || !secondFencePage.BeyondWindow {
		t.Fatalf("second fence page = (%+v, %v)", secondFencePage, err)
	}
	firstActivityPage, err := RecentActivity(ctx, connection, nil, ring, visitor)
	if err != nil || len(firstActivityPage.Results) != 25 || firstActivityPage.NextCursor == "" {
		t.Fatalf("first bounded activity page = (%+v, %v)", firstActivityPage, err)
	}
	verified, err := ring.VerifyCursor(firstActivityPage.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	secondActivityPage, err := RecentActivity(ctx, connection, &verified, ring, visitor)
	if err != nil || len(secondActivityPage.Results) != 25 || secondActivityPage.Results[0].PostID >= firstActivityPage.Results[len(firstActivityPage.Results)-1].PostID {
		t.Fatalf("second bounded activity page = (%+v, %v)", secondActivityPage, err)
	}
	if page, err := RecentActivity(ctx, connection, &verified, ring, member); err == nil || len(page.Results) != 0 || page.NextCursor != "" {
		t.Fatalf("audience-changed activity = (%+v, %v), want error", page, err)
	}
}

func insertDiscoveryUser(t *testing.T, ctx context.Context, connection *pgx.Conn, name, role string) int64 {
	t.Helper()
	var id int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ($1, $2) RETURNING id`, name, role).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertDiscoveryGroup(t *testing.T, ctx context.Context, connection *pgx.Conn, ownerID int64) int64 {
	t.Helper()
	var id int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Discovery', $1) RETURNING id`, ownerID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertDiscoveryArea(t *testing.T, ctx context.Context, connection *pgx.Conn, ownerID int64, slug, visibility string, groupID int64) string {
	t.Helper()
	var areaID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.areas (slug, name, visibility, created_by, updated_by) VALUES ($1, $1, $2, $3, $3) RETURNING id`, slug, visibility, ownerID).Scan(&areaID); err != nil {
		t.Fatal(err)
	}
	if groupID > 0 {
		if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by) VALUES ($1, $2, $3)`, areaID, groupID, ownerID); err != nil {
			t.Fatal(err)
		}
	}
	return slug
}

func createDiscoveryTopic(t *testing.T, ctx context.Context, connection *pgx.Conn, actor policy.AccessContext, area string, createdAt time.Time, title, body string) forumservice.PublishResult {
	t.Helper()
	result, err := forumservice.CreateTopic(ctx, connection, discoveryPublicationPolicy, discoveryDestinationPolicy, actor, area, title, body)
	if err != nil {
		t.Fatalf("CreateTopic(%q): %v", title, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.topics SET created_at = $2, updated_at = $2 WHERE id = $1`, result.TopicID, createdAt); err != nil {
		t.Fatalf("set topic fixture time for %q: %v", title, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET created_at = $2, updated_at = $2 WHERE id = $1`, result.PostID, createdAt); err != nil {
		t.Fatalf("set post fixture time for %q: %v", title, err)
	}
	return result
}

func loadIntegrationKeyring(t *testing.T) CursorKeyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-keyring.json")
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	contents := fmt.Sprintf(`{"version":1,"active":{"id":7,"key":%q,"not_before":"2026-01-01T00:00:00Z","issue_not_after":"2026-12-31T00:00:00Z"},"previous":null}`, key)
	if err := os.WriteFile(path, []byte(contents), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := LoadCursorKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
