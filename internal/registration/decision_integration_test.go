//go:build integration

package registration

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const decisionTestDatabase = "gotth_bb_beta109_registration_test"

type recordingGateway struct {
	operations []string
	failNext   error
}

func (gateway *recordingGateway) AddUser(_ context.Context, group, subject string) error {
	gateway.operations = append(gateway.operations, "add:"+group+":"+subject)
	return gateway.takeFailure()
}

func (gateway *recordingGateway) RemoveUser(_ context.Context, group, subject string) error {
	gateway.operations = append(gateway.operations, "remove:"+group+":"+subject)
	return gateway.takeFailure()
}

func (gateway *recordingGateway) takeFailure() error {
	err := gateway.failNext
	gateway.failNext = nil
	return err
}

func TestRegistrationDecisionsOnPostgreSQL17(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	connection := decisionTestConnection(t, ctx)

	var administratorID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users
		(display_name, role, authentik_sync_state) VALUES ('Administrator', 'administrator', 'accepted') RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	actor := policy.AccessContext{Authenticated: true, UserID: administratorID, Role: policy.RoleAdministrator}
	key := [32]byte{0x41}
	baseTime := time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC)
	clock := func() time.Time { return baseTime }

	approveSubject := "11111111-1111-4111-8111-111111111111"
	approveID := insertPendingRegistration(t, ctx, connection, 101, approveSubject, "Approve Me", "approve@example.test")
	approveRequest := pgtype.UUID{Bytes: [16]byte{0xa1}, Valid: true}
	gateway := &recordingGateway{}
	approved, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: approveID, Revision: 1, Decision: Approve,
		Reason: "Verified applicant", RequestID: approveRequest,
	}, key)
	if err != nil || approved.Status != "approved" || approved.Revision != 3 || approved.AuditID <= 0 {
		t.Fatalf("approve = (%+v, %v)", approved, err)
	}
	wantApproval := []string{"add:accepted:" + approveSubject, "remove:pending:" + approveSubject}
	if strings.Join(gateway.operations, "|") != strings.Join(wantApproval, "|") {
		t.Fatalf("approval operations = %v", gateway.operations)
	}
	beforeRetry := len(gateway.operations)
	retried, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: approveID, Revision: 1, Decision: Approve,
		Reason: "Verified applicant", RequestID: approveRequest,
	}, key)
	if err != nil || retried.Status != "approved" || retried.Revision != 3 || len(gateway.operations) != beforeRetry {
		t.Fatalf("terminal retry = (%+v, %v, operations %v)", retried, err, gateway.operations)
	}

	rejectSubject := "22222222-2222-4222-8222-222222222222"
	rejectID := insertPendingRegistration(t, ctx, connection, 102, rejectSubject, "Reject Me", "reject@example.test")
	rejected, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: rejectID, Revision: 1, Decision: Reject,
		Reason: "Application rejected", RequestID: pgtype.UUID{Bytes: [16]byte{0xb2}, Valid: true},
	}, key)
	if err != nil || rejected.Status != "rejected" || rejected.Revision != 3 {
		t.Fatalf("reject = (%+v, %v)", rejected, err)
	}
	lastTwo := gateway.operations[len(gateway.operations)-2:]
	wantRejection := []string{"remove:accepted:" + rejectSubject, "remove:pending:" + rejectSubject}
	if strings.Join(lastTwo, "|") != strings.Join(wantRejection, "|") {
		t.Fatalf("rejection operations = %v", lastTwo)
	}

	retrySubject := "33333333-3333-4333-8333-333333333333"
	retryID := insertPendingRegistration(t, ctx, connection, 103, retrySubject, "Retry Me", "retry@example.test")
	retryRequest := pgtype.UUID{Bytes: [16]byte{0xc3}, Valid: true}
	gateway.failNext = authentikgateway.ErrRemoteUnavailable
	failed, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: retryID, Revision: 1, Decision: Approve,
		Reason: "Retry a bounded failure", RequestID: retryRequest,
	}, key)
	if !errors.Is(err, ErrRemote) || failed != (DecisionResult{}) {
		t.Fatalf("remote failure = (%+v, %v)", failed, err)
	}
	assertRegistrationState(t, ctx, connection, retryID, "approval_required", 3, "remote_unavailable")
	recovered, err := Decide(ctx, connection, gateway, clock, actor, DecisionInput{
		RegistrationID: retryID, Revision: 1, Decision: Approve,
		Reason: "Retry a bounded failure", RequestID: retryRequest,
	}, key)
	if err != nil || recovered.Status != "approved" || recovered.Revision != 4 {
		t.Fatalf("recovered decision = (%+v, %v)", recovered, err)
	}

	for _, forbidden := range []string{"approve@example.test", "Approve Me", approveSubject, "authentik_user_id"} {
		var count int
		if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions
			WHERE previous_state::text LIKE '%' || $1 || '%' OR resulting_state::text LIKE '%' || $1 || '%'`, forbidden).Scan(&count); err != nil || count != 0 {
			t.Fatalf("audit leaked %q: count=%d error=%v", forbidden, count, err)
		}
	}
	var actionCount int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.moderation_actions
		WHERE action_type IN ('request_registration_approval', 'approve_registration',
		'request_registration_rejection', 'reject_registration', 'record_registration_transition_result')`).Scan(&actionCount); err != nil || actionCount != 7 {
		t.Fatalf("decision audit count = (%d, %v), want 7", actionCount, err)
	}
}

func decisionTestConnection(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+decisionTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+decisionTestDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+decisionTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = decisionTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	return connection
}

func insertPendingRegistration(t *testing.T, ctx context.Context, connection *pgx.Conn, remoteID int64, subject, name, email string) int64 {
	t.Helper()
	var id int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.pending_registrations
		(authentik_user_id, authentik_subject, display_name, verified_email, intake_at)
		VALUES ($1, $2, $3, $4, '2026-09-09T17:00:00Z') RETURNING id`, remoteID, subject, name, email).Scan(&id); err != nil {
		t.Fatalf("insert pending registration %s: %v", subject, err)
	}
	return id
}

func assertRegistrationState(t *testing.T, ctx context.Context, connection *pgx.Conn, id int64, status string, revision int64, failure string) {
	t.Helper()
	var gotStatus, gotFailure string
	var gotRevision int64
	if err := connection.QueryRow(ctx, `SELECT status, administration_revision, reconciliation_class
		FROM public.pending_registrations WHERE id = $1`, id).Scan(&gotStatus, &gotRevision, &gotFailure); err != nil || gotStatus != status || gotRevision != revision || gotFailure != failure {
		t.Fatalf("registration state = (%q, %d, %q, %v), want (%q, %d, %q)", gotStatus, gotRevision, gotFailure, err, status, revision, failure)
	}
}

var _ Gateway = (*recordingGateway)(nil)
