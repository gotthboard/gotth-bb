package forum

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestMarkTopicReadCommitsServerSelectedBoundary(t *testing.T) {
	t.Parallel()

	readAt := pgtype.Timestamptz{Time: time.Date(2026, time.September, 7, 20, 0, 0, 0, time.UTC), Valid: true}
	tx := &markReadTestTx{
		boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: 5, advanced: true,
		markerPostNumber: 5, markerReadAt: readAt,
	}
	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleModerator, GroupIDs: []int64{3, 5}}
	if err := MarkTopicRead(context.Background(), markReadTestBeginner{tx: tx}, actor, 41); err != nil {
		t.Fatalf("MarkTopicRead() returned error: %v", err)
	}
	if !tx.committed || tx.rolledBack || tx.configureCalls != 1 || tx.boundaryCalls != 1 || tx.markerCalls != 1 {
		t.Fatalf("transaction = (commit %t rollback %t configure %d boundary %d marker %d)", tx.committed, tx.rolledBack, tx.configureCalls, tx.boundaryCalls, tx.markerCalls)
	}
	if !reflect.DeepEqual(tx.boundaryArgs, []any{int64(41), true, []int64{3, 5}, int64(11)}) ||
		!reflect.DeepEqual(tx.markerArgs, []any{int64(11), int64(41)}) {
		t.Fatalf("query arguments = (boundary %#v marker %#v)", tx.boundaryArgs, tx.markerArgs)
	}
}

func TestFirstUnreadMapsBoundedAndDirectTargets(t *testing.T) {
	t.Parallel()

	finite := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for _, test := range []struct {
		name string
		row  firstUnreadTestRow
		want FirstUnreadTarget
	}{
		{name: "no target", row: validFirstUnreadRow(finite)},
		{name: "first page", row: firstUnreadTargetRow(finite, 91, 5, 1), want: FirstUnreadTarget{PostID: 91, Page: 1}},
		{name: "later page", row: firstUnreadTargetRow(finite, 92, 5, 26), want: FirstUnreadTarget{PostID: 92, Page: 2}},
		{name: "beyond bounded tree", row: firstUnreadTargetRow(finite, 93, 5, 0), want: FirstUnreadTarget{PostID: 93, Direct: true}},
		{name: "sentinel direct", row: firstUnreadTargetRow(finite, 94, 5, 250001), want: FirstUnreadTarget{PostID: 94, Direct: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &firstUnreadTestTx{row: test.row}
			got, err := FirstUnread(context.Background(), firstUnreadTestBeginner{tx: tx}, validMarkReadActor(), 41)
			if err != nil || got != test.want || !tx.committed || tx.rolledBack || tx.configureCalls != 1 || tx.queryCalls != 1 {
				t.Fatalf("FirstUnread() = (%+v, %v), transaction (commit %t rollback %t configure %d query %d)", got, err, tx.committed, tx.rolledBack, tx.configureCalls, tx.queryCalls)
			}
			if !reflect.DeepEqual(tx.args, []any{int64(41), false, []int64(nil), int64(11)}) {
				t.Fatalf("FirstUnread() args = %#v", tx.args)
			}
		})
	}
}

