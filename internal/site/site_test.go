package site

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var testObservedAt = time.Date(2026, time.September, 8, 12, 0, 0, 123456000, time.UTC)

type shellStub struct {
	row db.LoadSiteShellPresentationRow
	err error
}

func (stub shellStub) LoadSiteShellPresentation(context.Context) (db.LoadSiteShellPresentationRow, error) {
	return stub.row, stub.err
}

type rulesStub struct {
	row db.LoadPublicRulesRow
	err error
}

func (stub rulesStub) LoadPublicRules(context.Context) (db.LoadPublicRulesRow, error) {
	return stub.row, stub.err
}

type editableStub struct {
	row    db.LoadEditableSiteSettingsRow
	err    error
	called bool
}

func (stub *editableStub) LoadEditableSiteSettings(_ context.Context, parameters db.LoadEditableSiteSettingsParams) (db.LoadEditableSiteSettingsRow, error) {
	stub.called = true
	if parameters.ActorUserID != 7 || !parameters.ObservedAt.Valid || !parameters.ObservedAt.Time.Equal(testObservedAt) {
		panic("editable settings received incorrect authorization facts")
	}
	return stub.row, stub.err
}

func TestLoadSiteProjectionsValidatePersistedState(t *testing.T) {
	t.Parallel()
	defaultShell := db.LoadSiteShellPresentationRow{SiteName: "GOTTH Board", SiteDescription: "Community discussions, plainly organized.", BrandTheme: "blue"}
	shell, err := LoadShell(context.Background(), shellStub{row: defaultShell})
	if err != nil || shell.Name != defaultShell.SiteName || shell.Description != defaultShell.SiteDescription || shell.Theme != defaultShell.BrandTheme {
		t.Fatalf("LoadShell() = (%+v, %v)", shell, err)
	}
	if _, err := LoadShell(context.Background(), shellStub{row: db.LoadSiteShellPresentationRow{SiteName: " bad", BrandTheme: "blue"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("LoadShell(malformed) error = %v, want unavailable", err)
	}

	rendered, err := contentrender.RenderMarkdown("# Rules")
	if err != nil {
		t.Fatalf("RenderMarkdown() returned error: %v", err)
	}
	rulesHTML, rendererVersion, err := rendered.PersistenceValues()
	if err != nil {
		t.Fatalf("PersistenceValues() returned error: %v", err)
	}
	rules, err := LoadRules(context.Background(), rulesStub{row: db.LoadPublicRulesRow{
		SiteName: defaultShell.SiteName, SiteDescription: defaultShell.SiteDescription, BrandTheme: defaultShell.BrandTheme,
		RulesMarkdown: "# Rules", RulesHtml: rulesHTML, RulesRendererVersion: rendererVersion,
	}})
	if err != nil || rules.Shell.Name != defaultShell.SiteName || rules.HTML != rendered.TrustedHTML() {
		t.Fatalf("LoadRules() = (%+v, %v)", rules, err)
	}
	if _, err := LoadRules(context.Background(), rulesStub{row: db.LoadPublicRulesRow{
		SiteName: defaultShell.SiteName, SiteDescription: defaultShell.SiteDescription, BrandTheme: defaultShell.BrandTheme,
		RulesMarkdown: "# Rules", RulesHtml: "<p>forged</p>", RulesRendererVersion: rendererVersion,
	}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("LoadRules(forged) error = %v, want unavailable", err)
	}
}

func TestLoadEditableIsAuthorizationFirstAndDistinguishesMissingSettings(t *testing.T) {
	t.Parallel()
	memberStore := &editableStub{}
	if _, err := LoadEditable(context.Background(), memberStore, policy.AccessContext{Authenticated: true, UserID: 8, Role: policy.RoleMember}, testObservedAt); !errors.Is(err, ErrDenied) || memberStore.called {
		t.Fatalf("member LoadEditable() = (called %t, error %v)", memberStore.called, err)
	}
	admin := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	missing := &editableStub{row: db.LoadEditableSiteSettingsRow{SettingsPresent: false}}
	if _, err := LoadEditable(context.Background(), missing, admin, testObservedAt); !errors.Is(err, ErrUnavailable) || !missing.called {
		t.Fatalf("missing LoadEditable() = (called %t, error %v)", missing.called, err)
	}
	valid := &editableStub{row: db.LoadEditableSiteSettingsRow{
		SettingsPresent: true, SiteName: "Board", SiteDescription: "Description", BrandTheme: "cyan",
		RulesRendererVersion: contentrender.RendererVersion, AdministrationRevision: 4,
	}}
	got, err := LoadEditable(context.Background(), valid, admin, testObservedAt)
	if err != nil || got.Revision != 4 || got.Shell.Name != "Board" || got.RendererVersion != contentrender.RendererVersion {
		t.Fatalf("LoadEditable() = (%+v, %v)", got, err)
	}
}

type panicBeginner struct{}

func (panicBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	panic("site settings transaction started")
}

func TestUpdateSettingsRejectsInvalidBoundaryBeforeDatabaseWork(t *testing.T) {
	t.Parallel()
	admin := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	valid := SettingsInput{Name: "Board", Description: "Description", Theme: "blue", RulesMarkdown: "# Rules", Reason: "Publish rules", Revision: 1}
	tests := []struct {
		name  string
		actor policy.AccessContext
		input SettingsInput
		id    pgtype.UUID
		want  error
	}{
		{name: "member", actor: policy.AccessContext{Authenticated: true, UserID: 8, Role: policy.RoleMember}, input: valid, id: requestID, want: ErrDenied},
		{name: "non canonical name", actor: admin, input: func() SettingsInput { value := valid; value.Name = " Board"; return value }(), id: requestID, want: ErrInput},
		{name: "control description", actor: admin, input: func() SettingsInput { value := valid; value.Description = "bad\nvalue"; return value }(), id: requestID, want: ErrInput},
		{name: "open theme", actor: admin, input: func() SettingsInput { value := valid; value.Theme = "purple"; return value }(), id: requestID, want: ErrInput},
		{name: "blank reason", actor: admin, input: func() SettingsInput { value := valid; value.Reason = ""; return value }(), id: requestID, want: ErrInput},
		{name: "zero revision", actor: admin, input: func() SettingsInput { value := valid; value.Revision = 0; return value }(), id: requestID, want: ErrInput},
		{name: "missing request", actor: admin, input: valid, want: ErrInput},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := UpdateSettings(context.Background(), panicBeginner{}, func() time.Time { return testObservedAt }, test.actor, test.input, test.id)
			if result != (MutationResult{}) || !errors.Is(err, test.want) {
				t.Fatalf("UpdateSettings() = (%+v, %v), want %v", result, err, test.want)
			}
		})
	}
}

type settingsBeginner struct{ tx *settingsTx }

func (beginner settingsBeginner) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if options != (pgx.TxOptions{IsoLevel: pgx.ReadCommitted}) {
		panic("site settings transaction did not use read committed isolation")
	}
	return beginner.tx, nil
}

