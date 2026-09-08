package site

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/policy"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var testObservedAt = time.Date(2026, time.September, 8, 12, 0, 0, 123456000, time.UTC)
var testDestinationPolicy = abuse.NewEmptyDestinationPolicy()

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
	cause := errors.New("editable query failed")
	failed := &editableStub{err: cause}
	if _, err := LoadEditable(context.Background(), failed, admin, testObservedAt); !errors.Is(err, ErrUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("failed LoadEditable() error = %v", err)
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
			result, err := UpdateSettings(context.Background(), panicBeginner{}, func() time.Time { return testObservedAt }, testDestinationPolicy, test.actor, test.input, test.id)
			if result != (MutationResult{}) || !errors.Is(err, test.want) {
				t.Fatalf("UpdateSettings() = (%+v, %v), want %v", result, err, test.want)
			}
		})
	}
}

func TestUpdateSettingsRejectsBlockedRulesBeforeTransaction(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rules")
	if err := os.WriteFile(path, []byte("domain=blocked.example\n"), 0o400); err != nil {
		t.Fatalf("write destination policy: %v", err)
	}
	loaded, err := abuse.LoadPolicy(path, abuse.RateProfile{
		RequestLimit: 10, RequestWindow: time.Minute, RequestClientCapacity: 10,
		PublicationLimit: 10, NewAccountLimit: 3, PublicationWindow: time.Minute, NewAccountPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("LoadPolicy() returned error: %v", err)
	}
	input := SettingsInput{
		Name: "Board", Description: "Description", Theme: "blue",
		RulesMarkdown: "private [link](https://blocked.example/path)", Reason: "Publish rules", Revision: 1,
	}
	_, err = UpdateSettings(
		context.Background(), panicBeginner{}, func() time.Time { return testObservedAt }, loaded.DestinationPolicy(),
		policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}, input,
		pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	)
	if !errors.Is(err, ErrInput) || !errors.Is(err, abuse.ErrBlockedDestination) ||
		strings.Contains(err.Error(), "blocked.example") || strings.Contains(err.Error(), "private") {
		t.Fatalf("blocked settings error = %v", err)
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
	actorID    int64
	actorErr   error
	commitErr  error
	committed  bool
	rolledBack bool
}

func (tx *settingsTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	tx.queries = append(tx.queries, query)
	switch {
	case strings.Contains(query, "ConfigureAdministrationTransaction"):
		return settingsRow{values: []any{"2s", "250ms"}}
	case strings.Contains(query, "LockSiteSettingsAdministrator"):
		if tx.actorErr != nil {
			return settingsRow{err: tx.actorErr}
		}
		actorID := tx.actorID
		if actorID == 0 {
			actorID = 7
		}
		return settingsRow{values: []any{actorID}}
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
	return tx.commitErr
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
		context.Background(), settingsBeginner{tx: tx}, func() time.Time { return testObservedAt }, testDestinationPolicy,
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

func TestUpdateSettingsRejectsMismatchedAdministratorLock(t *testing.T) {
	t.Parallel()
	tx := &settingsTx{actorID: 8}
	result, err := updateSettingsForTest(tx)
	if result != (MutationResult{}) || err == nil || !strings.Contains(err.Error(), "administrator lock returned invalid state") || tx.committed || !tx.rolledBack {
		t.Fatalf("UpdateSettings() = (%+v, %v, committed %t, rolled back %t)", result, err, tx.committed, tx.rolledBack)
	}
}

func TestUpdateSettingsPreservesUnknownCommitOutcome(t *testing.T) {
	t.Parallel()
	cause := errors.New("connection lost during commit")
	tx := &settingsTx{commitErr: cause}
	result, err := updateSettingsForTest(tx)
	if result != (MutationResult{}) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "outcome unknown; inspect state before retry") || !tx.committed || !tx.rolledBack {
		t.Fatalf("UpdateSettings() = (%+v, %v, committed %t, rolled back %t)", result, err, tx.committed, tx.rolledBack)
	}
	if len(tx.queries) != 4 {
		t.Fatalf("query count after unknown commit = %d, want one transaction with four statements", len(tx.queries))
	}
}

func updateSettingsForTest(tx *settingsTx) (MutationResult, error) {
	return UpdateSettings(
		context.Background(), settingsBeginner{tx: tx}, func() time.Time { return testObservedAt }, testDestinationPolicy,
		policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator},
		SettingsInput{Name: "Community", Description: "A careful forum.", Theme: "emerald", RulesMarkdown: "# Rules", Reason: "Publish initial rules", Revision: 1},
		pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	)
}
