package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

type settingsQueryStub struct {
	row db.LoadRuntimeControlSettingsRow
	err error
}

type editableSettingsQueryStub struct {
	row db.LoadEditableControlSettingsRow
	err error
}

func (stub editableSettingsQueryStub) LoadEditableControlSettings(context.Context, db.LoadEditableControlSettingsParams) (db.LoadEditableControlSettingsRow, error) {
	return stub.row, stub.err
}

func (stub settingsQueryStub) LoadRuntimeControlSettings(context.Context) (db.LoadRuntimeControlSettingsRow, error) {
	return stub.row, stub.err
}

func testCeilings() Ceilings {
	return Ceilings{
		PublishLimit: 10, NewAccountLimit: 3,
		PublishWindow: 10 * time.Minute, NewAccountPeriod: 24 * time.Hour,
		SessionIdle: 8 * time.Hour, AuthRevalidate: 30 * time.Minute,
		SessionMaximumAge: 24 * time.Hour,
	}
}

func testSettingsRow() db.LoadRuntimeControlSettingsRow {
	return db.LoadRuntimeControlSettingsRow{
		RegistrationMode: "closed", MaintenanceMessage: "Scheduled maintenance",
		PublishRateLimit: 8, NewAccountPublishRateLimit: 2,
		PublishWindowSeconds: 300, NewAccountPeriodSeconds: 3600,
		SessionIdleSeconds: 3600, AuthRevalidateSeconds: 600,
		AdministrationRevision: 7,
	}
}

func TestLoadReturnsTypedSettingsBelowStartupCeilings(t *testing.T) {
	t.Parallel()

	loaded, err := Load(context.Background(), settingsQueryStub{row: testSettingsRow()}, testCeilings())
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if loaded.Registration != RegistrationClosed || loaded.MaintenanceEnabled ||
		loaded.MaintenanceMessage != "Scheduled maintenance" || !loaded.Publication.Valid() ||
		loaded.SessionIdle != time.Hour || loaded.AuthRevalidate != 10*time.Minute || loaded.Revision != 7 {
		t.Fatalf("Load() = %+v", loaded)
	}
	decision, err := loaded.Publication.DecidePublication(
		time.Unix(0, 0), time.Unix(3601, 0), nil, 0,
	)
	if err != nil || decision.Count != 1 || decision.RetryAfter != 0 {
		t.Fatalf("loaded publication policy decision = (%+v, %v)", decision, err)
	}
}

func TestLoadRejectsMalformedClosedValuesAndEveryCeilingOverrun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*db.LoadRuntimeControlSettingsRow)
	}{
		{name: "registration", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.RegistrationMode = "open" }},
		{name: "maintenance control", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.MaintenanceMessage = "bad\nmessage" }},
		{name: "maintenance invalid utf8", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.MaintenanceMessage = string([]byte{0xff}) }},
		{name: "maintenance rune limit", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.MaintenanceMessage = strings.Repeat("x", 281) }},
		{name: "revision", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.AdministrationRevision = 0 }},
		{name: "publish zero", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.PublishRateLimit = 0 }},
		{name: "new-account zero", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.NewAccountPublishRateLimit = 0 }},
		{name: "new-account above publish", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.NewAccountPublishRateLimit = row.PublishRateLimit + 1 }},
		{name: "publish limit", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.PublishRateLimit = 11 }},
		{name: "new-account limit", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.NewAccountPublishRateLimit = 4 }},
		{name: "publish window", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.PublishWindowSeconds = 601 }},
		{name: "new-account period", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.NewAccountPeriodSeconds = 86401 }},
		{name: "idle", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.SessionIdleSeconds = 28801 }},
		{name: "revalidate", mutate: func(row *db.LoadRuntimeControlSettingsRow) { row.AuthRevalidateSeconds = 1801 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			row := testSettingsRow()
			test.mutate(&row)
			if loaded, err := Load(context.Background(), settingsQueryStub{row: row}, testCeilings()); err == nil || loaded != (Settings{}) || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Load() = (%+v, %v), want zero/unavailable", loaded, err)
			}
		})
	}
}

