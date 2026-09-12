//go:build integration

package db

import (
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

const accountAdministrationPlanTestDatabase = "gotth_bb_an04_account_plan_test"

func TestAccountAdministrationPlansOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
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
	databaseIdentifier := pgx.Identifier{accountAdministrationPlanTestDatabase}.Sanitize()
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+databaseIdentifier+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+databaseIdentifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+databaseIdentifier+" WITH (FORCE)")
	})
	configured := adminConfig.Copy()
	configured.Database = accountAdministrationPlanTestDatabase
	if err := migration.Apply(ctx, configured, migrations.Files()); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.ConnectConfig(ctx, configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	var actorID, targetID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role, authentik_sync_state) VALUES ('Plan Administrator', 'administrator', 'accepted') RETURNING id`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Plan Target') RETURNING id`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.users (display_name)
		SELECT 'Plan Account ' || value FROM generate_series(1, 24998) AS value`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.forum_groups (name, created_by)
		SELECT 'Plan Group ' || value, $1 FROM generate_series(1, 25000) AS value`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.forum_group_members (group_id, user_id, granted_by)
		SELECT id, $1, $2 FROM public.forum_groups WHERE id % 2 = 0`, targetID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `ANALYZE public.users; ANALYZE public.forum_groups; ANALYZE public.forum_group_members`); err != nil {
		t.Fatal(err)
	}
	observedAt := "2026-09-08T16:00:00Z"
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		accountPlan := explainPrepared(t, ctx, connection, "an04_accounts", "timestamptz,bigint,bigint,integer", listAccountsForAdministration,
			fmt.Sprintf("'%s',%d,0,51", observedAt, actorID), mode)
		requireAdministrationPlan(t, mode, "accounts", accountPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`, `"Actual Rows":51`})
		deniedAccountPlan := explainPrepared(t, ctx, connection, "an04_accounts_denied", "timestamptz,bigint,bigint,integer", listAccountsForAdministration,
			fmt.Sprintf("'%s',%d,0,51", observedAt, targetID), mode)
		requireDeniedAdministrationPlan(t, mode, "accounts", deniedAccountPlan, "users")

		detailPlan := explainPrepared(t, ctx, connection, "an04_account_detail", "timestamptz,bigint,bigint", loadAccountForAdministration,
			fmt.Sprintf("'%s',%d,%d", observedAt, actorID, targetID), mode)
		requireAdministrationPlan(t, mode, "account-detail", detailPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`})
		deniedDetailPlan := explainPrepared(t, ctx, connection, "an04_account_detail_denied", "timestamptz,bigint,bigint", loadAccountForAdministration,
			fmt.Sprintf("'%s',%d,%d", observedAt, targetID, actorID), mode)
		requireDeniedAdministrationPlan(t, mode, "account-detail", deniedDetailPlan, "users")

		groupsPlan := explainPrepared(t, ctx, connection, "an04_groups", "bigint,timestamptz,bigint,integer", listGroupsForAdministration,
			fmt.Sprintf("%d,'%s',0,51", actorID, observedAt), mode)
		requireAdministrationPlan(t, mode, "groups", groupsPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"forum_groups_pkey"`, `"Actual Rows":51`})
		deniedGroupsPlan := explainPrepared(t, ctx, connection, "an04_groups_denied", "bigint,timestamptz,bigint,integer", listGroupsForAdministration,
			fmt.Sprintf("%d,'%s',0,51", targetID, observedAt), mode)
		requireDeniedAdministrationPlan(t, mode, "groups", deniedGroupsPlan, "forum_groups")

		membershipPlan := explainPrepared(t, ctx, connection, "an04_account_groups", "bigint,timestamptz,bigint,bigint,integer", listAccountGroupsForAdministration,
			fmt.Sprintf("%d,'%s',%d,0,51", actorID, observedAt, targetID), mode)
		requireAdministrationPlan(t, mode, "account-groups", membershipPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"forum_groups_pkey"`, `"Index Name":"forum_group_members_user_group_idx"`, `user_id =`, `group_id =`, `"Actual Loops":51`, `"Actual Rows":51`})
		deniedMembershipPlan := explainPrepared(t, ctx, connection, "an04_account_groups_denied", "bigint,timestamptz,bigint,bigint,integer", listAccountGroupsForAdministration,
			fmt.Sprintf("%d,'%s',%d,0,51", targetID, observedAt, actorID), mode)
		requireDeniedAdministrationPlan(t, mode, "account-groups", deniedMembershipPlan, "users", "forum_groups", "forum_group_members")

		continuityPlan := explainPrepared(t, ctx, connection, "an04_active_administrators", "timestamptz", countActiveAdministrators,
			fmt.Sprintf("'%s'", observedAt), mode)
		requireAdministrationPlan(t, mode, "active-administrator-count", continuityPlan, []string{`"Relation Name":"users"`, `role = 'administrator'`})
	}
}

func requireDeniedAdministrationPlan(t *testing.T, mode, name, encoded string, privateRelations ...string) {
	t.Helper()
	var document explainPlanDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil || len(document) != 1 {
		t.Fatalf("%s %s decode denied plan: documents=%d error=%v", mode, name, len(document), err)
	}
	root := &document[0].Plan
	actor := findPlanNode(root, func(node *explainPlanNode) bool { return node.SubplanName == "CTE actor" })
	if actor == nil || actor.ActualRows != 0 || actor.ActualLoops != 1 {
		t.Fatalf("%s %s denied actor fence rows/loops must be 0/1: %s", mode, name, encoded)
	}
	required := make(map[string]bool, len(privateRelations))
	for _, relation := range privateRelations {
		required[relation] = false
	}
	var inspect func(*explainPlanNode)
	inspect = func(node *explainPlanNode) {
		if node == actor {
			return
		}
		if _, tracked := required[node.RelationName]; tracked {
			required[node.RelationName] = true
			if node.ActualLoops != 0 {
				t.Fatalf("%s %s denied private relation %s executed %d loops: %s", mode, name, node.RelationName, node.ActualLoops, encoded)
			}
		}
		for index := range node.Plans {
			inspect(&node.Plans[index])
		}
	}
	inspect(root)
	for relation, seen := range required {
		if !seen {
			t.Fatalf("%s %s denied plan lacks private relation %s outside actor fence: %s", mode, name, relation, encoded)
		}
	}
	t.Logf("PLAN mode=%s query=%s-denied actor_rows=0 private_loops=0\n%s", mode, name, encoded)
}

func requireAdministrationPlan(t *testing.T, mode, name, plan string, required []string) {
	t.Helper()
	for _, fragment := range required {
		if !strings.Contains(plan, fragment) {
			t.Fatalf("%s %s plan lacks %s: %s", mode, name, fragment, plan)
		}
	}
	t.Logf("PLAN mode=%s query=%s\n%s", mode, name, plan)
}