func TestFirstUnreadRejectsInputsAndMalformedRows(t *testing.T) {
	t.Parallel()

	finite := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		beginner interface {
			BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
		}
		actor   policy.AccessContext
		topicID int64
	}{
		{name: "nil context", beginner: panicFirstUnreadBeginner{}, actor: validMarkReadActor(), topicID: 41},
		{name: "visitor", ctx: context.Background(), beginner: panicFirstUnreadBeginner{}, actor: policy.AccessContext{}, topicID: 41},
		{name: "suspended", ctx: context.Background(), beginner: panicFirstUnreadBeginner{}, actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, Suspended: true}, topicID: 41},
		{name: "invalid topic", ctx: context.Background(), beginner: panicFirstUnreadBeginner{}, actor: validMarkReadActor()},
		{name: "canceled", ctx: canceled, beginner: panicFirstUnreadBeginner{}, actor: validMarkReadActor(), topicID: 41},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, err := FirstUnread(test.ctx, test.beginner, test.actor, test.topicID); err == nil || got != (FirstUnreadTarget{}) {
				t.Fatalf("FirstUnread() = (%+v, %v), want zero/error", got, err)
			}
		})
	}
	for _, test := range []struct {
		name string
		row  firstUnreadTestRow
	}{
		{name: "wrong topic", row: firstUnreadTestRow{values: []any{int64(42), int32(7), pgtype.Int4{}, pgtype.Timestamptz{}, int32(0), pgtype.Int8{}, pgtype.Int4{}, pgtype.Int8{}}}},
		{name: "partial target", row: firstUnreadTestRow{values: []any{int64(41), int32(7), pgtype.Int4{}, pgtype.Timestamptz{}, int32(5), pgtype.Int8{Int64: 91, Valid: true}, pgtype.Int4{}, pgtype.Int8{}}}},
		{name: "missing target despite unread", row: firstUnreadTestRow{values: []any{int64(41), int32(7), pgtype.Int4{}, pgtype.Timestamptz{}, int32(5), pgtype.Int8{}, pgtype.Int4{}, pgtype.Int8{}}}},
		{name: "target not above marker", row: firstUnreadTestRow{values: []any{int64(41), int32(7), pgtype.Int4{Int32: 5, Valid: true}, finite, int32(5), pgtype.Int8{Int64: 91, Valid: true}, pgtype.Int4{Int32: 5, Valid: true}, pgtype.Int8{Int64: 1, Valid: true}}}},
		{name: "invalid ordinal", row: firstUnreadTestRow{values: []any{int64(41), int32(7), pgtype.Int4{}, pgtype.Timestamptz{}, int32(5), pgtype.Int8{Int64: 91, Valid: true}, pgtype.Int4{Int32: 5, Valid: true}, pgtype.Int8{Int64: -1, Valid: true}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &firstUnreadTestTx{row: test.row}
			if got, err := FirstUnread(context.Background(), firstUnreadTestBeginner{tx: tx}, validMarkReadActor(), 41); err == nil || got != (FirstUnreadTarget{}) || tx.committed || !tx.rolledBack {
				t.Fatalf("FirstUnread() = (%+v, %v), transaction (commit %t rollback %t)", got, err, tx.committed, tx.rolledBack)
			}
		})
	}
}

func validFirstUnreadRow(finite pgtype.Timestamptz) firstUnreadTestRow {
	return firstUnreadTestRow{values: []any{int64(41), int32(7), pgtype.Int4{Int32: 5, Valid: true}, finite, int32(5), pgtype.Int8{}, pgtype.Int4{}, pgtype.Int8{}}}
}

func firstUnreadTargetRow(finite pgtype.Timestamptz, postID int64, postNumber int32, ordinal int64) firstUnreadTestRow {
	node := pgtype.Int8{}
	if ordinal != 0 {
		node = pgtype.Int8{Int64: ordinal, Valid: true}
	}
	return firstUnreadTestRow{values: []any{int64(41), int32(7), pgtype.Int4{Int32: 4, Valid: true}, finite, int32(5), pgtype.Int8{Int64: postID, Valid: true}, pgtype.Int4{Int32: postNumber, Valid: true}, node}}
}

type panicFirstUnreadBeginner struct{}

func (panicFirstUnreadBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	panic("first-unread transaction must not begin")
}

type firstUnreadTestBeginner struct{ tx *firstUnreadTestTx }

func (beginner firstUnreadTestBeginner) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if options != (pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}) {
		panic("first-unread transaction must be repeatable-read and read-only")
	}
	return beginner.tx, nil
}

type firstUnreadTestTx struct {
	pgx.Tx
	row                        firstUnreadTestRow
	args                       []any
	configureCalls, queryCalls int
	committed, rolledBack      bool
}

func (tx *firstUnreadTestTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "ConfigureUnreadReadTransaction") ||
		!strings.Contains(query, "set_config('statement_timeout', '5000ms', true)") ||
		!strings.Contains(query, "set_config('work_mem', '4MB', true)") {
		panic("unexpected first-unread configuration query")
	}
	tx.configureCalls++
	return pgconn.CommandTag{}, nil
}

func (tx *firstUnreadTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	if !strings.Contains(query, "GetFirstUnreadTarget") {
		panic("unexpected first-unread query")
	}
	tx.queryCalls++
	tx.args = append([]any(nil), arguments...)
	return tx.row
}

