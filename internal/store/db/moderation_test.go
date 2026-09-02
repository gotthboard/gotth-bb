package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestTopicModerationQueriesBindScanAndPreserveAuditTransaction(t *testing.T) {
	t.Parallel()

	atTime := pgtype.Timestamptz{Valid: true}
	actorID := pgtype.Int8{Int64: 11, Valid: true}
	reason := pgtype.Text{String: "reason", Valid: true}
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

func TestTopicModerationQueriesPreserveScanFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("scan failed")
	for _, invoke := range []func(*Queries) (any, error){
		func(q *Queries) (any, error) { return q.LockTopicForModeration(context.Background(), 41) },
		func(q *Queries) (any, error) {
			return q.ChangeTopicStateAndAudit(context.Background(), ChangeTopicStateAndAuditParams{})
		},
	} {
		database := &publishingDBTX{row: publishingRow{err: cause}}
		if _, err := invoke(New(database)); !errors.Is(err, cause) {
			t.Fatalf("moderation scan error = %v, want cause", err)
		}
	}
}
