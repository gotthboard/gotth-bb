package administration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type emailAdministrationQuerierFunc func(context.Context, db.LoadEmailTestStateForAdministrationParams) (db.LoadEmailTestStateForAdministrationRow, error)

func (query emailAdministrationQuerierFunc) LoadEmailTestStateForAdministration(ctx context.Context, input db.LoadEmailTestStateForAdministrationParams) (db.LoadEmailTestStateForAdministrationRow, error) {
	return query(ctx, input)
}

func TestLoadEmailTestStateAcceptsAbsentAndCanonicalState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	for _, test := range []struct {
		name string
		row  db.LoadEmailTestStateForAdministrationRow
		want EmailTestState
	}{
		{name: "absent", row: db.LoadEmailTestStateForAdministrationRow{ActorPresent: true}},
		{name: "accepted", row: db.LoadEmailTestStateForAdministrationRow{
			ActorPresent: true, StatePresent: true, Status: "accepted",
			RequestedAt:   pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true},
			CompletedAt:   pgtype.Timestamptz{Time: now, Valid: true},
			NextAllowedAt: pgtype.Timestamptz{Time: now.Add(5*time.Minute - time.Second), Valid: true},
		}, want: EmailTestState{Status: "accepted", RequestedAt: now.Add(-time.Second), CompletedAt: now, NextAllowedAt: now.Add(5*time.Minute - time.Second)}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := emailAdministrationQuerierFunc(func(context.Context, db.LoadEmailTestStateForAdministrationParams) (db.LoadEmailTestStateForAdministrationRow, error) {
				return test.row, nil
			})
			got, err := LoadEmailTestState(context.Background(), query, actor, now)
			if err != nil || got != test.want {
				t.Fatalf("state = (%+v, %v), want %+v", got, err, test.want)
			}
		})
	}
}

func TestLoadEmailTestStateFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	admin := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	base := db.LoadEmailTestStateForAdministrationRow{
		ActorPresent: true, StatePresent: true, Status: "requested",
		RequestedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		NextAllowedAt: pgtype.Timestamptz{Time: now.Add(5 * time.Minute), Valid: true},
	}
	for _, test := range []struct {
		name  string
		actor policy.AccessContext
		row   db.LoadEmailTestStateForAdministrationRow
		err   error
		want  error
	}{
		{name: "policy", actor: policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleMember}, want: ErrAccountAdministrationDenied},
		{name: "database", actor: admin, err: errors.New("failed"), want: ErrAccountAdministrationUnavailable},
		{name: "missing actor", actor: admin, row: func() db.LoadEmailTestStateForAdministrationRow { row := base; row.ActorPresent = false; return row }(), want: ErrAccountAdministrationDenied},
		{name: "invalid status", actor: admin, row: func() db.LoadEmailTestStateForAdministrationRow { row := base; row.Status = "queued"; return row }(), want: ErrAccountAdministrationUnavailable},
		{name: "early throttle", actor: admin, row: func() db.LoadEmailTestStateForAdministrationRow {
			row := base
			row.NextAllowedAt.Time = now.Add(4 * time.Minute)
			return row
		}(), want: ErrAccountAdministrationUnavailable},
		{name: "requested completed", actor: admin, row: func() db.LoadEmailTestStateForAdministrationRow {
			row := base
			row.CompletedAt = pgtype.Timestamptz{Time: now, Valid: true}
			return row
		}(), want: ErrAccountAdministrationUnavailable},
		{name: "future request", actor: admin, row: func() db.LoadEmailTestStateForAdministrationRow {
			row := base
			row.RequestedAt.Time = now.Add(time.Second)
			row.NextAllowedAt.Time = now.Add(5*time.Minute + time.Second)
			return row
		}(), want: ErrAccountAdministrationUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := emailAdministrationQuerierFunc(func(context.Context, db.LoadEmailTestStateForAdministrationParams) (db.LoadEmailTestStateForAdministrationRow, error) {
				return test.row, test.err
			})
			_, err := LoadEmailTestState(context.Background(), query, test.actor, now)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEmailHelpersAreStrict(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"Admin <admin@example.test>", " admin@example.test", "admin@example.test ", "not-an-address"} {
		if validAddressOnly(address) {
			t.Fatalf("validAddressOnly(%q) = true", address)
		}
	}
	if !validAddressOnly("admin@example.test") {
		t.Fatal("plain address rejected")
	}
	if _, err := randomUUID(errorReader{err: errors.New("unreadable")}); err == nil {
		t.Fatal("randomUUID accepted failing source")
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
