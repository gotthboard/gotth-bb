package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestConfigureMarkTopicReadTransactionUsesExactLocalTimeouts(t *testing.T) {
	t.Parallel()

	database := &unreadDBTX{}
	if err := New(database).ConfigureMarkTopicReadTransaction(context.Background()); err != nil {
		t.Fatalf("ConfigureMarkTopicReadTransaction() returned error: %v", err)
	}
	for _, required := range []string{
		"set_config('statement_timeout', '2000ms', true)",
		"set_config('lock_timeout', '250ms', true)",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("configuration SQL lacks %q", required)
		}
	}
	cause := errors.New("configuration failed")
	database.execErr = cause
	if err := New(database).ConfigureMarkTopicReadTransaction(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("ConfigureMarkTopicReadTransaction() error = %v, want cause", err)
	}
}

func TestMarkTopicReadBoundaryBindsOnlyServerAuthorityAndScansSentinel(t *testing.T) {
	t.Parallel()

	database := &unreadDBTX{row: unreadRow{values: []any{int64(41), int32(7), int32(5), true}}}
	got, err := New(database).MarkTopicReadBoundary(context.Background(), MarkTopicReadBoundaryParams{
		TopicID: 41, IsStaff: true, GroupIds: []int64{3, 5}, ActorUserID: 11,
	})
	if err != nil || got != (MarkTopicReadBoundaryRow{TopicID: 41, NextPostNumber: 7, SelectedPostNumber: 5, Advanced: true}) ||
		!reflect.DeepEqual(database.args, []any{int64(41), true, []int64{3, 5}, int64(11)}) {
		t.Fatalf("MarkTopicReadBoundary() = (%+v, %v, args %#v)", got, err, database.args)
	}
	for _, required := range []string{
		"authorized_topic AS MATERIALIZED", "topic.deleted_at IS NULL", "topic.state <> 'hidden'",
		"area.visibility IN ('public', 'authenticated')", "membership.group_id = ANY($3::bigint[])",
		"post.deleted_at IS NULL", "post.redacted_at IS NULL", "post.author_id <> $4::bigint",
		"COALESCE(max(post.post_number), 0)", "statement_timestamp()", "ON CONFLICT (user_id, topic_id) DO UPDATE",
		"last_read_post_number = GREATEST", "read_at = EXCLUDED.read_at",
		"WHERE EXCLUDED.last_read_post_number > public.topic_reads.last_read_post_number",
	} {
		if !strings.Contains(database.query, required) {
			t.Fatalf("mark-read SQL lacks %q", required)
		}
	}
}

func TestGetTopicReadMarkerBindsIdentityAndScansPrivateTuple(t *testing.T) {
	t.Parallel()

	readAt := pgtype.Timestamptz{Valid: true}
	database := &unreadDBTX{row: unreadRow{values: []any{int32(5), readAt}}}
	got, err := New(database).GetTopicReadMarker(context.Background(), GetTopicReadMarkerParams{ActorUserID: 11, TopicID: 41})
	if err != nil || got != (GetTopicReadMarkerRow{LastReadPostNumber: 5, ReadAt: readAt}) ||
		!reflect.DeepEqual(database.args, []any{int64(11), int64(41)}) {
		t.Fatalf("GetTopicReadMarker() = (%+v, %v, args %#v)", got, err, database.args)
	}
	if !strings.Contains(database.query, "marker.user_id = $1") || !strings.Contains(database.query, "marker.topic_id = $2") {
		t.Fatalf("marker SQL = %q", database.query)
	}
}

func TestUnreadQueriesPreserveScanFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("unread scan failed")
	database := &unreadDBTX{row: unreadRow{err: cause}}
	if got, err := New(database).MarkTopicReadBoundary(context.Background(), MarkTopicReadBoundaryParams{}); !errors.Is(err, cause) || got != (MarkTopicReadBoundaryRow{}) {
		t.Fatalf("MarkTopicReadBoundary() = (%+v, %v), want zero/cause", got, err)
	}
	if got, err := New(database).GetTopicReadMarker(context.Background(), GetTopicReadMarkerParams{}); !errors.Is(err, cause) || got != (GetTopicReadMarkerRow{}) {
		t.Fatalf("GetTopicReadMarker() = (%+v, %v), want zero/cause", got, err)
	}
}

func TestMarkTopicReadIsTheSoleGeneratedMarkerMutation(t *testing.T) {
	t.Parallel()

	queryDirectory := filepath.Join("..", "queries")
	entries, err := os.ReadDir(queryDirectory)
	if err != nil {
		t.Fatalf("read query directory: %v", err)
	}
	mutation := regexp.MustCompile(`(?i)(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+public\.topic_reads`)
	mutations := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(queryDirectory, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for range mutation.FindAll(contents, -1) {
			mutations = append(mutations, entry.Name())
		}
	}
	if !reflect.DeepEqual(mutations, []string{"unread.sql"}) {
		t.Fatalf("generated-query marker mutations = %v, want only unread.sql", mutations)
	}
}

type unreadDBTX struct {
	DBTX
	query   string
	args    []any
	row     pgx.Row
	execErr error
}

func (database *unreadDBTX) Exec(_ context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
	database.query = query
	database.args = append([]any(nil), arguments...)
	return pgconn.CommandTag{}, database.execErr
}

func (database *unreadDBTX) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	database.query = query
	database.args = append([]any(nil), arguments...)
	return database.row
}

type unreadRow struct {
	values []any
	err    error
}

func (row unreadRow) Scan(destinations ...any) error {
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
			panic("unexpected unread row destination")
		}
	}
	return nil
}