type settingsTx struct {
	pgx.Tx
	queries    []string
	updateArgs []any
	committed  bool
	rolledBack bool
}

func (tx *settingsTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	tx.queries = append(tx.queries, query)
	switch {
	case strings.Contains(query, "ConfigureAdministrationTransaction"):
		return settingsRow{values: []any{"2s", "250ms"}}
	case strings.Contains(query, "LockSiteSettingsAdministrator"):
		return settingsRow{values: []any{int64(7)}}
	case strings.Contains(query, "LockSiteSettings"):
		return settingsRow{values: []any{
			"GOTTH Board", "Community discussions, plainly organized.", "blue", "", "", contentrender.RendererVersion,
			int64(1), pgtype.Timestamptz{Time: testObservedAt.Add(-time.Hour), Valid: true},
		}}
	case strings.Contains(query, "UpdateSiteSettingsAndAudit"):
		tx.updateArgs = append([]any(nil), arguments...)
		return settingsRow{values: []any{int64(2), int64(41)}}
	default:
		panic("unexpected site settings query")
	}
}

func (tx *settingsTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *settingsTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type settingsRow struct {
	values []any
	err    error
}

func (row settingsRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *string:
			*destination = value.(string)
		case *int64:
			*destination = value.(int64)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		default:
			panic("unexpected site settings scan destination")
		}
	}
	return nil
}

func TestUpdateSettingsCommitsExactRevisionAndDigestAudit(t *testing.T) {
	t.Parallel()
	tx := &settingsTx{}
	result, err := UpdateSettings(
		context.Background(), settingsBeginner{tx: tx}, func() time.Time { return testObservedAt },
		policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator},
		SettingsInput{Name: "Community", Description: "A careful forum.", Theme: "emerald", RulesMarkdown: "# Rules", Reason: "Publish initial rules", Revision: 1},
		pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	)
	if err != nil || result != (MutationResult{Revision: 2, AuditID: 41}) || !tx.committed || tx.rolledBack {
		t.Fatalf("UpdateSettings() = (%+v, %v, committed %t, rolled back %t)", result, err, tx.committed, tx.rolledBack)
	}
	if len(tx.queries) != 4 || len(tx.updateArgs) != 19 {
		t.Fatalf("query/update shape = (%d, %d)", len(tx.queries), len(tx.updateArgs))
	}
	for _, index := range []int{14, 15, 16, 17} {
		value, ok := tx.updateArgs[index].(string)
		if !ok || len(value) != 64 {
			t.Fatalf("audit digest argument %d = %#v", index, tx.updateArgs[index])
		}
	}
}
