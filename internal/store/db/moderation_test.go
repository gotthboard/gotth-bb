package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestModerationQueriesBindScanAndPreserveAuditTransaction(t *testing.T) {
	t.Parallel()

	atTime := pgtype.Timestamptz{Valid: true}
	observedAt := pgtype.Timestamptz{Time: time.Unix(1, 0), Valid: true}
	suspendedAt := pgtype.Timestamptz{Time: time.Unix(2, 0), Valid: true}
	updatedAt := pgtype.Timestamptz{Time: time.Unix(3, 0), Valid: true}
	actorID := pgtype.Int8{Int64: 11, Valid: true}
	reason := pgtype.Text{String: "reason", Valid: true}
	previousReason := pgtype.Text{String: "previous reason", Valid: true}
	previousAt := pgtype.Timestamptz{Valid: true}
	previousUntil := pgtype.Timestamptz{Valid: true}
	requestID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	for _, test := range []struct {
		name       string
		rowValues  []any
		wantArgs   []any
		required   []string
		invoke     func(*Queries) (any, error)
		wantResult any
	}{
		{
			name: "lock", rowValues: []any{int64(41), "open"}, wantArgs: []any{int64(41)},
			required:   []string{"topic.deleted_at IS NULL", "FOR UPDATE OF topic"},
			invoke:     func(q *Queries) (any, error) { return q.LockTopicForModeration(context.Background(), 41) },
			wantResult: LockTopicForModerationRow{ID: 41, State: "open"},
		},
		{
			name: "change and audit", rowValues: []any{int64(41), "locked", atTime, int64(71)},
			wantArgs: []any{"locked", atTime, int64(41), "open", actorID, "lock_topic", reason, requestID},
			required: []string{
				"WITH changed AS", "updated_at = GREATEST($2::timestamptz, topic.updated_at)", "topic.state = $4",
				"INSERT INTO public.moderation_actions", "'forum_user'", "'topic'", "jsonb_build_object('state', $4::text)",
				"jsonb_build_object('state', changed.state)", "JOIN audit ON audit.target_topic_id = changed.id",
			},
			invoke: func(q *Queries) (any, error) {
				return q.ChangeTopicStateAndAudit(context.Background(), ChangeTopicStateAndAuditParams{
					ResultingState: "locked", AtTime: atTime, TopicID: 41, PreviousState: "open",
					ActorUserID: actorID, ActionType: "lock_topic", Reason: reason, RequestID: requestID,
				})
			},
			wantResult: ChangeTopicStateAndAuditRow{TopicID: 41, State: "locked", UpdatedAt: atTime, AuditID: 71},
		},
		{
			name: "moderation user status",
			rowValues: []any{
				int64(42), "Local member", "member", previousAt, previousUntil, previousReason,
				pgtype.Timestamptz{}, atTime, updatedAt, updatedAt,
			},
			wantArgs: []any{int64(42), int64(11), true, false},
			required: []string{
				"FROM public.users AS target", "target.id = $1", "target.id <> $2",
				"$3::boolean", "$4::boolean AND target.role = 'member'",
			},
			invoke: func(q *Queries) (any, error) {
				return q.GetModerationUserStatus(context.Background(), GetModerationUserStatusParams{
					TargetUserID: 42, ActorUserID: 11, IsAdministrator: true,
				})
			},
			wantResult: GetModerationUserStatusRow{
				ID: 42, DisplayName: "Local member", Role: "member", SuspendedAt: previousAt,
				SuspendedUntil: previousUntil, SuspensionReason: previousReason,
				CreatedAt: atTime, UpdatedAt: updatedAt, LastLoginAt: updatedAt,
			},
		},
		{
			name: "lock user", rowValues: []any{int64(42), "member", previousAt, previousUntil, previousReason, pgtype.Timestamptz{}, atTime, atTime},
			wantArgs:   []any{int64(42)},
			required:   []string{"FROM public.users AS forum_user", "FOR UPDATE OF forum_user"},
			invoke:     func(q *Queries) (any, error) { return q.LockUserForSuspension(context.Background(), 42) },
			wantResult: LockUserForSuspensionRow{ID: 42, Role: "member", SuspendedAt: previousAt, SuspendedUntil: previousUntil, SuspensionReason: previousReason, CreatedAt: atTime, UpdatedAt: atTime},
		},
		{
			name:      "suspend user and audit",
			rowValues: []any{int64(42), suspendedAt, pgtype.Timestamptz{}, reason, updatedAt, int64(72)},
			wantArgs:  []any{suspendedAt, reason, updatedAt, int64(42), observedAt, actorID, previousAt, previousUntil, previousReason, requestID},
			required:  []string{"suspended_at = GREATEST($1::timestamptz", "updated_at = GREATEST($3::timestamptz", "forum_user.suspended_at > $5::timestamptz", "'suspend_user'", "'suspension_reason', $9::text", "JOIN audit ON audit.target_user_id = changed.id"},
			invoke: func(q *Queries) (any, error) {
				return q.SuspendUserAndAudit(context.Background(), SuspendUserAndAuditParams{
					ObservedAt: observedAt, SuspendedAt: suspendedAt, UpdatedAt: updatedAt,
					Reason: reason, UserID: 42, ActorUserID: actorID,
					PreviousSuspendedAt: previousAt, PreviousSuspendedUntil: previousUntil,
					PreviousSuspensionReason: previousReason, RequestID: requestID,
				})
			},
			wantResult: SuspendUserAndAuditRow{UserID: 42, SuspendedAt: suspendedAt, SuspensionReason: reason, UpdatedAt: updatedAt, AuditID: 72},
		},
		{
			name:      "reinstate user and audit",
			rowValues: []any{int64(42), pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Text{}, updatedAt, int64(73)},
			wantArgs:  []any{updatedAt, int64(42), observedAt, actorID, reason, previousAt, previousUntil, previousReason.String, requestID},
			required:  []string{"SET suspended_at = NULL", "updated_at = GREATEST($1::timestamptz", "forum_user.suspended_at <= $3::timestamptz", "'reinstate_user'", "'suspension_reason', $8::text", "JOIN audit ON audit.target_user_id = changed.id"},
			invoke: func(q *Queries) (any, error) {
				return q.ReinstateUserAndAudit(context.Background(), ReinstateUserAndAuditParams{
					ObservedAt: observedAt, UpdatedAt: updatedAt, UserID: 42, ActorUserID: actorID, Reason: reason,
					PreviousSuspendedAt: previousAt, PreviousSuspendedUntil: previousUntil,
					PreviousSuspensionReason: previousReason.String, RequestID: requestID,
				})
			},
			wantResult: ReinstateUserAndAuditRow{UserID: 42, UpdatedAt: updatedAt, AuditID: 73},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &publishingDBTX{row: publishingRow{values: test.rowValues}}
			got, err := test.invoke(New(database))
			if err != nil || !reflect.DeepEqual(got, test.wantResult) || !reflect.DeepEqual(database.args, test.wantArgs) {
				t.Fatalf("moderation query = (result %+v, error %v, args %#v)", got, err, database.args)
			}
			for _, required := range test.required {
				if !strings.Contains(database.query, required) {
					t.Fatalf("moderation query lacks %q", required)
				}
			}
		})
	}
}

func TestModerationQueriesPreserveScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	for _, invoke := range []func(*Queries) (any, error){
		func(q *Queries) (any, error) { return q.LockTopicForModeration(context.Background(), 41) },
		func(q *Queries) (any, error) {
			return q.ChangeTopicStateAndAudit(context.Background(), ChangeTopicStateAndAuditParams{})
		},
		func(q *Queries) (any, error) {
			return q.GetModerationUserStatus(context.Background(), GetModerationUserStatusParams{})
		},
		func(q *Queries) (any, error) { return q.LockUserForSuspension(context.Background(), 41) },
		func(q *Queries) (any, error) {
			return q.SuspendUserAndAudit(context.Background(), SuspendUserAndAuditParams{})
		},
		func(q *Queries) (any, error) {
			return q.ReinstateUserAndAudit(context.Background(), ReinstateUserAndAuditParams{})
		},
	} {
		database := &publishingDBTX{row: publishingRow{err: cause}}
		if _, err := invoke(New(database)); !errors.Is(err, cause) {
			t.Fatalf("moderation scan error = %v, want cause", err)
		}
	}
}
