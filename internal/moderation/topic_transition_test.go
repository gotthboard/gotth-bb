package moderation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var errModerationTest = errors.New("moderation test failure")

func TestChangeTopicLockCommitsTypedTransitionAndAudit(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 2, 8, 0, 0, 123456789, time.UTC)
	requestID := pgtype.UUID{Bytes: [16]byte{0x51}, Valid: true}
	for _, test := range []struct {
		name, current, resulting, action string
		lock                             bool
	}{
		{name: "lock", current: "open", resulting: "locked", action: "lock_topic", lock: true},
		{name: "unlock", current: "locked", resulting: "open", action: "unlock_topic"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &topicLockTestTx{topicID: 41, current: test.current, resulting: test.resulting, auditID: 71}
			result, err := ChangeTopicLock(context.Background(), topicLockTestBeginner{tx: tx}, func() time.Time { return at },
				policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}, 41, test.lock, "clear reason", requestID)
			if err != nil || result != (TopicTransitionResult{TopicID: 41, State: policy.TopicState(test.resulting), AuditID: 71}) {
				t.Fatalf("ChangeTopicLock() = (%+v, %v)", result, err)
			}
			if !tx.committed || tx.rolledBack || tx.changeCalls != 1 || tx.actorID != 11 || tx.action != test.action ||
				tx.reason != "clear reason" || tx.previous != test.current || tx.resultingArg != test.resulting || tx.requestID != requestID ||
				!tx.atTime.Equal(at.UTC().Truncate(time.Microsecond)) {
				t.Fatalf("topic moderation transaction = %+v", tx)
			}
		})
	}
}

func TestChangeTopicVisibilityCommitsTypedTransitionAndAudit(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 2, 9, 0, 0, 123456789, time.UTC)
	requestID := pgtype.UUID{Bytes: [16]byte{0x61}, Valid: true}
	for _, test := range []struct {
		name, current, resulting, action string
		hide                             bool
	}{
		{name: "hide", current: "open", resulting: "hidden", action: "hide_topic", hide: true},
		{name: "restore", current: "hidden", resulting: "open", action: "restore_topic"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &topicLockTestTx{topicID: 41, current: test.current, resulting: test.resulting, auditID: 81}
			result, err := ChangeTopicVisibility(context.Background(), topicLockTestBeginner{tx: tx}, func() time.Time { return at },
				policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator}, 41, test.hide, "visibility reason", requestID)
			if err != nil || result != (TopicTransitionResult{TopicID: 41, State: policy.TopicState(test.resulting), AuditID: 81}) {
				t.Fatalf("ChangeTopicVisibility() = (%+v, %v)", result, err)
			}
			if !tx.committed || tx.rolledBack || tx.changeCalls != 1 || tx.actorID != 11 || tx.action != test.action ||
				tx.reason != "visibility reason" || tx.previous != test.current || tx.resultingArg != test.resulting || tx.requestID != requestID ||
				!tx.atTime.Equal(at.UTC().Truncate(time.Microsecond)) {
				t.Fatalf("topic visibility transaction = %+v", tx)
			}
		})
	}
}

func TestChangeTopicVisibilityRequiresExactCurrentState(t *testing.T) {
	t.Parallel()

	requestID := pgtype.UUID{Bytes: [16]byte{0x61}, Valid: true}
	for _, test := range []struct {
		current string
		hide    bool
	}{
		{current: "locked", hide: true},
		{current: "open"},
	} {
		tx := &topicLockTestTx{topicID: 41, current: test.current}
		result, err := ChangeTopicVisibility(context.Background(), topicLockTestBeginner{tx: tx}, time.Now,
			policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}, 41, test.hide, "reason", requestID)
		if result != (TopicTransitionResult{}) || !errors.Is(err, ErrTopicModerationConflict) || tx.changeCalls != 0 || tx.committed || !tx.rolledBack {
			t.Fatalf("ChangeTopicVisibility(%q, %t) = (%+v, %v, tx %+v)", test.current, test.hide, result, err, tx)
		}
	}
}

func TestChangeTopicLockDeniesAuthorityAndWrongState(t *testing.T) {
	t.Parallel()

	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	for _, actor := range []policy.AccessContext{
		{Authenticated: true, UserID: 11, Role: policy.RoleMember},
		{Authenticated: true, UserID: 11, Role: policy.RoleModerator, Suspended: true},
		{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator, MutedUntil: func() *time.Time { value := time.Now(); return &value }()},
	} {
		result, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 41, true, "reason", requestID)
		if result != (TopicTransitionResult{}) || !errors.Is(err, ErrTopicModerationDenied) {
			t.Fatalf("denied actor %+v = (%+v, %v)", actor, result, err)
		}
	}
	tx := &topicLockTestTx{topicID: 41, current: "locked", resulting: "locked", auditID: 71}
	result, err := ChangeTopicLock(context.Background(), topicLockTestBeginner{tx: tx}, time.Now,
		policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}, 41, true, "reason", requestID)
	if result != (TopicTransitionResult{}) || !errors.Is(err, ErrTopicModerationConflict) || tx.changeCalls != 0 || tx.committed || !tx.rolledBack {
		t.Fatalf("wrong state = (%+v, %v, tx %+v)", result, err, tx)
	}
}

