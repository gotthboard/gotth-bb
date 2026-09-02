package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGetModerationUserStatusReturnsExactAuthorizedState(t *testing.T) {
	t.Parallel()

	createdAt := moderationStatusTime(8)
	updatedAt := moderationStatusTime(9)
	lastLoginAt := moderationStatusTime(10)
	base := db.GetModerationUserStatusRow{
		ID: 41, DisplayName: "Local member", Role: "member",
		CreatedAt: moderationStatusTimestamp(createdAt), UpdatedAt: moderationStatusTimestamp(updatedAt),
		LastLoginAt: moderationStatusTimestamp(lastLoginAt),
	}
	suspended := base
	suspended.SuspendedAt = moderationStatusTimestamp(updatedAt)
	suspended.SuspensionReason = pgtype.Text{String: "Repeated abuse", Valid: true}
	expired := suspended
	expired.SuspendedUntil = moderationStatusTimestamp(moderationStatusTime(11))
	for _, test := range []struct {
		name          string
		row           db.GetModerationUserStatusRow
		actor         policy.AccessContext
		observedAt    time.Time
		wantSuspended bool
		wantRole      policy.Role
		wantParams    db.GetModerationUserStatusParams
	}{
		{
			name: "moderator member", row: base,
			actor:      policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator},
			observedAt: moderationStatusTime(12).Add(999 * time.Nanosecond), wantRole: policy.RoleMember,
			wantParams: db.GetModerationUserStatusParams{TargetUserID: 41, ActorUserID: 11, IsModerator: true},
		},
		{
			name: "administrator suspended member", row: suspended,
			actor:      policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleAdministrator},
			observedAt: moderationStatusTime(12), wantSuspended: true, wantRole: policy.RoleMember,
			wantParams: db.GetModerationUserStatusParams{TargetUserID: 41, ActorUserID: 12, IsAdministrator: true},
		},
		{
			name: "expired suspension", row: expired,
			actor:      policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleAdministrator},
			observedAt: moderationStatusTime(12), wantRole: policy.RoleMember,
			wantParams: db.GetModerationUserStatusParams{TargetUserID: 41, ActorUserID: 12, IsAdministrator: true},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			querier := &moderationStatusTestQuerier{row: test.row}
			got, err := GetModerationUserStatus(context.Background(), querier, test.actor, 41, test.observedAt)
			want := ModerationUserStatus{
				UserID: test.row.ID, DisplayName: test.row.DisplayName, Role: test.wantRole, Suspended: test.wantSuspended,
				SuspendedAt: test.row.SuspendedAt, SuspendedUntil: test.row.SuspendedUntil,
				SuspensionReason: test.row.SuspensionReason, MutedUntil: test.row.MutedUntil,
				CreatedAt: test.row.CreatedAt, UpdatedAt: test.row.UpdatedAt, LastLoginAt: test.row.LastLoginAt,
			}
			if err != nil || !reflect.DeepEqual(got, want) || querier.params != test.wantParams || querier.calls != 1 {
				t.Fatalf("GetModerationUserStatus() = (%+v, %v), params %+v, calls %d; want %+v", got, err, querier.params, querier.calls, want)
			}
		})
	}
}

func TestGetModerationUserStatusRejectsBeforeQuery(t *testing.T) {
	t.Parallel()

	moderator := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	mutedAt := moderationStatusTime(13)
	for _, run := range []func() error{
		func() error {
			_, err := GetModerationUserStatus(nil, panicModerationStatusQuerier{}, moderator, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), nil, moderator, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, policy.AccessContext{Authenticated: true, Role: policy.RoleMember}, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(canceled, panicModerationStatusQuerier{}, moderator, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, policy.AccessContext{}, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator, Suspended: true}, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator, MutedUntil: &mutedAt}, 41, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, moderator, 0, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, moderator, 11, moderationStatusTime(12))
			return err
		},
		func() error {
			_, err := GetModerationUserStatus(context.Background(), panicModerationStatusQuerier{}, moderator, 41, time.Time{})
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatal("GetModerationUserStatus accepted an invalid boundary")
		}
	}
}

