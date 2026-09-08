//go:build integration

package forum

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/render"
	storedb "github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const unreadWriteTestDatabase = "gotth_bb_an03_unread_write_test"

func TestMarkTopicReadTransactionsOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+unreadWriteTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale unread-write database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+unreadWriteTestDatabase); err != nil {
		t.Fatalf("create unread-write database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+unreadWriteTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = unreadWriteTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}

	connections := make([]*pgx.Conn, 8)
	for index := range connections {
		connectionConfig := testConfig.Copy()
		connectionConfig.RuntimeParams["application_name"] = fmt.Sprintf("an03-unread-%d", index)
		connections[index], err = pgx.ConnectConfig(ctx, connectionConfig)
		if err != nil {
			t.Fatalf("connect unread database %d: %v", index, err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}
	connection := connections[0]

	var ownerID, readerID, otherID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Owner', 'administrator') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Reader') RETURNING id`).Scan(&readerID); err != nil {
		t.Fatalf("insert reader: %v", err)
	}
	if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Other') RETURNING id`).Scan(&otherID); err != nil {
		t.Fatalf("insert other author: %v", err)
	}
	var groupID int64
	if err := connection.QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by) VALUES ('Readers', $1) RETURNING id`, ownerID).Scan(&groupID); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	areas := make(map[string]int64)
	for _, area := range []struct {
		slug, visibility, postingMode string
	}{
		{slug: "public", visibility: "public", postingMode: "normal"},
		{slug: "group", visibility: "groups", postingMode: "normal"},
		{slug: "read-only", visibility: "public", postingMode: "read_only"},
	} {
		var areaID int64
		if err := connection.QueryRow(ctx, `INSERT INTO public.areas
            (slug, name, visibility, posting_mode, created_by, updated_by)
            VALUES ($1, $1, $2, $3, $4, $4) RETURNING id`,
			area.slug, area.visibility, area.postingMode, ownerID).Scan(&areaID); err != nil {
			t.Fatalf("insert area %q: %v", area.slug, err)
		}
		areas[area.slug] = areaID
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by) VALUES ($1, $2, $3)`, areas["group"], groupID, ownerID); err != nil {
		t.Fatalf("map group area: %v", err)
	}

	reader := policy.AccessContext{Authenticated: true, UserID: readerID, Role: policy.RoleMember}
	readerWithGroup := policy.AccessContext{Authenticated: true, UserID: readerID, Role: policy.RoleMember, GroupIDs: []int64{groupID}}
	other := policy.AccessContext{Authenticated: true, UserID: otherID, Role: policy.RoleMember}
	owner := policy.AccessContext{Authenticated: true, UserID: ownerID, Role: policy.RoleAdministrator}
	baseTime := time.Date(2026, time.September, 7, 21, 0, 0, 0, time.UTC)

	publicTopic := insertUnreadTopic(t, ctx, connection, areas["public"], "Public unread", []int64{readerID, otherID, readerID, otherID, otherID, otherID}, baseTime)
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET
        deleted_at = created_at, deleted_by = $1, deletion_reason = 'deleted fixture'
        WHERE topic_id = $2 AND post_number = 4`, ownerID, publicTopic.id); err != nil {
		t.Fatalf("delete fixture post: %v", err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.posts SET
        markdown_source = '[Content removed by moderation]', rendered_html = '<p>Content removed by moderation.</p>',
        renderer_version = 'moderation-redaction-v1', redacted_at = created_at, redacted_by = $1,
        redaction_reason = 'redacted fixture', deleted_at = created_at, deleted_by = $1,
        deletion_reason = 'redacted fixture' WHERE topic_id = $2 AND post_number = 5`, ownerID, publicTopic.id); err != nil {
		t.Fatalf("redact fixture post: %v", err)
	}

	t.Run("server boundary retries and publication exclusion", func(t *testing.T) {
		if err := MarkTopicRead(ctx, connection, reader, publicTopic.id); err != nil {
			t.Fatalf("initial MarkTopicRead() returned error: %v", err)
		}
		marker, firstReadAt := inspectMarker(t, ctx, connection, readerID, publicTopic.id)
		if marker != 6 {
			t.Fatalf("initial marker = %d, want 6", marker)
		}
		if err := MarkTopicRead(ctx, connection, reader, publicTopic.id); err != nil {
			t.Fatalf("equal MarkTopicRead() returned error: %v", err)
		}
		if got, readAt := inspectMarker(t, ctx, connection, readerID, publicTopic.id); got != 6 || !readAt.Equal(firstReadAt) {
			t.Fatalf("equal retry marker = (%d, %s), want (6, %s)", got, readAt, firstReadAt)
		}
		if _, err := connection.Exec(ctx, `UPDATE public.posts SET deleted_at = created_at, deleted_by = $1, deletion_reason = 'head deleted' WHERE topic_id = $2 AND post_number = 6`, ownerID, publicTopic.id); err != nil {
			t.Fatalf("delete current head: %v", err)
		}
		if err := MarkTopicRead(ctx, connection, reader, publicTopic.id); err != nil {
			t.Fatalf("lower MarkTopicRead() returned error: %v", err)
		}
		if got, readAt := inspectMarker(t, ctx, connection, readerID, publicTopic.id); got != 6 || !readAt.Equal(firstReadAt) {
			t.Fatalf("lower retry marker = (%d, %s), want unchanged", got, readAt)
		}
		if _, err := connection.Exec(ctx, `UPDATE public.posts SET deleted_at = NULL, deleted_by = NULL, deletion_reason = NULL WHERE topic_id = $1 AND post_number = 6`, publicTopic.id); err != nil {
			t.Fatalf("restore current head: %v", err)
		}
		rootID := publicTopic.postIDs[0]
		otherReply, err := CreateReply(ctx, connection, func() time.Time { return baseTime.Add(time.Hour) }, other, publicTopic.id, rootID, "new other reply")
		if err != nil || otherReply.PostNumber != 7 {
			t.Fatalf("CreateReply(other) = (%+v, %v)", otherReply, err)
		}
		if got, readAt := inspectMarker(t, ctx, connection, readerID, publicTopic.id); got != 6 || !readAt.Equal(firstReadAt) {
			t.Fatalf("publication changed marker = (%d, %s)", got, readAt)
		}
		if err := MarkTopicRead(ctx, connection, reader, publicTopic.id); err != nil {
			t.Fatalf("higher MarkTopicRead() returned error: %v", err)
		}
		if got, readAt := inspectMarker(t, ctx, connection, readerID, publicTopic.id); got != 7 || !readAt.After(firstReadAt) {
			t.Fatalf("higher retry marker = (%d, %s), want 7 after %s", got, readAt, firstReadAt)
		}
		_, advancedAt := inspectMarker(t, ctx, connection, readerID, publicTopic.id)
		ownReply, err := CreateReply(ctx, connection, func() time.Time { return baseTime.Add(2 * time.Hour) }, reader, publicTopic.id, rootID, "own reply")
		if err != nil || ownReply.PostNumber != 8 {
			t.Fatalf("CreateReply(own) = (%+v, %v)", ownReply, err)
		}
		if err := MarkTopicRead(ctx, connection, reader, publicTopic.id); err != nil {
			t.Fatalf("own-only head MarkTopicRead() returned error: %v", err)
		}
		if got, readAt := inspectMarker(t, ctx, connection, readerID, publicTopic.id); got != 7 || !readAt.Equal(advancedAt) {
			t.Fatalf("own publication affected marker = (%d, %s), want unchanged", got, readAt)
		}
	})

	t.Run("empty boundary creates no marker", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Own only", []int64{readerID, readerID}, baseTime.Add(3*time.Hour))
		if err := MarkTopicRead(ctx, connection, reader, topic.id); err != nil {
			t.Fatalf("MarkTopicRead() returned error: %v", err)
		}
		assertNoMarker(t, ctx, connection, readerID, topic.id)
	})

	t.Run("authorization revocation preserves dormant marker", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["group"], "Group topic", []int64{otherID}, baseTime.Add(4*time.Hour))
		if err := MarkTopicRead(ctx, connection, readerWithGroup, topic.id); err != nil {
			t.Fatalf("authorized group MarkTopicRead() returned error: %v", err)
		}
		marker, readAt := inspectMarker(t, ctx, connection, readerID, topic.id)
		if _, err := connection.Exec(ctx, `DELETE FROM public.area_groups WHERE area_id = $1 AND group_id = $2`, areas["group"], groupID); err != nil {
			t.Fatalf("remove group mapping: %v", err)
		}
		if err := MarkTopicRead(ctx, connection, readerWithGroup, topic.id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("revoked MarkTopicRead() error = %v, want missing", err)
		}
		if got, gotAt := inspectMarker(t, ctx, connection, readerID, topic.id); got != marker || !gotAt.Equal(readAt) {
			t.Fatalf("revocation changed marker = (%d, %s)", got, gotAt)
		}
		if _, err := connection.Exec(ctx, `INSERT INTO public.area_groups (area_id, group_id, added_by) VALUES ($1, $2, $3)`, areas["group"], groupID, ownerID); err != nil {
			t.Fatalf("restore group mapping: %v", err)
		}
		if err := MarkTopicRead(ctx, connection, readerWithGroup, topic.id); err != nil {
			t.Fatalf("restored group MarkTopicRead() returned error: %v", err)
		}
	})

	t.Run("hidden deleted suspended and muted boundaries", func(t *testing.T) {
		hidden := insertUnreadTopic(t, ctx, connection, areas["public"], "Hidden topic", []int64{otherID}, baseTime.Add(5*time.Hour))
		if _, err := connection.Exec(ctx, `UPDATE public.topics SET state = 'hidden' WHERE id = $1`, hidden.id); err != nil {
			t.Fatalf("hide topic: %v", err)
		}
		if err := MarkTopicRead(ctx, connection, reader, hidden.id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("member hidden MarkTopicRead() error = %v, want missing", err)
		}
		if err := MarkTopicRead(ctx, connection, owner, hidden.id); err != nil {
			t.Fatalf("staff hidden MarkTopicRead() returned error: %v", err)
		}
		deleted := insertUnreadTopic(t, ctx, connection, areas["public"], "Deleted topic", []int64{otherID}, baseTime.Add(6*time.Hour))
		if _, err := connection.Exec(ctx, `UPDATE public.topics SET deleted_at = updated_at WHERE id = $1`, deleted.id); err != nil {
			t.Fatalf("soft delete topic: %v", err)
		}
		if err := MarkTopicRead(ctx, connection, reader, deleted.id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("deleted MarkTopicRead() error = %v, want missing", err)
		}
		assertNoMarker(t, ctx, connection, readerID, deleted.id)
		muted := reader
		mutedUntil := time.Now().Add(time.Hour)
		muted.MutedUntil = &mutedUntil
		muteTopic := insertUnreadTopic(t, ctx, connection, areas["public"], "Muted reader", []int64{otherID}, baseTime.Add(7*time.Hour))
		if err := MarkTopicRead(ctx, connection, muted, muteTopic.id); err != nil {
			t.Fatalf("muted MarkTopicRead() returned error: %v", err)
		}
		suspended := reader
		suspended.Suspended = true
		if err := MarkTopicRead(ctx, connection, suspended, muteTopic.id); err == nil {
			t.Fatal("suspended MarkTopicRead() returned nil")
		}
	})

	t.Run("cancellation rolls back a blocked marker advance", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Canceled marker", []int64{readerID, otherID}, baseTime.Add(8*time.Hour))
		insertMarker(t, ctx, connection, readerID, topic.id, 1, baseTime)
		_, originalAt := inspectMarker(t, ctx, connection, readerID, topic.id)
		blocker, err := connections[1].Begin(ctx)
		if err != nil {
			t.Fatalf("begin blocker: %v", err)
		}
		if _, err := blocker.Exec(ctx, `SELECT 1 FROM public.topic_reads WHERE user_id = $1 AND topic_id = $2 FOR UPDATE`, readerID, topic.id); err != nil {
			t.Fatalf("lock marker: %v", err)
		}
		blockedContext, blockedCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		err = MarkTopicRead(blockedContext, connections[2], reader, topic.id)
		blockedCancel()
		_ = blocker.Rollback(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked MarkTopicRead() error = %v, want deadline", err)
		}
		if got, readAt := inspectMarker(t, ctx, connection, readerID, topic.id); got != 1 || !readAt.Equal(originalAt) {
			t.Fatalf("canceled marker = (%d, %s), want unchanged", got, readAt)
		}
	})

	t.Run("transaction local lock timeout bounds a blocked advance", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Lock timeout marker", []int64{readerID, otherID}, baseTime.Add(9*time.Hour))
		insertMarker(t, ctx, connection, readerID, topic.id, 1, baseTime)
		_, originalAt := inspectMarker(t, ctx, connection, readerID, topic.id)
		blocker, err := connections[1].Begin(ctx)
		if err != nil {
			t.Fatalf("begin timeout blocker: %v", err)
		}
		defer func() { _ = blocker.Rollback(context.Background()) }()
		if _, err := blocker.Exec(ctx, `SELECT 1 FROM public.topic_reads WHERE user_id = $1 AND topic_id = $2 FOR UPDATE`, readerID, topic.id); err != nil {
			t.Fatalf("lock timeout marker: %v", err)
		}
		started := time.Now()
		err = MarkTopicRead(context.Background(), connections[3], reader, topic.id)
		elapsed := time.Since(started)
		var databaseError *pgconn.PgError
		if !errors.As(err, &databaseError) || databaseError.Code != "55P03" {
			t.Fatalf("locked MarkTopicRead() error = %v, want lock_not_available", err)
		}
		if elapsed < 100*time.Millisecond || elapsed > 2*time.Second {
			t.Fatalf("lock timeout elapsed = %s, want bounded near 250ms", elapsed)
		}
		if got, readAt := inspectMarker(t, ctx, connection, readerID, topic.id); got != 1 || !readAt.Equal(originalAt) {
			t.Fatalf("lock-timeout marker = (%d, %s), want unchanged", got, readAt)
		}
	})

	t.Run("post committed after statement snapshot remains unread", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Post race", []int64{readerID, otherID}, baseTime.Add(10*time.Hour))
		insertMarker(t, ctx, connection, readerID, topic.id, 1, baseTime)
		blocker, err := connections[1].Begin(ctx)
		if err != nil {
			t.Fatalf("begin race blocker: %v", err)
		}
		defer func() { _ = blocker.Rollback(context.Background()) }()
		if _, err := blocker.Exec(ctx, `SELECT 1 FROM public.topic_reads WHERE user_id = $1 AND topic_id = $2 FOR UPDATE`, readerID, topic.id); err != nil {
			t.Fatalf("lock race marker: %v", err)
		}
		result := make(chan error, 1)
		go func() { result <- MarkTopicRead(context.Background(), connections[4], reader, topic.id) }()
		waitForPostgreSQLLock(t, ctx, connection, connections[4].PgConn().PID())
		published, err := CreateReply(ctx, connections[5], func() time.Time { return baseTime.Add(10 * time.Hour) }, other, topic.id, topic.postIDs[0], "post after snapshot")
		if err != nil || published.PostNumber != 3 {
			_ = blocker.Rollback(context.Background())
			t.Fatalf("CreateReply() during marker lock = (%+v, %v)", published, err)
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatalf("release race blocker: %v", err)
		}
		if err := <-result; err != nil {
			t.Fatalf("racing MarkTopicRead() returned error: %v", err)
		}
		if got, _ := inspectMarker(t, ctx, connection, readerID, topic.id); got != 2 {
			t.Fatalf("post-race marker = %d, want snapshot head 2", got)
		}
		if err := MarkTopicRead(ctx, connection, reader, topic.id); err != nil {
			t.Fatalf("follow-up MarkTopicRead() returned error: %v", err)
		}
		if got, _ := inspectMarker(t, ctx, connection, readerID, topic.id); got != 3 {
			t.Fatalf("follow-up marker = %d, want 3", got)
		}
	})

	t.Run("concurrent devices converge", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Concurrent devices", []int64{readerID, otherID, otherID}, baseTime.Add(11*time.Hour))
		start := make(chan struct{})
		devices := []*pgx.Conn{connections[0], connections[1], connections[3], connections[4], connections[5], connections[6], connections[7]}
		errorsChannel := make(chan error, len(devices))
		var wait sync.WaitGroup
		for _, device := range devices {
			device := device
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				errorsChannel <- MarkTopicRead(context.Background(), device, reader, topic.id)
			}()
		}
		close(start)
		wait.Wait()
		close(errorsChannel)
		for err := range errorsChannel {
			if err != nil {
				t.Fatalf("concurrent MarkTopicRead() returned error: %v", err)
			}
		}
		if got, _ := inspectMarker(t, ctx, connection, readerID, topic.id); got != 3 {
			t.Fatalf("concurrent marker = %d, want 3", got)
		}
	})

	t.Run("different boundaries converge in both commit orders", func(t *testing.T) {
		t.Run("lower then higher", func(t *testing.T) {
			topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Lower device first", []int64{readerID, otherID, otherID}, baseTime.Add(12*time.Hour))
			if _, err := connection.Exec(ctx, `UPDATE public.posts SET deleted_at = created_at, deleted_by = $1, deletion_reason = 'lower boundary fixture' WHERE topic_id = $2 AND post_number = 3`, ownerID, topic.id); err != nil {
				t.Fatalf("hide higher boundary: %v", err)
			}
			lower, err := connections[1].BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatalf("begin lower device: %v", err)
			}
			defer func() { _ = lower.Rollback(context.Background()) }()
			queries := storedb.New(lower)
			if err := queries.ConfigureMarkTopicReadTransaction(ctx); err != nil {
				t.Fatalf("configure lower device: %v", err)
			}
			boundary, err := queries.MarkTopicReadBoundary(ctx, storedb.MarkTopicReadBoundaryParams{TopicID: topic.id, ActorUserID: readerID})
			if err != nil || boundary.SelectedPostNumber != 2 || !boundary.Advanced {
				t.Fatalf("lower boundary = (%+v, %v)", boundary, err)
			}
			if _, err := connections[5].Exec(ctx, `UPDATE public.posts SET deleted_at = NULL, deleted_by = NULL, deletion_reason = NULL WHERE topic_id = $1 AND post_number = 3`, topic.id); err != nil {
				t.Fatalf("restore higher boundary: %v", err)
			}
			result := make(chan error, 1)
			go func() { result <- MarkTopicRead(context.Background(), connections[4], reader, topic.id) }()
			waitForPostgreSQLLock(t, ctx, connection, connections[4].PgConn().PID())
			if err := lower.Commit(ctx); err != nil {
				t.Fatalf("commit lower device: %v", err)
			}
			if err := <-result; err != nil {
				t.Fatalf("higher device returned error: %v", err)
			}
			if got, _ := inspectMarker(t, ctx, connection, readerID, topic.id); got != 3 {
				t.Fatalf("lower-then-higher marker = %d, want 3", got)
			}
		})

		t.Run("higher then lower", func(t *testing.T) {
			topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Higher device first", []int64{readerID, otherID, otherID}, baseTime.Add(14*time.Hour))
			higher, err := connections[1].BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatalf("begin higher device: %v", err)
			}
			defer func() { _ = higher.Rollback(context.Background()) }()
			queries := storedb.New(higher)
			if err := queries.ConfigureMarkTopicReadTransaction(ctx); err != nil {
				t.Fatalf("configure higher device: %v", err)
			}
			boundary, err := queries.MarkTopicReadBoundary(ctx, storedb.MarkTopicReadBoundaryParams{TopicID: topic.id, ActorUserID: readerID})
			if err != nil || boundary.SelectedPostNumber != 3 || !boundary.Advanced {
				t.Fatalf("higher boundary = (%+v, %v)", boundary, err)
			}
			if _, err := connections[5].Exec(ctx, `UPDATE public.posts SET deleted_at = created_at, deleted_by = $1, deletion_reason = 'lower boundary fixture' WHERE topic_id = $2 AND post_number = 3`, ownerID, topic.id); err != nil {
				t.Fatalf("lower eligible head: %v", err)
			}
			result := make(chan error, 1)
			go func() { result <- MarkTopicRead(context.Background(), connections[4], reader, topic.id) }()
			waitForPostgreSQLLock(t, ctx, connection, connections[4].PgConn().PID())
			if err := higher.Commit(ctx); err != nil {
				t.Fatalf("commit higher device: %v", err)
			}
			if err := <-result; err != nil {
				t.Fatalf("lower device returned error: %v", err)
			}
			if got, _ := inspectMarker(t, ctx, connection, readerID, topic.id); got != 3 {
				t.Fatalf("higher-then-lower marker = %d, want 3", got)
			}
		})
	})

	t.Run("unknown commit is inspectable and retry safe", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Unknown commit", []int64{readerID, otherID}, baseTime.Add(12*time.Hour))
		unknown := errors.New("simulated lost commit acknowledgement")
		err := MarkTopicRead(ctx, unknownCommitBeginner{connection: connections[3], commitErr: unknown}, reader, topic.id)
		if !errors.Is(err, unknown) || err == nil {
			t.Fatalf("unknown-commit MarkTopicRead() error = %v", err)
		}
		marker, readAt := inspectMarker(t, ctx, connection, readerID, topic.id)
		if marker != 2 {
			t.Fatalf("unknown commit marker = %d, want 2", marker)
		}
		if err := MarkTopicRead(ctx, connection, reader, topic.id); err != nil {
			t.Fatalf("unknown-commit retry returned error: %v", err)
		}
		if got, gotAt := inspectMarker(t, ctx, connection, readerID, topic.id); got != marker || !gotAt.Equal(readAt) {
			t.Fatalf("unknown retry marker = (%d, %s), want unchanged", got, gotAt)
		}
	})

	t.Run("malformed private row fails closed", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Malformed marker", []int64{readerID}, baseTime.Add(13*time.Hour))
		insertMarker(t, ctx, connection, readerID, topic.id, 9, baseTime)
		if err := MarkTopicRead(ctx, connection, reader, topic.id); err == nil {
			t.Fatal("MarkTopicRead() accepted marker beyond topic head")
		}
	})

	t.Run("hard deletion does not decrement and cascades remain explicit", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "Hard purge", []int64{readerID, otherID, otherID}, baseTime.Add(14*time.Hour))
		if err := MarkTopicRead(ctx, connection, reader, topic.id); err != nil {
			t.Fatalf("initial hard-purge marker: %v", err)
		}
		purge, err := connection.Begin(ctx)
		if err != nil {
			t.Fatalf("begin hard purge: %v", err)
		}
		if _, err := purge.Exec(ctx, `DELETE FROM public.posts WHERE topic_id = $1 AND post_number = 2`, topic.id); err != nil {
			_ = purge.Rollback(ctx)
			t.Fatalf("hard purge post: %v", err)
		}
		if _, err := purge.Exec(ctx, `UPDATE public.topics SET reply_count = reply_count - 1 WHERE id = $1`, topic.id); err != nil {
			_ = purge.Rollback(ctx)
			t.Fatalf("repair hard-purge count: %v", err)
		}
		if err := purge.Commit(ctx); err != nil {
			t.Fatalf("commit hard purge: %v", err)
		}
		if got, _ := inspectMarker(t, ctx, connection, readerID, topic.id); got != 3 {
			t.Fatalf("hard purge decremented marker to %d", got)
		}
		cascade := insertUnreadTopic(t, ctx, connection, areas["public"], "Topic cascade", []int64{otherID}, baseTime.Add(15*time.Hour))
		if err := MarkTopicRead(ctx, connection, reader, cascade.id); err != nil {
			t.Fatalf("create cascade marker: %v", err)
		}
		if _, err := connection.Exec(ctx, `DELETE FROM public.topics WHERE id = $1`, cascade.id); err != nil {
			t.Fatalf("hard delete topic: %v", err)
		}
		assertNoMarker(t, ctx, connection, readerID, cascade.id)
	})

	t.Run("publishing creates no marker", func(t *testing.T) {
		before := markerCount(t, ctx, connection)
		created, err := CreateTopic(ctx, connection, func() time.Time { return baseTime.Add(16 * time.Hour) }, reader, "public", "No automatic marker", "body")
		if err != nil {
			t.Fatalf("CreateTopic() returned error: %v", err)
		}
		assertNoMarker(t, ctx, connection, readerID, created.TopicID)
		if _, err := CreateTopic(ctx, connection, time.Now, reader, "read-only", "Denied marker", "body"); !errors.Is(err, ErrPublishingDenied) {
			t.Fatalf("denied CreateTopic() error = %v", err)
		}
		if got := markerCount(t, ctx, connection); got != before {
			t.Fatalf("publishing marker count = %d, want %d", got, before)
		}
	})

	t.Run("first unread and topic page share one private read model", func(t *testing.T) {
		topic := insertUnreadTopic(t, ctx, connection, areas["public"], "First unread", []int64{readerID, otherID, readerID, otherID}, baseTime.Add(17*time.Hour))
		target, err := FirstUnread(ctx, connection, reader, topic.id)
		if err != nil || target != (FirstUnreadTarget{PostID: topic.postIDs[1], Page: 1}) {
			t.Fatalf("initial FirstUnread() = (%+v, %v)", target, err)
		}
		page, err := LoadVisibleTopicPostPage(ctx, connection, reader, topic.id, 1)
		if err != nil || page.ReadState == nil || *page.ReadState != "new" {
			t.Fatalf("initial LoadVisibleTopicPostPage() = (state %v, %v)", page.ReadState, err)
		}
		visitorPage, err := LoadVisibleTopicPostPage(ctx, connection, policy.AccessContext{}, topic.id, 1)
		if err != nil || visitorPage.ReadState != nil {
			t.Fatalf("visitor LoadVisibleTopicPostPage() = (state %v, %v)", visitorPage.ReadState, err)
		}
		if err := MarkTopicRead(ctx, connection, reader, topic.id); err != nil {
			t.Fatalf("MarkTopicRead() returned error: %v", err)
		}
		target, err = FirstUnread(ctx, connection, reader, topic.id)
		if err != nil || target != (FirstUnreadTarget{}) {
			t.Fatalf("read FirstUnread() = (%+v, %v)", target, err)
		}
		page, err = LoadVisibleTopicPostPage(ctx, connection, reader, topic.id, 1)
		if err != nil || page.ReadState == nil || *page.ReadState != "read" {
			t.Fatalf("read LoadVisibleTopicPostPage() = (state %v, %v)", page.ReadState, err)
		}
		groupTopic := insertUnreadTopic(t, ctx, connection, areas["group"], "First unread group", []int64{otherID}, baseTime.Add(18*time.Hour))
		if _, err := FirstUnread(ctx, connection, reader, groupTopic.id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unauthorized FirstUnread() error = %v, want missing", err)
		}
		if target, err := FirstUnread(ctx, connection, readerWithGroup, groupTopic.id); err != nil || target.PostID != groupTopic.postIDs[0] || target.Page != 1 || target.Direct {
			t.Fatalf("authorized group FirstUnread() = (%+v, %v)", target, err)
		}
	})
}