func TestChangeTopicLockRejectsInvalidBoundaryBeforeTransaction(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleAdministrator}
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, run := range []func() error{
		func() error {
			_, err := ChangeTopicLock(nil, panicTopicLockBeginner{}, time.Now, actor, 1, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), nil, time.Now, actor, 1, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, nil, actor, 1, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, policy.AccessContext{}, 1, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 0, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, " ", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, " padded ", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, string([]byte{0xff}), requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, "line one\nline two", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, "line one\u2028line two", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, strings.Repeat("x", 2001), requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, "reason", pgtype.UUID{})
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, "reason", pgtype.UUID{Valid: true})
			return err
		},
		func() error {
			_, err := ChangeTopicLock(canceled, panicTopicLockBeginner{}, time.Now, actor, 1, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, func() time.Time { return time.Time{} }, actor, 1, true, "reason", requestID)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatal("ChangeTopicLock accepted invalid boundary")
		}
	}
}

func TestChangeTopicLockTypesUserInputFailures(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	for _, run := range []func() error{
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 0, true, "reason", requestID)
			return err
		},
		func() error {
			_, err := ChangeTopicLock(context.Background(), panicTopicLockBeginner{}, time.Now, actor, 1, true, " padded ", requestID)
			return err
		},
	} {
		if err := run(); !errors.Is(err, ErrTopicModerationInput) {
			t.Fatalf("ChangeTopicLock input error = %v, want typed input failure", err)
		}
	}
}

func TestChangeTopicLockFailsClosedAtTransactionStages(t *testing.T) {
	t.Parallel()

	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator}
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	for _, failure := range []string{"begin", "lock", "invalid-lock", "change", "invalid-change", "commit"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			tx := &topicLockTestTx{topicID: 41, current: "open", resulting: "locked", auditID: 71, failure: failure}
			beginner := topicLockTestBeginner{tx: tx}
			if failure == "begin" {
				beginner.err = errModerationTest
			}
			result, err := ChangeTopicLock(context.Background(), beginner, time.Now, actor, 41, true, "reason", requestID)
			if err == nil || result != (TopicTransitionResult{}) || tx.committed || failure != "begin" && !tx.rolledBack {
				t.Fatalf("ChangeTopicLock(%q) = (%+v, %v), tx %+v", failure, result, err, tx)
			}
		})
	}
}

type topicLockTestBeginner struct {
	tx  *topicLockTestTx
	err error
}

func (beginner topicLockTestBeginner) Begin(context.Context) (pgx.Tx, error) {
	if beginner.err != nil {
		return nil, beginner.err
	}
	return beginner.tx, nil
}

type topicLockTestTx struct {
	pgx.Tx
	topicID, auditID, actorID int64
	current, resulting        string
	previous, resultingArg    string
	action, reason            string
	atTime                    time.Time
	requestID                 pgtype.UUID
	changeCalls               int
	failure                   string
	committed, rolledBack     bool
}

func (tx *topicLockTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	switch {
	case strings.Contains(query, "LockTopicForModeration"):
		if tx.failure == "lock" {
			return moderationTestRow{err: errModerationTest}
		}
		if tx.failure == "invalid-lock" {
			return moderationTestRow{values: []any{int64(0), "invented"}}
		}
		return moderationTestRow{values: []any{tx.topicID, tx.current}}
	case strings.Contains(query, "ChangeTopicStateAndAudit"):
		if tx.failure == "change" {
			return moderationTestRow{err: errModerationTest}
		}
		tx.changeCalls++
		tx.resultingArg = arguments[0].(string)
		tx.atTime = arguments[1].(pgtype.Timestamptz).Time
		tx.previous = arguments[3].(string)
		tx.actorID = arguments[4].(pgtype.Int8).Int64
		tx.action = arguments[5].(string)
		tx.reason = arguments[6].(pgtype.Text).String
		tx.requestID = arguments[7].(pgtype.UUID)
		if tx.failure == "invalid-change" {
			return moderationTestRow{values: []any{int64(0), "invented", pgtype.Timestamptz{}, int64(0)}}
		}
		return moderationTestRow{values: []any{tx.topicID, tx.resulting, pgtype.Timestamptz{Time: tx.atTime, Valid: true}, tx.auditID}}
	default:
		panic("unexpected moderation query")
	}
}

func (tx *topicLockTestTx) Commit(context.Context) error {
	if tx.failure == "commit" {
		return errModerationTest
	}
	tx.committed = true
	return nil
}
func (tx *topicLockTestTx) Rollback(context.Context) error { tx.rolledBack = true; return nil }

type moderationTestRow struct {
	values []any
	err    error
}

func (row moderationTestRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *int64:
			*destination = value.(int64)
		case *string:
			*destination = value.(string)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		default:
			panic("unexpected moderation scan destination")
		}
	}
	return nil
}

type panicTopicLockBeginner struct{}

func (panicTopicLockBeginner) Begin(context.Context) (pgx.Tx, error) {
	panic("moderation transaction began")
}
