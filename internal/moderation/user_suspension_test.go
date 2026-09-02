package moderation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestChangeUserSuspensionCommitsExactTransitions(t *testing.T) {
	t.Parallel()

	requestID := pgtype.UUID{Bytes: [16]byte{0x71}, Valid: true}
	for _, test := range []struct {
		name                                           string
		suspend                                        bool
		target                                         db.LockUserForSuspensionRow
		clock                                          time.Time
		wantObservedAt, wantSuspendedAt, wantUpdatedAt time.Time
		wantResult                                     UserSuspensionResult
		wantSteps                                      []string
	}{
		{
			name: "suspend uses nondecreasing target time", suspend: true,
			target:          activeSuspensionTarget(41, "member", time.Date(2026, time.September, 2, 10, 0, 0, 0, time.UTC), time.Date(2026, time.September, 2, 11, 0, 0, 0, time.UTC)),
			clock:           time.Date(2026, time.September, 2, 9, 0, 0, 0, time.UTC),
			wantObservedAt:  time.Date(2026, time.September, 2, 9, 0, 0, 0, time.UTC),
			wantSuspendedAt: time.Date(2026, time.September, 2, 10, 0, 0, 0, time.UTC),
			wantUpdatedAt:   time.Date(2026, time.September, 2, 11, 0, 0, 0, time.UTC),
			wantResult:      UserSuspensionResult{UserID: 41, Suspended: true, AuditID: 81},
			wantSteps:       []string{"governance", "actor", "target", "suspend", "commit"},
		},
		{
			name: "reinstate", target: suspendedTarget(41, "member"),
			clock:          time.Date(2026, time.September, 2, 12, 0, 0, 123456789, time.UTC),
			wantObservedAt: time.Date(2026, time.September, 2, 12, 0, 0, 123456000, time.UTC),
			wantUpdatedAt:  time.Date(2026, time.September, 2, 12, 0, 0, 123456000, time.UTC),
			wantResult:     UserSuspensionResult{UserID: 41, AuditID: 82},
			wantSteps:      []string{"governance", "actor", "target", "reinstate", "commit"},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &userSuspensionTestTx{actor: activeSuspensionTarget(11, "moderator", testCreatedAt().Add(-2*time.Hour), testCreatedAt().Add(-time.Hour)), target: test.target, auditID: test.wantResult.AuditID}
			result, err := ChangeUserSuspension(context.Background(), userSuspensionTestBeginner{tx: tx}, func() time.Time { return test.clock },
				policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}, 41, test.suspend, "Clear reason", requestID)
			if err != nil || result != test.wantResult {
				t.Fatalf("ChangeUserSuspension() = (%+v, %v), want %+v", result, err, test.wantResult)
			}
			if !reflect.DeepEqual(tx.steps, test.wantSteps) || tx.observedAt != test.wantObservedAt || tx.suspendedAt != test.wantSuspendedAt || tx.updatedAt != test.wantUpdatedAt ||
				tx.actorID != 11 || tx.targetID != 41 || tx.reason != "Clear reason" || tx.requestID != requestID ||
				tx.previousAt != test.target.SuspendedAt || tx.previousUntil != test.target.SuspendedUntil || tx.previousReason != test.target.SuspensionReason {
				t.Fatalf("transaction = %+v", tx)
			}
		})
	}
}