func TestLoadFailsClosedAndRedactsDatabaseErrors(t *testing.T) {
	t.Parallel()

	secret := errors.New("database contains private@example.test")
	for _, test := range []struct {
		name    string
		ctx     context.Context
		querier settingsQuerier
		ceiling Ceilings
		cause   error
	}{
		{name: "nil context", querier: settingsQueryStub{}, ceiling: testCeilings()},
		{name: "nil querier", ctx: context.Background(), ceiling: testCeilings()},
		{name: "invalid ceilings", ctx: context.Background(), querier: settingsQueryStub{}},
		{name: "missing row", ctx: context.Background(), querier: settingsQueryStub{err: pgx.ErrNoRows}, ceiling: testCeilings()},
		{name: "database error", ctx: context.Background(), querier: settingsQueryStub{err: secret}, ceiling: testCeilings()},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			loaded, err := Load(test.ctx, test.querier, test.ceiling)
			if err == nil || loaded != (Settings{}) || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Load() = (%+v, %v), want zero/unavailable", loaded, err)
			}
			if strings.Contains(err.Error(), "private@example.test") {
				t.Fatalf("Load() exposed database error: %v", err)
			}
		})
	}
}

func TestRegistrationModeIsClosed(t *testing.T) {
	t.Parallel()

	for _, mode := range []RegistrationMode{
		RegistrationClosed, RegistrationVerifiedEmailOpen,
		RegistrationAdministratorApproval, RegistrationInvitationOnly,
	} {
		if !mode.Valid() {
			t.Fatalf("admitted mode %q is invalid", mode)
		}
	}
	for _, mode := range []RegistrationMode{"", "open", " CLOSED", "closed\x00"} {
		if mode.Valid() {
			t.Fatalf("unadmitted mode %q is valid", mode)
		}
	}
}

func testControlInput() Input {
	return Input{
		Registration: RegistrationClosed, MaintenanceMessage: "Maintenance soon",
		PublishLimit: 8, NewAccountLimit: 2,
		PublishWindow: 5 * time.Minute, NewAccountPeriod: time.Hour,
		SessionIdle: time.Hour, AuthRevalidate: 10 * time.Minute,
		Revision: 7, Reason: "Adjust policy",
	}
}

