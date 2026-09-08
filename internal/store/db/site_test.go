package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestSiteProjectionQueriesBindAndScanExactRows(t *testing.T) {
	t.Parallel()

	observedAt := pgtype.Timestamptz{Valid: true}
	database := &siteDBTX{row: siteRow{values: []any{true, "Board", "Description", "emerald", "# Rules", "<h1>Rules</h1>", "renderer-v1", int64(7)}}}
	got, err := New(database).LoadEditableSiteSettings(context.Background(), LoadEditableSiteSettingsParams{ActorUserID: 41, ObservedAt: observedAt})
	want := LoadEditableSiteSettingsRow{
		SettingsPresent: true, SiteName: "Board", SiteDescription: "Description", BrandTheme: "emerald",
		RulesMarkdown: "# Rules", RulesHtml: "<h1>Rules</h1>", RulesRendererVersion: "renderer-v1", AdministrationRevision: 7,
	}
	if err != nil || got != want || !reflect.DeepEqual(database.args, []any{int64(41), observedAt}) {
		t.Fatalf("LoadEditableSiteSettings() = (%+v, %v, args %#v)", got, err, database.args)
	}
	for _, required := range []string{
		"actor AS MATERIALIZED", "forum_user.role = 'administrator'", "forum_user.muted_until IS NULL OR forum_user.muted_until <= $2", "LEFT JOIN LATERAL",
		"(settings.singleton IS TRUE)::boolean AS settings_present", "COALESCE(settings.administration_revision, 0)::bigint",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("editable site settings SQL lacks %q", required)
		}
	}

	database.row = siteRow{values: []any{"Board", "Description", "blue"}}
	shell, err := New(database).LoadSiteShellPresentation(context.Background())
	if err != nil || shell != (LoadSiteShellPresentationRow{SiteName: "Board", SiteDescription: "Description", BrandTheme: "blue"}) || len(database.args) != 0 {
		t.Fatalf("LoadSiteShellPresentation() = (%+v, %v, args %#v)", shell, err, database.args)
	}
	if strings.Contains(database.query, "rules_") || strings.Contains(database.query, "administration_revision") {
		t.Fatalf("shell query includes rules or revision state: %q", database.query)
	}
}

func TestSiteMutationQueriesUseExactTimeoutsAndAuditBinding(t *testing.T) {
	t.Parallel()

	database := &siteDBTX{row: siteRow{values: []any{"2s", "250ms"}}}
	configured, err := New(database).ConfigureAdministrationTransaction(context.Background())
	if err != nil || configured != (ConfigureAdministrationTransactionRow{SetConfig: "2s", SetConfig_2: "250ms"}) {
		t.Fatalf("ConfigureAdministrationTransaction() = (%+v, %v)", configured, err)
	}
	for _, required := range []string{"set_config('statement_timeout', '2000ms', true)", "set_config('lock_timeout', '250ms', true)"} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("administration transaction SQL lacks %q", required)
		}
	}

	parameters := UpdateSiteSettingsAndAuditParams{
		SiteName: "Board", SiteDescription: "Description", BrandTheme: "rose", RulesMarkdown: "# Rules", RulesHtml: "<h1>Rules</h1>",
		RulesRendererVersion: "renderer-v1", ObservedAt: pgtype.Timestamptz{Valid: true}, ExpectedRevision: 7,
		ActorUserID: pgtype.Int8{Int64: 41, Valid: true}, Reason: pgtype.Text{String: "Update presentation", Valid: true},
		PreviousSiteName: "Old", PreviousSiteDescription: "Old description", PreviousBrandTheme: "blue", PreviousRulesRendererVersion: "renderer-v1",
		PreviousRulesMarkdownSha256: strings.Repeat("a", 64), PreviousRulesHtmlSha256: strings.Repeat("b", 64),
		RulesMarkdownSha256: strings.Repeat("c", 64), RulesHtmlSha256: strings.Repeat("d", 64), RequestID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	database.row = siteRow{values: []any{int64(8), int64(91)}}
	changed, err := New(database).UpdateSiteSettingsAndAudit(context.Background(), parameters)
	if err != nil || changed != (UpdateSiteSettingsAndAuditRow{AdministrationRevision: 8, AuditID: 91}) || len(database.args) != 19 {
		t.Fatalf("UpdateSiteSettingsAndAudit() = (%+v, %v, args %d)", changed, err, len(database.args))
	}
	for _, required := range []string{"UPDATE public.site_settings", "settings.administration_revision + 1", "INSERT INTO public.moderation_actions", "'update_site_settings'", "FROM updated"} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("site settings mutation SQL lacks %q", required)
		}
	}
}

func TestSiteQueriesPreserveScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("site scan failed")
	database := &siteDBTX{row: siteRow{err: cause}}
	if got, err := New(database).LoadSiteShellPresentation(context.Background()); !errors.Is(err, cause) || got != (LoadSiteShellPresentationRow{}) {
		t.Fatalf("LoadSiteShellPresentation() = (%+v, %v), want zero/cause", got, err)
	}
	if got, err := New(database).LoadEditableSiteSettings(context.Background(), LoadEditableSiteSettingsParams{}); !errors.Is(err, cause) || got != (LoadEditableSiteSettingsRow{}) {
		t.Fatalf("LoadEditableSiteSettings() = (%+v, %v), want zero/cause", got, err)
	}
}

type siteDBTX struct {
	DBTX
	query string
	args  []any
	row   pgx.Row
}

func (database *siteDBTX) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	database.query = query
	database.args = append([]any(nil), args...)
	return database.row
}

type siteRow struct {
	values []any
	err    error
}

func (row siteRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *bool:
			*destination = value.(bool)
		case *string:
			*destination = value.(string)
		case *int64:
			*destination = value.(int64)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		default:
			panic("unexpected site row destination")
		}
	}
	return nil
}
