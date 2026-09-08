//go:build integration

package site

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const siteTestDatabase = "gotth_bb_an04_site_test"

func TestSiteSettingsMutationIsAtomicAuditedAndRevisionSerialized(t *testing.T) {
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
	adminConnection, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() { _ = adminConnection.Close(context.Background()) })
	databaseIdentifier := pgx.Identifier{siteTestDatabase}.Sanitize()
	if _, err := adminConnection.Exec(ctx, "DROP DATABASE IF EXISTS "+databaseIdentifier+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale site test database: %v", err)
	}
	if _, err := adminConnection.Exec(ctx, "CREATE DATABASE "+databaseIdentifier); err != nil {
		t.Fatalf("create site test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminConnection.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+databaseIdentifier+" WITH (FORCE)")
	})

	testConfig := adminConfig.Copy()
	testConfig.Database = siteTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connections := make([]*pgx.Conn, 3)
	for index := range connections {
		connections[index], err = pgx.ConnectConfig(ctx, testConfig)
		if err != nil {
			t.Fatalf("connect site test database %d: %v", index, err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}

	var actorID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Settings Administrator', 'administrator') RETURNING id`).Scan(&actorID); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	actor := policy.AccessContext{Authenticated: true, UserID: actorID, Role: policy.RoleAdministrator}
	observedAt := time.Date(2026, time.September, 8, 14, 0, 0, 123456000, time.UTC)
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	input := SettingsInput{
		Name: "Community Board", Description: "A plainly organized community.", Theme: "cyan",
		RulesMarkdown: "# Rules\n\nBe decent.", Reason: "Publish the initial community rules", Revision: 1,
	}
	result, err := UpdateSettings(ctx, connections[0], func() time.Time { return observedAt }, actor, input, requestID)
	if err != nil || result.Revision != 2 || result.AuditID <= 0 {
		t.Fatalf("UpdateSettings() = (%+v, %v)", result, err)
	}

	var name, description, theme, source, html, version string
	var revision int64
	if err := connections[0].QueryRow(ctx, `SELECT site_name, site_description, brand_theme, rules_markdown, rules_html, rules_renderer_version, administration_revision FROM public.site_settings WHERE singleton`).Scan(
		&name, &description, &theme, &source, &html, &version, &revision,
	); err != nil {
		t.Fatalf("read changed site settings: %v", err)
	}
	if name != input.Name || description != input.Description || theme != input.Theme || source != input.RulesMarkdown || html == "" || version == "" || revision != 2 {
		t.Fatalf("persisted site settings = (%q, %q, %q, %q, html %d, %q, %d)", name, description, theme, source, len(html), version, revision)
	}
	var targetSite bool
	var actionType, reason string
	var previousState, resultingState []byte
	if err := connections[0].QueryRow(ctx, `SELECT target_site, action_type, reason, previous_state, resulting_state FROM public.moderation_actions WHERE id = $1`, result.AuditID).Scan(
		&targetSite, &actionType, &reason, &previousState, &resultingState,
	); err != nil {
		t.Fatalf("read site settings audit: %v", err)
	}
	if !targetSite || actionType != "update_site_settings" || reason != input.Reason {
		t.Fatalf("audit identity = (%t, %q, %q)", targetSite, actionType, reason)
	}
	for label, document := range map[string][]byte{"previous": previousState, "resulting": resultingState} {
		var fields map[string]any
		if err := json.Unmarshal(document, &fields); err != nil {
			t.Fatalf("decode %s audit state: %v", label, err)
		}
		if _, exists := fields["rules_markdown"]; exists {
			t.Fatalf("%s audit stores raw rules Markdown", label)
		}
		if _, exists := fields["rules_html"]; exists {
			t.Fatalf("%s audit stores raw rules HTML", label)
		}
		for _, field := range []string{"rules_markdown_sha256", "rules_html_sha256"} {
			value, ok := fields[field].(string)
			if !ok || len(value) != 64 {
				t.Fatalf("%s audit %s = %#v", label, field, fields[field])
			}
		}
	}

	if _, err := UpdateSettings(ctx, connections[0], func() time.Time { return observedAt.Add(time.Second) }, actor, func() SettingsInput {
		value := input
		value.Revision = 2
		return value
	}(), pgtype.UUID{Bytes: [16]byte{2}, Valid: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("no-op UpdateSettings() error = %v, want conflict", err)
	}
	var auditCount int64
	if err := connections[0].QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions WHERE action_type = 'update_site_settings'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("site settings audit count after no-op = (%d, %v)", auditCount, err)
	}

	inputs := []SettingsInput{
		{Name: "First Winner", Description: input.Description, Theme: "emerald", RulesMarkdown: input.RulesMarkdown, Reason: "Concurrent update one", Revision: 2},
		{Name: "Second Winner", Description: input.Description, Theme: "amber", RulesMarkdown: input.RulesMarkdown, Reason: "Concurrent update two", Revision: 2},
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for index := range 2 {
		index := index
		go func() {
			ready.Done()
			<-start
			_, updateErr := UpdateSettings(ctx, connections[index+1], func() time.Time { return observedAt.Add(2 * time.Second) }, actor, inputs[index], pgtype.UUID{Bytes: [16]byte{byte(index + 3)}, Valid: true})
			results <- updateErr
		}()
	}
	ready.Wait()
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		updateErr := <-results
		switch {
		case updateErr == nil:
			successes++
		case errors.Is(updateErr, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent UpdateSettings() error = %v", updateErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = (%d success, %d conflict)", successes, conflicts)
	}
	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET muted_until = $2 WHERE id = $1`, actorID, observedAt.Add(time.Hour)); err != nil {
		t.Fatalf("mute administrator: %v", err)
	}
	if _, err := LoadEditable(ctx, db.New(connections[0]), actor, observedAt.Add(3*time.Second)); !errors.Is(err, ErrDenied) {
		t.Fatalf("muted administrator LoadEditable() error = %v, want denied", err)
	}

	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET muted_until = NULL, suspended_at = $2, suspended_until = $3, suspension_reason = 'Readiness test suspension' WHERE id = $1`, actorID, observedAt.Add(time.Second), observedAt.Add(time.Hour)); err != nil {
		t.Fatalf("suspend administrator: %v", err)
	}
	deniedInput := inputs[0]
	deniedInput.Revision = 3
	deniedInput.Name = "Denied Change"
	if _, err := UpdateSettings(ctx, connections[0], func() time.Time { return observedAt.Add(3 * time.Second) }, actor, deniedInput, pgtype.UUID{Bytes: [16]byte{9}, Valid: true}); !errors.Is(err, ErrDenied) {
		t.Fatalf("suspended administrator UpdateSettings() error = %v, want denied", err)
	}
	if err := connections[0].QueryRow(ctx, `SELECT administration_revision FROM public.site_settings WHERE singleton`).Scan(&revision); err != nil || revision != 3 {
		t.Fatalf("site settings revision after denied update = (%d, %v)", revision, err)
	}
}