func TestGetModerationUserStatusPreservesQueryFailureAndRejectsMalformedRow(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}
	cause := errors.New("query failed")
	if got, err := GetModerationUserStatus(context.Background(), &moderationStatusTestQuerier{err: cause}, actor, 41, moderationStatusTime(12)); got != (ModerationUserStatus{}) || !errors.Is(err, cause) {
		t.Fatalf("query failure = (%+v, %v), want zero/cause", got, err)
	}
	malformed := db.GetModerationUserStatusRow{
		ID: 41, Role: "member", CreatedAt: moderationStatusTimestamp(moderationStatusTime(8)),
		UpdatedAt: moderationStatusTimestamp(moderationStatusTime(9)), LastLoginAt: moderationStatusTimestamp(moderationStatusTime(10)),
	}
	if got, err := GetModerationUserStatus(context.Background(), &moderationStatusTestQuerier{row: malformed}, actor, 41, moderationStatusTime(12)); got != (ModerationUserStatus{}) || err == nil || errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("malformed row = (%+v, %v), want internal failure", got, err)
	}
}

func TestModerationUserStatusValidationClosesPersistedValues(t *testing.T) {
	t.Parallel()

	createdAt := moderationStatusTimestamp(moderationStatusTime(8))
	base := db.GetModerationUserStatusRow{CreatedAt: createdAt}
	if !validModerationSuspension(base) {
		t.Fatal("active suspension state was rejected")
	}
	valid := base
	valid.SuspendedAt = moderationStatusTimestamp(moderationStatusTime(9))
	valid.SuspensionReason = pgtype.Text{String: "Valid reason", Valid: true}
	if !validModerationSuspension(valid) {
		t.Fatal("valid suspension state was rejected")
	}
	for _, invalid := range []db.GetModerationUserStatusRow{
		{CreatedAt: createdAt, SuspendedUntil: moderationStatusTimestamp(moderationStatusTime(10))},
		{CreatedAt: createdAt, SuspendedAt: pgtype.Timestamptz{}, SuspensionReason: pgtype.Text{String: "reason", Valid: true}},
		{CreatedAt: createdAt, SuspendedAt: moderationStatusTimestamp(moderationStatusTime(7)), SuspensionReason: pgtype.Text{String: "reason", Valid: true}},
		{CreatedAt: createdAt, SuspendedAt: moderationStatusTimestamp(moderationStatusTime(9))},
		{CreatedAt: createdAt, SuspendedAt: moderationStatusTimestamp(moderationStatusTime(9)), SuspensionReason: pgtype.Text{String: " bad ", Valid: true}},
		{CreatedAt: createdAt, SuspendedAt: moderationStatusTimestamp(moderationStatusTime(9)), SuspensionReason: pgtype.Text{String: "reason", Valid: true}, SuspendedUntil: pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.Infinity}},
		{CreatedAt: createdAt, SuspendedAt: moderationStatusTimestamp(moderationStatusTime(9)), SuspensionReason: pgtype.Text{String: "reason", Valid: true}, SuspendedUntil: moderationStatusTimestamp(moderationStatusTime(9))},
	} {
		if validModerationSuspension(invalid) {
			t.Fatalf("invalid suspension was accepted: %+v", invalid)
		}
	}
	for _, reason := range []string{"", " bad", "bad ", "line\nbreak", string([]byte{0xff})} {
		if validModerationReason(reason) {
			t.Fatalf("invalid reason %q was accepted", reason)
		}
	}
	if !validModerationReason("canonical") {
		t.Fatal("canonical reason was rejected")
	}
	for value, want := range map[string]policy.Role{"member": policy.RoleMember, "moderator": policy.RoleModerator, "administrator": policy.RoleAdministrator} {
		if got, valid := moderationUserRole(value); !valid || got != want {
			t.Fatalf("moderationUserRole(%q) = (%v, %t)", value, got, valid)
		}
	}
	if got, valid := moderationUserRole("invented"); valid || got != 0 {
		t.Fatalf("moderationUserRole(invented) = (%v, %t)", got, valid)
	}
	if validModerationUserTime(pgtype.Timestamptz{}) || !validModerationUserTime(createdAt) {
		t.Fatal("timestamp closure is incorrect")
	}
}

func moderationStatusTime(hour int) time.Time {
	return time.Date(2026, time.September, 2, hour, 0, 0, 0, time.UTC)
}

func moderationStatusTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

type moderationStatusTestQuerier struct {
	row    db.GetModerationUserStatusRow
	err    error
	params db.GetModerationUserStatusParams
	calls  int
}

func (querier *moderationStatusTestQuerier) GetModerationUserStatus(_ context.Context, params db.GetModerationUserStatusParams) (db.GetModerationUserStatusRow, error) {
	querier.calls++
	querier.params = params
	return querier.row, querier.err
}

type panicModerationStatusQuerier struct{}

func (panicModerationStatusQuerier) GetModerationUserStatus(context.Context, db.GetModerationUserStatusParams) (db.GetModerationUserStatusRow, error) {
	panic("moderation status query executed")
}
