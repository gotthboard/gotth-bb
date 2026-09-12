package moderation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestCreateReportCommitsVisibleTarget(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 6, 18, 0, 0, 123456789, time.UTC)
	tx := &reportServiceTestTx{mode: "create", actor: activeSuspensionTarget(11, "member", testCreatedAt(), testCreatedAt()), reportID: 71, targetID: 41}
	result, err := CreateReport(context.Background(), reportServiceTestBeginner{tx}, func() time.Time { return at }, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, ReportTopic, 41, "Visible concern")
	if err != nil || result.ReportID != 71 || result.Target != ReportTopic || result.TargetID != 41 || result.Status != "open" || !tx.committed || tx.rolledBack {
		t.Fatalf("CreateReport() = (%+v, %v), tx %+v", result, err, tx)
	}
}

func TestCreateReportEnforcesCapBeforeInsert(t *testing.T) {
	t.Parallel()
	tx := &reportServiceTestTx{mode: "create", actor: activeSuspensionTarget(11, "member", testCreatedAt(), testCreatedAt()), activeReports: 25}
	result, err := CreateReport(context.Background(), reportServiceTestBeginner{tx}, testModerationNow, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, ReportPost, 41, "Concern")
	if result != (ReportResult{}) || !errors.Is(err, ErrReportLimit) || tx.insertCalls != 0 || tx.committed || !tx.rolledBack {
		t.Fatalf("capped CreateReport() = (%+v, %v), tx %+v", result, err, tx)
	}
}

func TestProcessReportClaimsStrictly(t *testing.T) {
	t.Parallel()
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	tx := &reportServiceTestTx{mode: "claim", actor: activeSuspensionTarget(11, "moderator", testCreatedAt(), testCreatedAt()), reportID: 71}
	result, err := ProcessReport(context.Background(), reportServiceTestBeginner{tx}, testModerationNow, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}, 71, ClaimReport, "", requestID)
	if err != nil || result != (ReportActionResult{ReportID: 71, Status: "in_review", AuditID: 91}) || !tx.committed || tx.rolledBack {
		t.Fatalf("ProcessReport(claim) = (%+v, %v), tx %+v", result, err, tx)
	}
}

func TestReportAndExtendedModerationDenyUnacceptedActor(t *testing.T) {
	t.Parallel()

	actorRow := activeSuspensionTarget(11, "moderator", testCreatedAt(), testCreatedAt())
	actorRow.AuthentikSyncState = "grant_required"
	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}

	reportTx := &reportServiceTestTx{mode: "claim", actor: actorRow, reportID: 71}
	result, err := ProcessReport(context.Background(), reportServiceTestBeginner{reportTx}, testModerationNow, actor, 71, ClaimReport, "", pgtype.UUID{Bytes: [16]byte{1}, Valid: true})
	if result != (ReportActionResult{}) || !errors.Is(err, ErrReportDenied) || reportTx.committed || !reportTx.rolledBack {
		t.Fatalf("ProcessReport(unaccepted actor) = (%+v, %v), tx %+v", result, err, reportTx)
	}

	extendedTx := &reportServiceTestTx{mode: "pin", actor: actorRow, targetID: 41}
	extended, err := ApplyExtendedAction(context.Background(), reportServiceTestBeginner{extendedTx}, testModerationNow, actor, ExtendedActionInput{Action: PinTopic, TargetID: 41, Reason: "Important"}, pgtype.UUID{Bytes: [16]byte{2}, Valid: true})
	if extended != (ExtendedActionResult{}) || !errors.Is(err, ErrUserModerationDenied) || extendedTx.committed || !extendedTx.rolledBack {
		t.Fatalf("ApplyExtendedAction(unaccepted actor) = (%+v, %v), tx %+v", extended, err, extendedTx)
	}
}

func TestReportBoundariesRejectMalformedInputBeforeTransaction(t *testing.T) {
	t.Parallel()
	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	staff := policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleModerator}
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	for _, run := range []func() error{
		func() error {
			_, err := CreateReport(context.Background(), panicReportBeginner{}, time.Now, actor, "bad", 1, "reason")
			return err
		},
		func() error {
			_, err := CreateReport(context.Background(), panicReportBeginner{}, time.Now, actor, ReportTopic, 0, "reason")
			return err
		},
		func() error {
			_, err := CreateReport(context.Background(), panicReportBeginner{}, time.Now, actor, ReportTopic, 1, " padded ")
			return err
		},
		func() error {
			_, err := CreateReport(context.Background(), panicReportBeginner{}, time.Now, actor, ReportTopic, 1, strings.Repeat("x", 2001))
			return err
		},
		func() error {
			_, err := ProcessReport(context.Background(), panicReportBeginner{}, time.Now, staff, 1, ClaimReport, "text", requestID)
			return err
		},
		func() error {
			_, err := ProcessReport(context.Background(), panicReportBeginner{}, time.Now, staff, 1, NoteReport, "", requestID)
			return err
		},
		func() error {
			_, err := ProcessReport(context.Background(), panicReportBeginner{}, time.Now, staff, 1, ResolveReport, "reason", pgtype.UUID{})
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatal("report boundary accepted malformed input")
		}
	}
}

