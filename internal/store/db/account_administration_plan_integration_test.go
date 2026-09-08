//go:build integration

package db

import (
	"context"
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
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Plan Administrator', 'administrator') RETURNING id`).Scan(&actorID); err != nil {
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
		requireAdministrationPlan(t, mode, "accounts", accountPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`, `"Plan Rows":51`})

		detailPlan := explainPrepared(t, ctx, connection, "an04_account_detail", "timestamptz,bigint,bigint", loadAccountForAdministration,
			fmt.Sprintf("'%s',%d,%d", observedAt, actorID, targetID), mode)
		requireAdministrationPlan(t, mode, "account-detail", detailPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"users_pkey"`})

		groupsPlan := explainPrepared(t, ctx, connection, "an04_groups", "bigint,timestamptz,bigint,integer", listGroupsForAdministration,
			fmt.Sprintf("%d,'%s',0,51", actorID, observedAt), mode)
		requireAdministrationPlan(t, mode, "groups", groupsPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"forum_groups_pkey"`, `"Plan Rows":51`})

		membershipPlan := explainPrepared(t, ctx, connection, "an04_account_groups", "bigint,timestamptz,bigint,bigint,integer", listAccountGroupsForAdministration,
			fmt.Sprintf("%d,'%s',%d,0,51", actorID, observedAt, targetID), mode)
		requireAdministrationPlan(t, mode, "account-groups", membershipPlan, []string{`"Subplan Name":"CTE actor"`, `"Index Name":"forum_groups_pkey"`, `"Index Name":"forum_group_members_user_group_idx"`, `user_id =`, `group_id =`, `"Actual Loops":51`, `"Plan Rows":51`})

		continuityPlan := explainPrepared(t, ctx, connection, "an04_active_administrators", "timestamptz", countActiveAdministrators,
			fmt.Sprintf("'%s'", observedAt), mode)
		requireAdministrationPlan(t, mode, "active-administrator-count", continuityPlan, []string{`"Relation Name":"users"`, `role = 'administrator'`})
	}
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
