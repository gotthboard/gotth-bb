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

type sessionAdministrationQuerierFunc func(context.Context, db.ListSessionsForAdministrationParams) ([]db.ListSessionsForAdministrationRow, error)

func (query sessionAdministrationQuerierFunc) ListSessionsForAdministration(ctx context.Context, input db.ListSessionsForAdministrationParams) ([]db.ListSessionsForAdministrationRow, error) {
	return query(ctx, input)
}

func TestListSessionsReturnsOnlyBoundedPresentationFields(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	actor := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	query := sessionAdministrationQuerierFunc(func(ctx context.Context, input db.ListSessionsForAdministrationParams) ([]db.ListSessionsForAdministrationRow, error) {
		if ctx == nil || input.ActorUserID != 7 || input.TargetUserID != 11 || input.PageLimit != 51 || !input.ObservedAt.Time.Equal(now) {
			t.Fatalf("query input = %+v", input)
		}
		rows := make([]db.ListSessionsForAdministrationRow, 51)
		for index := range rows {
			issued := now.Add(-time.Duration(index+1) * time.Minute)
			rows[index] = validSessionAdministrationRow(11, int64(100-index), issued, now)
		}
		return rows, nil
	})
	page, err := ListSessions(context.Background(), query, actor, now, 11)
	if err != nil {
		t.Fatal(err)
	}
	if page.DisplayName != "Target User" || page.Revision != 4 || !page.More || len(page.Sessions) != 50 || page.Sessions[0].ID != 100 || page.Sessions[49].ID != 51 {
		t.Fatalf("page = %+v", page)
	}
}

func TestListSessionsFailsClosedOnAuthorityAndMalformedRows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 12, 19, 0, 0, 0, time.UTC)
	admin := policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleAdministrator}
	valid := validSessionAdministrationRow(11, 9, now.Add(-time.Minute), now)
	for _, test := range []struct {
		name  string
		actor policy.AccessContext
		rows  []db.ListSessionsForAdministrationRow
		err   error
		want  error
	}{
		{name: "policy", actor: policy.AccessContext{Authenticated: true, UserID: 7, Role: policy.RoleMember}, want: ErrAccountAdministrationDenied},
		{name: "database", actor: admin, err: errors.New("failed"), want: ErrAccountAdministrationUnavailable},
		{name: "missing actor", actor: admin, rows: []db.ListSessionsForAdministrationRow{{}}, want: ErrAccountAdministrationDenied},
		{name: "missing target", actor: admin, rows: []db.ListSessionsForAdministrationRow{{ActorPresent: true, SettingsPresent: true}}, want: ErrAccountAdministrationNotFound},
		{name: "non descending", actor: admin, rows: []db.ListSessionsForAdministrationRow{valid, valid}, want: ErrAccountAdministrationUnavailable},
		{name: "future issued", actor: admin, rows: []db.ListSessionsForAdministrationRow{validSessionAdministrationRow(11, 9, now.Add(time.Second), now)}, want: ErrAccountAdministrationUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := sessionAdministrationQuerierFunc(func(context.Context, db.ListSessionsForAdministrationParams) ([]db.ListSessionsForAdministrationRow, error) {
				return test.rows, test.err
			})
			_, err := ListSessions(context.Background(), query, test.actor, now, 11)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func validSessionAdministrationRow(targetID, sessionID int64, issued, now time.Time) db.ListSessionsForAdministrationRow {
	return db.ListSessionsForAdministrationRow{
		ActorPresent: true, TargetPresent: true, SettingsPresent: true,
		TargetUserID: targetID, DisplayName: "Target User", TargetRevision: 4,
		SessionPresent: true, SessionID: sessionID,
		IssuedAt:    pgtype.Timestamptz{Time: issued, Valid: true},
		LastSeenAt:  pgtype.Timestamptz{Time: issued.Add(10 * time.Second), Valid: true},
		ValidatedAt: pgtype.Timestamptz{Time: issued.Add(20 * time.Second), Valid: true},
		ExpiresAt:   pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
	}
}