func TestChangeUserSuspensionEnforcesHierarchyAndAdministratorContinuity(t *testing.T) {
	t.Parallel()

	requestID := pgtype.UUID{Bytes: [16]byte{0x72}, Valid: true}
	moderator := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}
	administrator := policy.AccessContext{Authenticated: true, UserID: 12, Role: policy.RoleAdministrator}
	for _, test := range []struct {
		name           string
		actor          policy.AccessContext
		target         db.LockUserForSuspensionRow
		administrators int64
		wantCause      error
		wantResult     UserSuspensionResult
		wantChanges    int
	}{
		{name: "moderator cannot suspend moderator", actor: moderator, target: activeSuspensionTarget(41, "moderator", testCreatedAt(), testCreatedAt()), wantCause: ErrUserModerationDenied},
		{name: "administrator preserves final administrator", actor: administrator, target: activeSuspensionTarget(41, "administrator", testCreatedAt(), testCreatedAt()), administrators: 1, wantCause: ErrAdministratorContinuity},
		{name: "administrator may suspend another when two remain", actor: administrator, target: activeSuspensionTarget(41, "administrator", testCreatedAt(), testCreatedAt()), administrators: 2, wantResult: UserSuspensionResult{UserID: 41, Suspended: true, AuditID: 83}, wantChanges: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &userSuspensionTestTx{actor: activeSuspensionTarget(test.actor.UserID, storedRole(test.actor.Role), testCreatedAt(), testCreatedAt()), target: test.target, administrators: test.administrators, auditID: 83}
			result, err := ChangeUserSuspension(context.Background(), userSuspensionTestBeginner{tx: tx}, testModerationNow,
				test.actor, 41, true, "Reason", requestID)
			if result != test.wantResult || !errors.Is(err, test.wantCause) || tx.changeCalls != test.wantChanges || tx.committed != (test.wantCause == nil) || tx.rolledBack == (test.wantCause == nil) {
				t.Fatalf("ChangeUserSuspension() = (%+v, %v), tx %+v", result, err, tx)
			}
		})
	}
}

func TestChangeUserSuspensionRevalidatesLockedActorAndOrdersUserLocks(t *testing.T) {
	t.Parallel()

	access := policy.AccessContext{Authenticated: true, UserID: 50, Role: policy.RoleAdministrator}
	target := activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())
	requestID := pgtype.UUID{Bytes: [16]byte{0x76}, Valid: true}
	activeActor := activeSuspensionTarget(50, "administrator", testCreatedAt(), testCreatedAt())
	suspendedActor := suspendedTarget(50, "administrator")
	mutedActor := activeActor
	mutedActor.MutedUntil = pgtype.Timestamptz{Time: testModerationNow().Add(time.Hour), Valid: true}
	futureUpdatedTarget := target
	futureUpdatedTarget.UpdatedAt = pgtype.Timestamptz{Time: testModerationNow().Add(2 * time.Hour), Valid: true}
	expiredActor := expiredSuspensionTarget(50, "administrator")
	expiredActor.MutedUntil = pgtype.Timestamptz{Time: testModerationNow().Add(-time.Hour), Valid: true}
	for _, test := range []struct {
		name       string
		actor      db.LockUserForSuspensionRow
		target     db.LockUserForSuspensionRow
		wantDenied bool
	}{
		{name: "role changed", actor: activeSuspensionTarget(50, "member", testCreatedAt(), testCreatedAt()), wantDenied: true},
		{name: "suspended", actor: suspendedActor, wantDenied: true},
		{name: "muted", actor: mutedActor, wantDenied: true},
		{name: "future target update cannot expire mute", actor: mutedActor, target: futureUpdatedTarget, wantDenied: true},
		{name: "expired restrictions", actor: expiredActor},
		{name: "active", actor: activeActor},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lockedTarget := test.target
			if lockedTarget.ID == 0 {
				lockedTarget = target
			}
			tx := &userSuspensionTestTx{actor: test.actor, target: lockedTarget, auditID: 84}
			result, err := ChangeUserSuspension(context.Background(), userSuspensionTestBeginner{tx: tx}, testModerationNow,
				access, 41, true, "Reason", requestID)
			if test.wantDenied {
				if result != (UserSuspensionResult{}) || !errors.Is(err, ErrUserModerationDenied) || tx.changeCalls != 0 || tx.committed || !tx.rolledBack {
					t.Fatalf("locked actor denial = (%+v, %v), tx %+v", result, err, tx)
				}
				return
			}
			if err != nil || result != (UserSuspensionResult{UserID: 41, Suspended: true, AuditID: 84}) ||
				!reflect.DeepEqual(tx.steps, []string{"governance", "target", "actor", "suspend", "commit"}) {
				t.Fatalf("locked actor success = (%+v, %v), tx %+v", result, err, tx)
			}
		})
	}
}