func TestMapCreateReportErrorMatchesOnlyActiveTargetConstraints(t *testing.T) {
	t.Parallel()
	for _, constraint := range []string{
		"reports_open_topic_reporter_unique",
		"reports_open_post_reporter_unique",
		"reports_open_user_reporter_unique",
	} {
		err := &pgconn.PgError{Code: "23505", ConstraintName: constraint}
		if !errors.Is(mapCreateReportError(err), ErrReportDuplicate) {
			t.Fatalf("constraint %q was not mapped to duplicate report", constraint)
		}
	}
	primaryKey := &pgconn.PgError{Code: "23505", ConstraintName: "reports_pkey"}
	if mapped := mapCreateReportError(primaryKey); errors.Is(mapped, ErrReportDuplicate) || !errors.Is(mapped, primaryKey) {
		t.Fatalf("primary-key violation mapped as %v", mapped)
	}
}

func TestApplyExtendedActionPinsTopic(t *testing.T) {
	t.Parallel()
	requestID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	tx := &reportServiceTestTx{mode: "pin", actor: activeSuspensionTarget(11, "moderator", testCreatedAt(), testCreatedAt()), targetID: 41}
	result, err := ApplyExtendedAction(context.Background(), reportServiceTestBeginner{tx}, testModerationNow, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}, ExtendedActionInput{Action: PinTopic, TargetID: 41, Reason: "Important"}, requestID)
	if err != nil || result != (ExtendedActionResult{Action: PinTopic, TargetID: 41, TopicID: 41, AuditID: 92}) || !tx.committed || tx.rolledBack {
		t.Fatalf("ApplyExtendedAction(pin) = (%+v, %v), tx %+v", result, err, tx)
	}
}

type panicReportBeginner struct{}

func (panicReportBeginner) Begin(context.Context) (pgx.Tx, error) {
	panic("transaction must not begin")
}

type reportServiceTestBeginner struct{ tx *reportServiceTestTx }

func (b reportServiceTestBeginner) Begin(context.Context) (pgx.Tx, error) { return b.tx, nil }

type reportServiceTestTx struct {
	pgx.Tx
	mode                  string
	actor                 db.LockUserForSuspensionRow
	reportID, targetID    int64
	activeReports         int32
	insertCalls           int
	committed, rolledBack bool
}

func (tx *reportServiceTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	if strings.Contains(query, "LockUserForSuspension") {
		return reportTestRow{values: []any{tx.actor.ID, tx.actor.Role, tx.actor.SuspendedAt, tx.actor.SuspendedUntil, tx.actor.SuspensionReason, tx.actor.MutedUntil, tx.actor.CreatedAt, tx.actor.UpdatedAt, tx.actor.AdministrationRevision, tx.actor.AuthentikSyncState}}
	}
	if strings.Contains(query, "CountActiveReportsByReporter") {
		return reportTestRow{values: []any{tx.activeReports}}
	}
	if strings.Contains(query, "CreateTopicReport") {
		tx.insertCalls++
		at := arguments[2].(pgtype.Timestamptz)
		return reportTestRow{values: []any{tx.reportID, int64(11), pgtype.Int8{Int64: tx.targetID, Valid: true}, pgtype.Int8{}, pgtype.Int8{}, "open", at}}
	}
	if strings.Contains(query, "LockReportForModeration") {
		return reportTestRow{values: []any{tx.reportID, "open", pgtype.Int8{}, pgtype.Timestamptz{Time: testCreatedAt(), Valid: true}, pgtype.Timestamptz{Time: testCreatedAt(), Valid: true}}}
	}
	if strings.Contains(query, "ClaimReportAndAudit") {
		at := arguments[1].(pgtype.Timestamptz)
		return reportTestRow{values: []any{tx.reportID, "in_review", pgtype.Int8{Int64: 11, Valid: true}, at, int64(91)}}
	}
	if strings.Contains(query, "LockTopicForExtendedModeration") {
		return reportTestRow{values: []any{tx.targetID, int64(7), "open", pgtype.Timestamptz{}, pgtype.Timestamptz{Time: testCreatedAt(), Valid: true}, pgtype.Timestamptz{Time: testCreatedAt(), Valid: true}}}
	}
	if strings.Contains(query, "ChangeTopicPinAndAudit") {
		at := arguments[0].(pgtype.Timestamptz)
		return reportTestRow{values: []any{tx.targetID, at, at, int64(92)}}
	}
	panic("unexpected report service query")
}

func (tx *reportServiceTestTx) Commit(context.Context) error   { tx.committed = true; return nil }
func (tx *reportServiceTestTx) Rollback(context.Context) error { tx.rolledBack = true; return nil }

type reportTestRow struct{ values []any }

func (row reportTestRow) Scan(destinations ...any) error {
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *int64:
			*destination = value.(int64)
		case *int32:
			*destination = value.(int32)
		case *string:
			*destination = value.(string)
		case *pgtype.Int8:
			*destination = value.(pgtype.Int8)
		case *pgtype.Text:
			*destination = value.(pgtype.Text)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		default:
			panic("unexpected report test scan destination")
		}
	}
	return nil
}
