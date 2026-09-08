package db

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestAccountAdministrationProjectionQueriesFenceAuthorityAndBindLimits(t *testing.T) {
	t.Parallel()
	observedAt := pgtype.Timestamptz{Valid: true}
	for _, test := range []struct {
		name     string
		invoke   func(*Queries) error
		wantArgs []any
		required []string
	}{
		{
			name: "accounts", wantArgs: []any{observedAt, int64(7), int64(50), int32(51)},
			invoke: func(queries *Queries) error {
				_, err := queries.ListAccountsForAdministration(context.Background(), ListAccountsForAdministrationParams{ObservedAt: observedAt, ActorUserID: 7, AfterUserID: 50, PageLimit: 51})
				return err
			},
			required: []string{"actor AS MATERIALIZED", "forum_user.muted_until IS NULL OR forum_user.muted_until <= $1", "JOIN LATERAL", "FROM actor", "ORDER BY forum_user.id", "LIMIT $4", "LEFT JOIN account ON true"},
		},
		{
			name: "account groups", wantArgs: []any{int64(7), observedAt, int64(41), int64(50), int32(51)},
			invoke: func(queries *Queries) error {
				_, err := queries.ListAccountGroupsForAdministration(context.Background(), ListAccountGroupsForAdministrationParams{ActorUserID: 7, ObservedAt: observedAt, TargetUserID: 41, AfterGroupID: 50, PageLimit: 51})
				return err
			},
			required: []string{"actor AS MATERIALIZED", "forum_user.muted_until IS NULL OR forum_user.muted_until <= $2", "target AS MATERIALIZED", "FROM target", "JOIN LATERAL", "LIMIT $5", "EXISTS", "membership.user_id = target.id", "membership.group_id = forum_group.id"},
		},
		{
			name: "account detail", wantArgs: []any{observedAt, int64(7), int64(41)},
			invoke: func(queries *Queries) error {
				_, err := queries.LoadAccountForAdministration(context.Background(), LoadAccountForAdministrationParams{ObservedAt: observedAt, ActorUserID: 7, TargetUserID: 41})
				return err
			},
			required: []string{"actor AS MATERIALIZED", "forum_user.muted_until IS NULL OR forum_user.muted_until <= $1", "target AS MATERIALIZED", "FROM actor", "forum_user.id = $3", "LEFT JOIN target ON true"},
		},
		{
			name: "groups", wantArgs: []any{int64(7), observedAt, int64(50), int32(51)},
			invoke: func(queries *Queries) error {
				_, err := queries.ListGroupsForAdministration(context.Background(), ListGroupsForAdministrationParams{ActorUserID: 7, ObservedAt: observedAt, AfterGroupID: 50, PageLimit: 51})
				return err
			},
			required: []string{"actor AS MATERIALIZED", "forum_user.muted_until IS NULL OR forum_user.muted_until <= $2", "FROM actor", "JOIN LATERAL", "ORDER BY group_row.id", "LIMIT $4"},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &publishingDBTX{
				rows: &publishingRows{},
				row: publishingRow{values: []any{
					true, int64(41), "Account", "member", false, observedAt, observedAt, int64(1),
				}},
			}
			if err := test.invoke(New(database)); err != nil || !reflect.DeepEqual(database.args, test.wantArgs) {
				t.Fatalf("projection query = (error %v, args %#v)", err, database.args)
			}
			for _, required := range test.required {
				if !strings.Contains(database.query, required) {
					t.Fatalf("projection SQL lacks %q", required)
				}
			}
		})
	}
}

func TestAccountAdministrationMutationsRemainAtomicAndRevisionGuarded(t *testing.T) {
	t.Parallel()
	database := &publishingDBTX{row: publishingRow{values: []any{int64(41), "moderator", int64(8), int64(91), int64(3)}}}
	parameters := ChangeAdministrationRoleAndAuditParams{
		Role: "moderator", ObservedAt: pgtype.Timestamptz{Valid: true}, TargetUserID: 41,
		ExpectedRole: "member", ExpectedRevision: 7, ActorUserID: pgtype.Int8{Int64: 7, Valid: true},
		Reason: pgtype.Text{String: "Promote the account", Valid: true}, RequestID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	changed, err := New(database).ChangeAdministrationRoleAndAudit(context.Background(), parameters)
	if err != nil || changed != (ChangeAdministrationRoleAndAuditRow{UserID: 41, Role: "moderator", AdministrationRevision: 8, AuditID: 91, RevokedSessions: 3}) {
		t.Fatalf("ChangeAdministrationRoleAndAudit() = (%+v, %v)", changed, err)
	}
	for _, required := range []string{
		"WITH changed AS", "administration_revision = forum_user.administration_revision + 1",
		"forum_user.administration_revision = $5", "INSERT INTO public.moderation_actions", "'change_role'",
		"UPDATE public.sessions AS session", "session.revoked_at IS NULL", "FROM changed",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("role mutation SQL lacks %q", required)
		}
	}

	database.row = publishingRow{values: []any{int64(41), int64(9), int64(92)}}
	_, err = New(database).GrantAdministrationMembershipAndAudit(context.Background(), GrantAdministrationMembershipAndAuditParams{
		GroupID: 3, TargetUserID: 41, ActorUserID: 7, ObservedAt: pgtype.Timestamptz{Valid: true}, ExpectedRevision: 8,
		Reason: pgtype.Text{String: "Grant access", Valid: true}, RequestID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
	})
	if err != nil {
		t.Fatalf("GrantAdministrationMembershipAndAudit() error = %v", err)
	}
	for _, required := range []string{"ON CONFLICT (group_id, user_id) DO NOTHING", "FROM mapping", "administration_revision = $5", "'grant_group_membership'"} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("membership mutation SQL lacks %q", required)
		}
	}
}