func TestChangeUserSuspensionRejectsInvalidBoundaryBeforeTransaction(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator}
	requestID := pgtype.UUID{Bytes: [16]byte{0x73}, Valid: true}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, run := range []func() error{
		func() error {
			_, err := ChangeUserSuspension(nil, panicUserSuspensionBeginner{}, time.Now, actor, 41, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), nil, time.Now, actor, 41, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, nil, actor, 41, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, policy.AccessContext{}, 41, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}, 41, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator, Suspended: true}, 41, true, "reason", requestID)
			return err
		},
		func() error {
			muted := time.Now()
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator, MutedUntil: &muted}, 41, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 0, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 11, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 41, true, " padded ", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 41, true, strings.Repeat("é", 501), requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 41, true, strings.Repeat("x", 2001), requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 41, true, "reason", pgtype.UUID{})
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 41, true, "reason", pgtype.UUID{Valid: true})
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(canceled, panicUserSuspensionBeginner{}, time.Now, actor, 41, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, func() time.Time { return time.Time{} }, actor, 41, true, "reason", requestID)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatal("ChangeUserSuspension accepted an invalid boundary")
		}
	}
}

func TestChangeUserSuspensionTypesInputAndStateConflicts(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator}
	requestID := pgtype.UUID{Bytes: [16]byte{0x74}, Valid: true}
	if _, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 0, true, "reason", requestID); !errors.Is(err, ErrUserModerationInput) {
		t.Fatalf("invalid target error = %v", err)
	}
	if _, err := ChangeUserSuspension(context.Background(), panicUserSuspensionBeginner{}, time.Now, actor, 41, true, " ", requestID); !errors.Is(err, ErrUserModerationInput) {
		t.Fatalf("invalid reason error = %v", err)
	}
	for _, test := range []struct {
		suspend bool
		target  db.LockUserForSuspensionRow
	}{
		{suspend: true, target: suspendedTarget(41, "member")},
		{target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{target: expiredSuspensionTarget(41, "member")},
	} {
		tx := &userSuspensionTestTx{actor: activeSuspensionTarget(11, "administrator", testCreatedAt(), testCreatedAt()), target: test.target}
		result, err := ChangeUserSuspension(context.Background(), userSuspensionTestBeginner{tx: tx}, testModerationNow, actor, 41, test.suspend, "reason", requestID)
		if result != (UserSuspensionResult{}) || !errors.Is(err, ErrUserModerationConflict) || tx.changeCalls != 0 || tx.committed || !tx.rolledBack {
			t.Fatalf("state conflict = (%+v, %v), tx %+v", result, err, tx)
		}
	}
}

func TestChangeUserSuspensionRollsBackTransactionFailures(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator}
	requestID := pgtype.UUID{Bytes: [16]byte{0x75}, Valid: true}
	for _, test := range []struct {
		name, failure string
		suspend       bool
		target        db.LockUserForSuspensionRow
	}{
		{name: "begin", failure: "begin", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "governance", failure: "governance", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "false governance", failure: "false-governance", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "first user", failure: "first-user", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "second user", failure: "second-user", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "invalid actor", failure: "invalid-actor", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "invalid target", failure: "invalid-target", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "invalid target role", failure: "invalid-role", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "count", failure: "count", suspend: true, target: activeSuspensionTarget(41, "administrator", testCreatedAt(), testCreatedAt())},
		{name: "suspend", failure: "change", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "invalid suspend", failure: "invalid-change", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
		{name: "reinstate", failure: "change", target: suspendedTarget(41, "member")},
		{name: "invalid reinstate", failure: "invalid-change", target: suspendedTarget(41, "member")},
		{name: "commit", failure: "commit", suspend: true, target: activeSuspensionTarget(41, "member", testCreatedAt(), testCreatedAt())},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &userSuspensionTestTx{actor: activeSuspensionTarget(11, "administrator", testCreatedAt(), testCreatedAt()), target: test.target, administrators: 2, auditID: 81, failure: test.failure}
			beginner := userSuspensionTestBeginner{tx: tx}
			if test.failure == "begin" {
				beginner.err = errModerationTest
			}
			result, err := ChangeUserSuspension(context.Background(), beginner, testModerationNow, actor, 41, test.suspend, "reason", requestID)
			if err == nil || result != (UserSuspensionResult{}) || tx.committed || test.failure != "begin" && !tx.rolledBack {
				t.Fatalf("failure %q = (%+v, %v), tx %+v", test.failure, result, err, tx)
			}
		})
	}
}