func (tx *firstUnreadTestTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *firstUnreadTestTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type firstUnreadTestRow struct {
	values []any
	err    error
}

func (row firstUnreadTestRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *int64:
			*destination = value.(int64)
		case *int32:
			*destination = value.(int32)
		case *pgtype.Int4:
			*destination = value.(pgtype.Int4)
		case *pgtype.Int8:
			*destination = value.(pgtype.Int8)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		default:
			panic("unexpected first-unread destination")
		}
	}
	return nil
}

func TestMarkTopicReadAcceptsIdempotentAndEmptyBoundaries(t *testing.T) {
	t.Parallel()

	finite := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for _, test := range []struct {
		name                 string
		selected, marker     int32
		markerErr            error
		advanced             bool
		wantMarkerInspection int
	}{
		{name: "equal retry", selected: 4, marker: 4},
		{name: "lower stale retry", selected: 3, marker: 5},
		{name: "no eligible post and no marker", markerErr: pgx.ErrNoRows, wantMarkerInspection: 1},
		{name: "no eligible post preserves dormant marker", marker: 3, wantMarkerInspection: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tx := &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: test.selected,
				advanced: test.advanced, markerPostNumber: test.marker, markerReadAt: finite, markerErr: test.markerErr}
			if err := MarkTopicRead(context.Background(), markReadTestBeginner{tx: tx}, validMarkReadActor(), 41); err != nil {
				t.Fatalf("MarkTopicRead() returned error: %v", err)
			}
			if !tx.committed || tx.rolledBack || tx.markerCalls != 1 {
				t.Fatalf("transaction = (commit %t rollback %t marker %d)", tx.committed, tx.rolledBack, tx.markerCalls)
			}
		})
	}
}

func TestMarkTopicReadRejectsInvalidInputsBeforeBegin(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	valid := validMarkReadActor()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		beginner interface {
			BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
		}
		actor   policy.AccessContext
		topicID int64
		cause   error
	}{
		{name: "nil context", beginner: panicMarkReadBeginner{}, actor: valid, topicID: 41},
		{name: "nil beginner", ctx: context.Background(), actor: valid, topicID: 41},
		{name: "visitor", ctx: context.Background(), beginner: panicMarkReadBeginner{}, actor: policy.AccessContext{}, topicID: 41},
		{name: "suspended", ctx: context.Background(), beginner: panicMarkReadBeginner{}, actor: policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember, Suspended: true}, topicID: 41},
		{name: "malformed actor", ctx: context.Background(), beginner: panicMarkReadBeginner{}, actor: policy.AccessContext{Authenticated: true, UserID: 0, Role: policy.RoleMember}, topicID: 41},
		{name: "invalid topic", ctx: context.Background(), beginner: panicMarkReadBeginner{}, actor: valid},
		{name: "canceled", ctx: canceled, beginner: panicMarkReadBeginner{}, actor: valid, topicID: 41, cause: context.Canceled},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := MarkTopicRead(test.ctx, test.beginner, test.actor, test.topicID)
			if err == nil || test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("MarkTopicRead() error = %v, want failure cause %v", err, test.cause)
			}
		})
	}
}

func TestMarkTopicReadRollsBackFailuresAndMalformedRows(t *testing.T) {
	t.Parallel()

	cause := errors.New("forced mark-read failure")
	finite := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for _, test := range []struct {
		name string
		tx   *markReadTestTx
	}{
		{name: "transaction configuration", tx: &markReadTestTx{configureErr: cause}},
		{name: "boundary query", tx: &markReadTestTx{boundaryErr: cause}},
		{name: "wrong topic", tx: &markReadTestTx{boundaryTopicID: 42, nextPostNumber: 7}},
		{name: "invalid next", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 1}},
		{name: "negative selection", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: -1}},
		{name: "selection at next", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: 7}},
		{name: "advanced empty selection", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, advanced: true}},
		{name: "missing selected marker", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: 4, markerErr: pgx.ErrNoRows}},
		{name: "marker query", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, markerErr: cause}},
		{name: "zero marker", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, markerReadAt: finite}},
		{name: "marker at next", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, markerPostNumber: 7, markerReadAt: finite}},
		{name: "missing read time", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, markerPostNumber: 4}},
		{name: "infinite read time", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, markerPostNumber: 4, markerReadAt: pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.Infinity}}},
		{name: "marker below selection", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: 5, markerPostNumber: 4, markerReadAt: finite}},
		{name: "advanced to wrong marker", tx: &markReadTestTx{boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: 4, advanced: true, markerPostNumber: 5, markerReadAt: finite}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := MarkTopicRead(context.Background(), markReadTestBeginner{tx: test.tx}, validMarkReadActor(), 41)
			if err == nil || test.tx.committed || !test.tx.rolledBack {
				t.Fatalf("MarkTopicRead() = %v, transaction (commit %t rollback %t)", err, test.tx.committed, test.tx.rolledBack)
			}
		})
	}
}

