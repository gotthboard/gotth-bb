//go:build integration

package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	controlTestDatabase = "gotth_bb_b109_control_test"
	controlRuntimeRole  = "gotth_bb_b109_control_runtime"
)

func TestControlMutationIsAtomicAuditedAndSerializedOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+controlTestDatabase+" WITH (FORCE)")
	_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+controlRuntimeRole)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+controlRuntimeRole+" NOLOGIN"); err != nil {
		t.Fatalf("create runtime role: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+controlTestDatabase); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+controlTestDatabase+" WITH (FORCE)")
		_, _ = admin.Exec(cleanup, "DROP ROLE IF EXISTS "+controlRuntimeRole)
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = controlTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	owner, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	var administratorID int64
	if err := owner.QueryRow(ctx, `INSERT INTO public.users (display_name, role, authentik_sync_state)
		VALUES ('Control Administrator', 'administrator', 'accepted') RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	sessionHash := bytes.Repeat([]byte{0x71}, 32)
	observedAt := time.Date(2026, time.September, 9, 16, 30, 0, 0, time.UTC)
	if _, err := owner.Exec(ctx, `INSERT INTO public.sessions (
		token_hash, user_id, issued_at, last_seen_at, validated_at, expires_at
	) VALUES ($1, $2, $3, $4, $3, $5)`, sessionHash, administratorID,
		observedAt.Add(-time.Hour), observedAt.Add(-20*time.Minute), observedAt.Add(time.Hour)); err != nil {
		t.Fatalf("insert dynamic-policy session: %v", err)
	}
	queries := db.New(owner)
	if active, err := queries.GetActiveSessionWithControl(ctx, db.GetActiveSessionWithControlParams{
		TokenHash: sessionHash, ObservedAt: pgtype.Timestamptz{Time: observedAt, Valid: true},
	}); err != nil || active.SessionIdleSeconds != 28800 {
		t.Fatalf("default dynamic session = (%+v, %v)", active, err)
	}
	grants, err := os.ReadFile("../../deploy/postgresql/runtime-grants.sql")
	if err != nil {
		t.Fatalf("read runtime grants: %v", err)
	}
	roleIdentifier := pgx.Identifier{controlRuntimeRole}.Sanitize()
	if _, err := owner.Exec(ctx, strings.ReplaceAll(string(grants), `:"runtime_role"`, roleIdentifier)); err != nil {
		t.Fatalf("apply runtime grants: %v", err)
	}
	actor := policy.AccessContext{Authenticated: true, UserID: administratorID, Role: policy.RoleAdministrator}
	input := testControlInput()
	input.Revision = 1
	input.SessionIdle = 10 * time.Minute
	input.MaintenanceMessage = "Private planned maintenance"
	if _, err := owner.Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("set runtime role: %v", err)
	}
	result, err := Update(ctx, owner, func() time.Time { return observedAt }, actor, input, testCeilings(), false, pgtype.UUID{Bytes: [16]byte{1}, Valid: true})
	if _, resetErr := owner.Exec(ctx, "RESET ROLE"); resetErr != nil {
		t.Fatalf("reset runtime role: %v", resetErr)
	}
	if err != nil || result.Revision != 2 || result.AuditID <= 0 {
		t.Fatalf("Update() = (%+v, %v)", result, err)
	}
	if active, err := queries.GetActiveSessionWithControl(ctx, db.GetActiveSessionWithControlParams{
		TokenHash: sessionHash, ObservedAt: pgtype.Timestamptz{Time: observedAt, Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tightened dynamic session = (%+v, %v), want no rows", active, err)
	}
	var revision int64
	var previous, resulting string
	if err := owner.QueryRow(ctx, `SELECT settings.administration_revision,
		action.previous_state::text, action.resulting_state::text
		FROM public.site_settings AS settings
		JOIN public.moderation_actions AS action ON action.id = $1
		WHERE settings.singleton`, result.AuditID).Scan(&revision, &previous, &resulting); err != nil {
		t.Fatalf("load changed state: %v", err)
	}
	if revision != 2 || strings.Contains(previous, "Private") || strings.Contains(resulting, "Private") ||
		!strings.Contains(resulting, "maintenance_message_sha256") {
		t.Fatalf("changed state = (revision %d, previous %q, resulting %q)", revision, previous, resulting)
	}

	if _, err := owner.Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("set role for stale mutation: %v", err)
	}
	stale, staleErr := Update(ctx, owner, func() time.Time { return observedAt.Add(time.Second) }, actor, input, testCeilings(), false, pgtype.UUID{Bytes: [16]byte{2}, Valid: true})
	_, _ = owner.Exec(ctx, "RESET ROLE")
	if stale != (MutationResult{}) || !errors.Is(staleErr, ErrConflict) {
		t.Fatalf("stale Update() = (%+v, %v)", stale, staleErr)
	}
	noOpInput := input
	noOpInput.Revision = 2
	if _, err := owner.Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("set role for no-op mutation: %v", err)
	}
	noOp, noOpErr := Update(ctx, owner, func() time.Time { return observedAt.Add(1500 * time.Millisecond) }, actor, noOpInput, testCeilings(), false, pgtype.UUID{Bytes: [16]byte{22}, Valid: true})
	_, _ = owner.Exec(ctx, "RESET ROLE")
	if noOp != (MutationResult{}) || !errors.Is(noOpErr, ErrConflict) {
		t.Fatalf("no-op Update() = (%+v, %v)", noOp, noOpErr)
	}
	if _, err := owner.Exec(ctx, `CREATE FUNCTION public.reject_control_audit()
		RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private audit failure'; END $$;
		CREATE TRIGGER reject_control_audit BEFORE INSERT ON public.moderation_actions
		FOR EACH ROW WHEN (NEW.action_type = 'update_control_settings')
		EXECUTE FUNCTION public.reject_control_audit()`); err != nil {
		t.Fatalf("install audit rejection: %v", err)
	}
	auditFailureInput := noOpInput
	auditFailureInput.MaintenanceEnabled = true
	if _, err := owner.Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("set role for audit failure: %v", err)
	}
	auditFailure, auditFailureErr := Update(ctx, owner, func() time.Time { return observedAt.Add(1750 * time.Millisecond) }, actor, auditFailureInput, testCeilings(), false, pgtype.UUID{Bytes: [16]byte{23}, Valid: true})
	_, _ = owner.Exec(ctx, "RESET ROLE")
	if auditFailure != (MutationResult{}) || auditFailureErr == nil || strings.Contains(auditFailureErr.Error(), "private audit failure") {
		t.Fatalf("audit-failure Update() = (%+v, %v)", auditFailure, auditFailureErr)
	}
	if err := owner.QueryRow(ctx, `SELECT administration_revision FROM public.site_settings WHERE singleton`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("audit-failure rollback revision = (%d, %v)", revision, err)
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER reject_control_audit ON public.moderation_actions;
		DROP FUNCTION public.reject_control_audit()`); err != nil {
		t.Fatalf("remove audit rejection: %v", err)
	}

	if _, err := owner.Exec(ctx, `UPDATE public.users SET authentik_sync_state = 'removal_required' WHERE id = $1`, administratorID); err != nil {
		t.Fatalf("revoke administrator sync state: %v", err)
	}
	deniedInput := input
	deniedInput.Revision = 2
	deniedInput.MaintenanceEnabled = true
	if _, err := owner.Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
		t.Fatalf("set role for denied mutation: %v", err)
	}
	denied, deniedErr := Update(ctx, owner, func() time.Time { return observedAt.Add(2 * time.Second) }, actor, deniedInput, testCeilings(), false, pgtype.UUID{Bytes: [16]byte{3}, Valid: true})
	_, _ = owner.Exec(ctx, "RESET ROLE")
	if denied != (MutationResult{}) || !errors.Is(deniedErr, ErrDenied) {
		t.Fatalf("revoked-actor Update() = (%+v, %v)", denied, deniedErr)
	}
	if _, err := owner.Exec(ctx, `UPDATE public.users SET authentik_sync_state = 'accepted' WHERE id = $1`, administratorID); err != nil {
		t.Fatalf("restore administrator sync state: %v", err)
	}

	connections := make([]*pgx.Conn, 2)
	for index := range connections {
		connections[index], err = pgx.ConnectConfig(ctx, testConfig)
		if err != nil {
			t.Fatalf("connect concurrent writer %d: %v", index, err)
		}
		defer connections[index].Close(context.Background())
		if _, err := connections[index].Exec(ctx, "SET ROLE "+roleIdentifier); err != nil {
			t.Fatalf("set concurrent role %d: %v", index, err)
		}
	}
	results := make([]MutationResult, 2)
	errorsSeen := make([]error, 2)
	var writers sync.WaitGroup
	for index := range connections {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			candidate := deniedInput
			candidate.MaintenanceMessage = "Concurrent maintenance " + string(rune('A'+index))
			results[index], errorsSeen[index] = Update(ctx, connections[index], func() time.Time {
				return observedAt.Add(time.Duration(3+index) * time.Second)
			}, actor, candidate, testCeilings(), false, pgtype.UUID{Bytes: [16]byte{byte(4 + index)}, Valid: true})
		}(index)
	}
	writers.Wait()
	successes, conflicts := 0, 0
	for index := range results {
		if errorsSeen[index] == nil && results[index].Revision == 3 {
			successes++
		} else if results[index] == (MutationResult{}) {
			var postgresError *pgconn.PgError
			if errors.Is(errorsSeen[index], ErrConflict) ||
				(errors.As(errorsSeen[index], &postgresError) && postgresError.Code == "55P03") {
				conflicts++
			}
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent updates = (results %+v, errors %+v)", results, errorsSeen)
	}

	unknownCommit := errors.New("simulated lost commit acknowledgement")
	unknownInput := deniedInput
	unknownInput.Revision = 3
	unknownInput.MaintenanceMessage = "Unknown commit remains inspectable"
	unknownResult, unknownErr := Update(
		ctx,
		controlUnknownCommitBeginner{connection: connections[0], commitErr: unknownCommit},
		func() time.Time { return observedAt.Add(10 * time.Second) },
		actor,
		unknownInput,
		testCeilings(),
		false,
		pgtype.UUID{Bytes: [16]byte{6}, Valid: true},
	)
	if unknownResult != (MutationResult{}) || !errors.Is(unknownErr, unknownCommit) {
		t.Fatalf("unknown-commit Update() = (%+v, %v)", unknownResult, unknownErr)
	}
	var message string
	var auditCount int
	if err := owner.QueryRow(ctx, `SELECT administration_revision, maintenance_message,
		(SELECT count(*) FROM public.moderation_actions WHERE action_type='update_control_settings')
		FROM public.site_settings WHERE singleton`).Scan(&revision, &message, &auditCount); err != nil {
		t.Fatalf("inspect unknown commit: %v", err)
	}
	if revision != 4 || message != unknownInput.MaintenanceMessage || auditCount != 3 {
		t.Fatalf("unknown commit state = (revision %d, message %q, audits %d)", revision, message, auditCount)
	}
	if _, err := owner.Exec(ctx, `UPDATE public.site_settings
		SET administration_revision=9223372036854775807 WHERE singleton`); err != nil {
		t.Fatalf("install overflow fixture: %v", err)
	}
	overflowInput := unknownInput
	overflowInput.Revision = int64(^uint64(0) >> 1)
	overflowInput.MaintenanceMessage = "Overflow must not wrap"
	overflowResult, overflowErr := Update(
		ctx, connections[0], func() time.Time { return observedAt.Add(11 * time.Second) },
		actor, overflowInput, testCeilings(), false,
		pgtype.UUID{Bytes: [16]byte{7}, Valid: true},
	)
	if overflowResult != (MutationResult{}) || !errors.Is(overflowErr, ErrConflict) {
		t.Fatalf("overflow Update() = (%+v, %v)", overflowResult, overflowErr)
	}
	if err := owner.QueryRow(ctx, `SELECT administration_revision,
		(SELECT count(*) FROM public.moderation_actions WHERE action_type='update_control_settings')
		FROM public.site_settings WHERE singleton`).Scan(&revision, &auditCount); err != nil ||
		revision != int64(^uint64(0)>>1) || auditCount != 3 {
		t.Fatalf("overflow state = (revision %d, audits %d, error %v)", revision, auditCount, err)
	}
}

type controlUnknownCommitBeginner struct {
	connection *pgx.Conn
	commitErr  error
}

func (beginner controlUnknownCommitBeginner) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := beginner.connection.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &controlUnknownCommitTx{Tx: tx, commitErr: beginner.commitErr}, nil
}

type controlUnknownCommitTx struct {
	pgx.Tx
	commitErr error
}

func (tx *controlUnknownCommitTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return tx.commitErr
}