func testCreatedAt() time.Time {
	return time.Date(2026, time.September, 2, 8, 0, 0, 0, time.UTC)
}

func testModerationNow() time.Time {
	return time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
}

func storedRole(role policy.Role) string {
	switch role {
	case policy.RoleMember:
		return "member"
	case policy.RoleModerator:
		return "moderator"
	case policy.RoleAdministrator:
		return "administrator"
	default:
		return "invalid"
	}
}

func activeSuspensionTarget(id int64, role string, createdAt, updatedAt time.Time) db.LockUserForSuspensionRow {
	return db.LockUserForSuspensionRow{
		ID: id, Role: role,
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}
}

func suspendedTarget(id int64, role string) db.LockUserForSuspensionRow {
	target := activeSuspensionTarget(id, role, testCreatedAt(), testCreatedAt().Add(time.Hour))
	target.SuspendedAt = pgtype.Timestamptz{Time: testCreatedAt().Add(time.Hour), Valid: true}
	target.SuspensionReason = pgtype.Text{String: "Earlier reason", Valid: true}
	return target
}

func expiredSuspensionTarget(id int64, role string) db.LockUserForSuspensionRow {
	target := suspendedTarget(id, role)
	target.SuspendedUntil = pgtype.Timestamptz{Time: testCreatedAt().Add(2 * time.Hour), Valid: true}
	return target
}

type userSuspensionTestBeginner struct {
	tx  *userSuspensionTestTx
	err error
}

func (beginner userSuspensionTestBeginner) Begin(context.Context) (pgx.Tx, error) {
	if beginner.err != nil {
		return nil, beginner.err
	}
	return beginner.tx, nil
}

type userSuspensionTestTx struct {
	pgx.Tx
	actor, target                      db.LockUserForSuspensionRow
	administrators, auditID            int64
	steps                              []string
	failure                            string
	observedAt, suspendedAt, updatedAt time.Time
	targetID, actorID                  int64
	reason                             string
	requestID                          pgtype.UUID
	previousAt, previousUntil          pgtype.Timestamptz
	previousReason                     pgtype.Text
	changeCalls                        int
	userLockCalls                      int
	committed, rolledBack              bool
}

