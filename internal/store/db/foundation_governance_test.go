package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestBootstrapAdministratorAndAuditBindsAndScansExactResult(t *testing.T) {
	t.Parallel()

	atTime := pgtype.Timestamptz{Time: time.Date(2026, time.September, 1, 22, 55, 0, 0, time.UTC), Valid: true}
	requestID := pgtype.UUID{Bytes: [16]byte{0x10, 0x20, 0x30}, Valid: true}
	database := &governanceQueryDBTX{row: governanceQueryRow{values: []int64{41, 73}}}
	ctx := context.WithValue(context.Background(), governanceQueryContextKey{}, "preserved")
	operatorIdentifier := pgtype.Text{String: "operator@example.test", Valid: true}
	got, err := New(database).BootstrapAdministratorAndAudit(ctx, BootstrapAdministratorAndAuditParams{
		UserID: 41, AtTime: atTime, OperatorIdentifier: operatorIdentifier, RequestID: requestID,
	})
	if err != nil || got.UserID != 41 || got.AuditID != 73 {
		t.Fatalf("BootstrapAdministratorAndAudit() = (%+v, %v)", got, err)
	}
	if database.ctx != ctx || database.query != bootstrapAdministratorAndAudit || len(database.args) != 4 ||
		database.args[0] != int64(41) || !reflect.DeepEqual(database.args[1], atTime) ||
		!reflect.DeepEqual(database.args[2], operatorIdentifier) || !reflect.DeepEqual(database.args[3], requestID) {
		t.Fatalf("query call = (context %v, query %q, args %#v)", database.ctx, database.query, database.args)
	}
	for _, required := range []string{
		"FOR UPDATE OF forum_user", "role = 'administrator'", "actor_kind", "'operator'",
		"'bootstrap_administrator'", "jsonb_build_object('role', target.previous_role)",
		"jsonb_build_object('role', 'administrator')",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("bootstrap query lacks %q", required)
		}
	}
}

func TestBootstrapAdministratorAndAuditReturnsScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	database := &governanceQueryDBTX{row: governanceQueryRow{err: cause}}
	got, err := New(database).BootstrapAdministratorAndAudit(context.Background(), BootstrapAdministratorAndAuditParams{})
	if !errors.Is(err, cause) || got != (BootstrapAdministratorAndAuditRow{}) {
		t.Fatalf("BootstrapAdministratorAndAudit() = (%+v, %v), want zero/cause", got, err)
	}
}

func TestClaimInitialAdministratorAndAuditBindsRevokesAndAuditsExactSession(t *testing.T) {
	t.Parallel()
	atTime := pgtype.Timestamptz{Time: time.Date(2026, time.September, 2, 15, 0, 0, 0, time.UTC), Valid: true}
	requestID := pgtype.UUID{Bytes: [16]byte{0x40, 0x50}, Valid: true}
	database := &governanceQueryDBTX{row: governanceQueryRow{values: []int64{41, 73, 91}}}
	got, err := New(database).ClaimInitialAdministratorAndAudit(context.Background(), ClaimInitialAdministratorAndAuditParams{
		SessionID: 91, UserID: 41, AtTime: atTime, Issuer: "issuer", Subject: "subject", RequestID: requestID,
	})
	if err != nil || got != (ClaimInitialAdministratorAndAuditRow{UserID: 41, AuditID: 73, RevokedSessionID: 91}) {
		t.Fatalf("ClaimInitialAdministratorAndAudit() = (%+v, %v)", got, err)
	}
	if len(database.args) != 6 || database.args[0] != int64(91) || database.args[1] != int64(41) ||
		!reflect.DeepEqual(database.args[2], atTime) || database.args[3] != "issuer" || database.args[4] != "subject" || !reflect.DeepEqual(database.args[5], requestID) {
		t.Fatalf("query args = %#v", database.args)
	}
	for _, required := range []string{
		"FOR UPDATE OF session", "FOR UPDATE OF forum_user, identity", "session.revoked_at IS NULL",
		"SET role = 'administrator'", "SET revoked_at", "'forum_user'", "'bootstrap_administrator'",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("claim query lacks %q", required)
		}
	}
}

type governanceQueryContextKey struct{}

type governanceQueryDBTX struct {
	DBTX
	ctx   context.Context
	query string
	args  []any
	row   pgx.Row
}

func (database *governanceQueryDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.ctx = ctx
	database.query = query
	database.args = append([]any(nil), args...)
	return database.row
}

type governanceQueryRow struct {
	values []int64
	err    error
}

func (row governanceQueryRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*(destinations[0].(*int64)) = row.values[0]
	*(destinations[1].(*int64)) = row.values[1]
	if len(destinations) == 3 {
		*(destinations[2].(*int64)) = row.values[2]
	}
	return nil
}