func TestValidateInputEnforcesSMTPAndStartupCeilingsWithoutClamping(t *testing.T) {
	t.Parallel()

	valid := testControlInput()
	settings, err := validateInput(valid, testCeilings(), false)
	if err != nil || settings.Registration != RegistrationClosed || settings.Revision != 7 {
		t.Fatalf("validateInput(closed) = (%+v, %v)", settings, err)
	}
	open := valid
	open.Registration = RegistrationVerifiedEmailOpen
	if settings, err := validateInput(open, testCeilings(), false); err == nil || settings != (Settings{}) {
		t.Fatalf("validateInput(open without SMTP) = (%+v, %v), want zero/error", settings, err)
	}
	for _, mode := range []RegistrationMode{
		RegistrationClosed, RegistrationVerifiedEmailOpen,
		RegistrationAdministratorApproval, RegistrationInvitationOnly,
	} {
		candidate := valid
		candidate.Registration = mode
		if _, err := validateInput(candidate, testCeilings(), true); err != nil {
			t.Fatalf("validateInput(%s with SMTP) returned error: %v", mode, err)
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*Input)
	}{
		{name: "publish ceiling", mutate: func(input *Input) { input.PublishLimit = 11 }},
		{name: "new account ceiling", mutate: func(input *Input) { input.NewAccountLimit = 4 }},
		{name: "publish window ceiling", mutate: func(input *Input) { input.PublishWindow = 601 * time.Second }},
		{name: "new account period ceiling", mutate: func(input *Input) { input.NewAccountPeriod = 24*time.Hour + time.Second }},
		{name: "idle ceiling", mutate: func(input *Input) { input.SessionIdle = 8*time.Hour + time.Second }},
		{name: "revalidation ceiling", mutate: func(input *Input) { input.AuthRevalidate = 30*time.Minute + time.Second }},
		{name: "subsecond", mutate: func(input *Input) { input.AuthRevalidate = time.Second + time.Nanosecond }},
		{name: "invalid registration", mutate: func(input *Input) { input.Registration = "open" }},
		{name: "maintenance invalid utf8", mutate: func(input *Input) { input.MaintenanceMessage = string([]byte{0xff}) }},
		{name: "maintenance rune limit", mutate: func(input *Input) { input.MaintenanceMessage = strings.Repeat("x", 281) }},
		{name: "publish zero", mutate: func(input *Input) { input.PublishLimit = 0 }},
		{name: "new-account zero", mutate: func(input *Input) { input.NewAccountLimit = 0 }},
		{name: "new-account above publish", mutate: func(input *Input) { input.NewAccountLimit = input.PublishLimit + 1 }},
		{name: "publish window below minimum", mutate: func(input *Input) { input.PublishWindow = 0 }},
		{name: "new-account period below minimum", mutate: func(input *Input) { input.NewAccountPeriod = time.Second }},
		{name: "idle below minimum", mutate: func(input *Input) { input.SessionIdle = 0 }},
		{name: "revalidation below minimum", mutate: func(input *Input) { input.AuthRevalidate = 0 }},
		{name: "revision", mutate: func(input *Input) { input.Revision = 0 }},
		{name: "reason empty", mutate: func(input *Input) { input.Reason = "" }},
		{name: "reason padded", mutate: func(input *Input) { input.Reason = " padded " }},
		{name: "reason control", mutate: func(input *Input) { input.Reason = "bad\nreason" }},
		{name: "reason invalid utf8", mutate: func(input *Input) { input.Reason = string([]byte{0xff}) }},
		{name: "reason byte limit", mutate: func(input *Input) { input.Reason = strings.Repeat("x", 2001) }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := valid
			test.mutate(&input)
			if settings, err := validateInput(input, testCeilings(), true); err == nil || settings != (Settings{}) {
				t.Fatalf("validateInput() = (%+v, %v), want zero/error", settings, err)
			}
		})
	}
}

func TestLoadEditableIsAuthorizationFirstAndValidatesPersistedSettings(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 17, Role: policy.RoleAdministrator}
	row := testSettingsRow()
	editableRow := db.LoadEditableControlSettingsRow{
		SettingsPresent: true, RegistrationMode: row.RegistrationMode,
		MaintenanceEnabled: row.MaintenanceEnabled, MaintenanceMessage: row.MaintenanceMessage,
		PublishRateLimit: row.PublishRateLimit, NewAccountPublishRateLimit: row.NewAccountPublishRateLimit,
		PublishWindowSeconds: row.PublishWindowSeconds, NewAccountPeriodSeconds: row.NewAccountPeriodSeconds,
		SessionIdleSeconds: row.SessionIdleSeconds, AuthRevalidateSeconds: row.AuthRevalidateSeconds,
		AdministrationRevision: row.AdministrationRevision,
	}
	loaded, err := LoadEditable(context.Background(), editableSettingsQueryStub{row: editableRow}, actor, time.Now(), testCeilings())
	if err != nil || loaded.Settings.Revision != 7 {
		t.Fatalf("LoadEditable() = (%+v, %v)", loaded, err)
	}
	if loaded, err := LoadEditable(context.Background(), editableSettingsQueryStub{row: editableRow}, policy.AccessContext{}, time.Now(), testCeilings()); err == nil || loaded != (EditableSettings{}) || !errors.Is(err, ErrDenied) {
		t.Fatalf("LoadEditable(denied) = (%+v, %v)", loaded, err)
	}
}