func (tx *userSuspensionTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	switch {
	case strings.Contains(query, "LockGovernanceState"):
		tx.steps = append(tx.steps, "governance")
		if tx.failure == "governance" {
			return userModerationTestRow{err: errModerationTest}
		}
		return userModerationTestRow{values: []any{tx.failure != "false-governance"}}
	case strings.Contains(query, "LockUserForSuspension"):
		tx.userLockCalls++
		if tx.failure == "first-user" && tx.userLockCalls == 1 || tx.failure == "second-user" && tx.userLockCalls == 2 {
			return userModerationTestRow{err: errModerationTest}
		}
		userID := arguments[0].(int64)
		user := tx.actor
		step := "actor"
		if userID == tx.target.ID {
			user = tx.target
			step = "target"
		}
		tx.steps = append(tx.steps, step)
		if tx.failure == "invalid-actor" && step == "actor" || tx.failure == "invalid-target" && step == "target" {
			user.ID = 0
		}
		if tx.failure == "invalid-role" && step == "target" {
			user.Role = "invented"
		}
		return userModerationTestRow{values: []any{user.ID, user.Role, user.SuspendedAt, user.SuspendedUntil, user.SuspensionReason, user.MutedUntil, user.CreatedAt, user.UpdatedAt}}
	case strings.Contains(query, "CountActiveAdministrators"):
		tx.steps = append(tx.steps, "count")
		if tx.failure == "count" {
			return userModerationTestRow{err: errModerationTest}
		}
		return userModerationTestRow{values: []any{tx.administrators}}
	case strings.Contains(query, "SuspendUserAndAudit"):
		tx.steps = append(tx.steps, "suspend")
		tx.captureSuspend(arguments)
		if tx.failure == "change" {
			return userModerationTestRow{err: errModerationTest}
		}
		if tx.failure == "invalid-change" {
			return userModerationTestRow{values: []any{int64(0), pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Text{}, pgtype.Timestamptz{}, int64(0)}}
		}
		suspendedAt := pgtype.Timestamptz{Time: tx.suspendedAt, Valid: true}
		updatedAt := pgtype.Timestamptz{Time: tx.updatedAt, Valid: true}
		return userModerationTestRow{values: []any{tx.targetID, suspendedAt, pgtype.Timestamptz{}, pgtype.Text{String: tx.reason, Valid: true}, updatedAt, tx.auditID}}
	case strings.Contains(query, "ReinstateUserAndAudit"):
		tx.steps = append(tx.steps, "reinstate")
		tx.captureReinstate(arguments)
		if tx.failure == "change" {
			return userModerationTestRow{err: errModerationTest}
		}
		if tx.failure == "invalid-change" {
			return userModerationTestRow{values: []any{int64(0), pgtype.Timestamptz{Valid: true}, pgtype.Timestamptz{}, pgtype.Text{}, pgtype.Timestamptz{}, int64(0)}}
		}
		updatedAt := pgtype.Timestamptz{Time: tx.updatedAt, Valid: true}
		return userModerationTestRow{values: []any{tx.targetID, pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Text{}, updatedAt, tx.auditID}}
	default:
		panic("unexpected user moderation query")
	}
}

func (tx *userSuspensionTestTx) captureSuspend(arguments []any) {
	tx.changeCalls++
	tx.suspendedAt = arguments[0].(pgtype.Timestamptz).Time
	tx.reason = arguments[1].(pgtype.Text).String
	tx.updatedAt = arguments[2].(pgtype.Timestamptz).Time
	tx.targetID = arguments[3].(int64)
	tx.observedAt = arguments[4].(pgtype.Timestamptz).Time
	tx.actorID = arguments[5].(pgtype.Int8).Int64
	tx.previousAt = arguments[6].(pgtype.Timestamptz)
	tx.previousUntil = arguments[7].(pgtype.Timestamptz)
	tx.previousReason = arguments[8].(pgtype.Text)
	tx.requestID = arguments[9].(pgtype.UUID)
}

func (tx *userSuspensionTestTx) captureReinstate(arguments []any) {
	tx.changeCalls++
	tx.updatedAt = arguments[0].(pgtype.Timestamptz).Time
	tx.targetID = arguments[1].(int64)
	tx.observedAt = arguments[2].(pgtype.Timestamptz).Time
	tx.actorID = arguments[3].(pgtype.Int8).Int64
	tx.reason = arguments[4].(pgtype.Text).String
	tx.previousAt = arguments[5].(pgtype.Timestamptz)
	tx.previousUntil = arguments[6].(pgtype.Timestamptz)
	tx.previousReason = pgtype.Text{String: arguments[7].(string), Valid: true}
	tx.requestID = arguments[8].(pgtype.UUID)
}

func (tx *userSuspensionTestTx) Commit(context.Context) error {
	tx.steps = append(tx.steps, "commit")
	if tx.failure == "commit" {
		return errModerationTest
	}
	tx.committed = true
	return nil
}

func (tx *userSuspensionTestTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type userModerationTestRow struct {
	values []any
	err    error
}

func (row userModerationTestRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *bool:
			*destination = value.(bool)
		case *int64:
			*destination = value.(int64)
		case *string:
			*destination = value.(string)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		case *pgtype.Text:
			*destination = value.(pgtype.Text)
		default:
			panic("unexpected user moderation scan destination")
		}
	}
	return nil
}

type panicUserSuspensionBeginner struct{}

func (panicUserSuspensionBeginner) Begin(context.Context) (pgx.Tx, error) {
	panic("user moderation transaction began")
}