type unreadTopicFixture struct {
	id      int64
	postIDs []int64
}

func insertUnreadTopic(t *testing.T, ctx context.Context, connection *pgx.Conn, areaID int64, title string, authors []int64, createdAt time.Time) unreadTopicFixture {
	t.Helper()
	if len(authors) == 0 {
		t.Fatal("unread fixture requires posts")
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin unread fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var topicID int64
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.topics', 'id'))::bigint`).Scan(&topicID); err != nil {
		t.Fatalf("allocate topic ID: %v", err)
	}
	postIDs := make([]int64, len(authors))
	for index := range postIDs {
		if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.posts', 'id'))::bigint`).Scan(&postIDs[index]); err != nil {
			t.Fatalf("allocate post ID %d: %v", index, err)
		}
	}
	lastAt := createdAt.Add(time.Duration(len(authors)-1) * time.Microsecond)
	if _, err := tx.Exec(ctx, `INSERT INTO public.topics
        (id, area_id, author_id, title, first_post_id, latest_post_id, reply_count, next_post_number,
         created_at, updated_at, last_activity_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
		topicID, areaID, authors[0], title, postIDs[0], postIDs[len(postIDs)-1], len(postIDs)-1, len(postIDs)+1, createdAt, lastAt); err != nil {
		t.Fatalf("insert unread topic: %v", err)
	}
	for index, authorID := range authors {
		var parent any
		if index > 0 {
			parent = postIDs[0]
		}
		at := createdAt.Add(time.Duration(index) * time.Microsecond)
		if _, err := tx.Exec(ctx, `INSERT INTO public.posts
            (id, topic_id, author_id, post_number, markdown_source, rendered_html, renderer_version,
             parent_post_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'fixture', '<p>fixture</p>', $5, $6, $7, $7)`,
			postIDs[index], topicID, authorID, index+1, render.RendererVersion, parent, at); err != nil {
			t.Fatalf("insert unread post %d: %v", index+1, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit unread fixture: %v", err)
	}
	return unreadTopicFixture{id: topicID, postIDs: postIDs}
}

func insertMarker(t *testing.T, ctx context.Context, connection *pgx.Conn, userID, topicID int64, postNumber int32, readAt time.Time) {
	t.Helper()
	if _, err := connection.Exec(ctx, `INSERT INTO public.topic_reads (user_id, topic_id, last_read_post_number, read_at) VALUES ($1, $2, $3, $4)`, userID, topicID, postNumber, readAt); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
}

func inspectMarker(t *testing.T, ctx context.Context, connection *pgx.Conn, userID, topicID int64) (int32, time.Time) {
	t.Helper()
	var postNumber int32
	var readAt time.Time
	if err := connection.QueryRow(ctx, `SELECT last_read_post_number, read_at FROM public.topic_reads WHERE user_id = $1 AND topic_id = $2`, userID, topicID).Scan(&postNumber, &readAt); err != nil {
		t.Fatalf("inspect marker: %v", err)
	}
	return postNumber, readAt
}

func assertNoMarker(t *testing.T, ctx context.Context, connection *pgx.Conn, userID, topicID int64) {
	t.Helper()
	var count int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.topic_reads WHERE user_id = $1 AND topic_id = $2`, userID, topicID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("marker count = (%d, %v), want zero", count, err)
	}
}

func markerCount(t *testing.T, ctx context.Context, connection *pgx.Conn) int {
	t.Helper()
	var count int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM public.topic_reads`).Scan(&count); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	return count
}

func waitForPostgreSQLLock(t *testing.T, ctx context.Context, observer *pgx.Conn, processID uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waitEventType *string
		if err := observer.QueryRow(ctx, `SELECT wait_event_type FROM pg_catalog.pg_stat_activity WHERE pid = $1`, processID).Scan(&waitEventType); err != nil {
			t.Fatalf("inspect PostgreSQL wait: %v", err)
		}
		if waitEventType != nil && *waitEventType == "Lock" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("mark-read transaction did not reach PostgreSQL lock wait")
}

type unknownCommitBeginner struct {
	connection *pgx.Conn
	commitErr  error
}

func (beginner unknownCommitBeginner) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := beginner.connection.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &unknownCommitTx{Tx: tx, commitErr: beginner.commitErr}, nil
}

type unknownCommitTx struct {
	pgx.Tx
	commitErr error
}

func (tx *unknownCommitTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return tx.commitErr
}