func TestMarkTopicReadPreservesBeginAndUnknownCommitFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("transaction failed")
	if err := MarkTopicRead(context.Background(), markReadTestBeginner{err: cause}, validMarkReadActor(), 41); !errors.Is(err, cause) {
		t.Fatalf("begin failure = %v, want cause", err)
	}
	tx := &markReadTestTx{
		boundaryTopicID: 41, nextPostNumber: 7, selectedPostNumber: 4, advanced: true,
		markerPostNumber: 4, markerReadAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}, commitErr: cause,
	}
	err := MarkTopicRead(context.Background(), markReadTestBeginner{tx: tx}, validMarkReadActor(), 41)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "outcome unknown; inspect state before retry") || !tx.committed || !tx.rolledBack {
		t.Fatalf("commit failure = (%v, commit %t rollback %t)", err, tx.committed, tx.rolledBack)
	}
}

func validMarkReadActor() policy.AccessContext {
	return policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
}

type panicMarkReadBeginner struct{}

func (panicMarkReadBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	panic("mark-read transaction must not begin")
}

type markReadTestBeginner struct {
	tx  *markReadTestTx
	err error
}

func (beginner markReadTestBeginner) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if options != (pgx.TxOptions{IsoLevel: pgx.ReadCommitted}) {
		panic("mark-read transaction must use read committed isolation")
	}
	if beginner.err != nil {
		return nil, beginner.err
	}
	return beginner.tx, nil
}

type markReadTestTx struct {
	pgx.Tx
	boundaryTopicID, markerUserID, markerTopicID    int64
	nextPostNumber, selectedPostNumber              int32
	markerPostNumber                                int32
	markerReadAt                                    pgtype.Timestamptz
	advanced                                        bool
	configureErr, boundaryErr, markerErr, commitErr error
	boundaryArgs, markerArgs                        []any
	configureCalls, boundaryCalls, markerCalls      int
	committed, rolledBack                           bool
}

func (tx *markReadTestTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "ConfigureMarkTopicReadTransaction") ||
		!strings.Contains(query, "set_config('statement_timeout', '2000ms', true)") ||
		!strings.Contains(query, "set_config('lock_timeout', '250ms', true)") {
		panic("unexpected mark-read configuration query")
	}
	tx.configureCalls++
	return pgconn.CommandTag{}, tx.configureErr
}

func (tx *markReadTestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	switch {
	case strings.Contains(query, "MarkTopicReadBoundary"):
		tx.boundaryCalls++
		tx.boundaryArgs = append([]any(nil), arguments...)
		if tx.boundaryErr != nil {
			return markReadTestRow{err: tx.boundaryErr}
		}
		return markReadTestRow{values: []any{tx.boundaryTopicID, tx.nextPostNumber, tx.selectedPostNumber, tx.advanced}}
	case strings.Contains(query, "GetTopicReadMarker"):
		tx.markerCalls++
		tx.markerArgs = append([]any(nil), arguments...)
		if tx.markerErr != nil {
			return markReadTestRow{err: tx.markerErr}
		}
		return markReadTestRow{values: []any{tx.markerPostNumber, tx.markerReadAt}}
	default:
		panic("unexpected mark-read query")
	}
}

func (tx *markReadTestTx) Commit(context.Context) error {
	tx.committed = true
	return tx.commitErr
}

func (tx *markReadTestTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type markReadTestRow struct {
	values []any
	err    error
}

func (row markReadTestRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch destination := destinations[index].(type) {
		case *int64:
			*destination = value.(int64)
		case *int32:
			*destination = value.(int32)
		case *bool:
			*destination = value.(bool)
		case *pgtype.Timestamptz:
			*destination = value.(pgtype.Timestamptz)
		default:
			panic("unexpected mark-read destination")
		}
	}
	return nil
}
