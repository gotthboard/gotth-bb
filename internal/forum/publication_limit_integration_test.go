//go:build integration

package forum

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const publicationLimitTestDatabase = "gotth_bb_an05_02_publication_limit_test"

func TestDurablePublicationAdmissionOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+publicationLimitTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+publicationLimitTestDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+publicationLimitTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = publicationLimitTestDatabase

	beforeAN05 := migrationPrefix(t, 10)
	if err := migration.Apply(ctx, testConfig, beforeAN05); err != nil {
		t.Fatalf("apply migrations 000001-000010: %v", err)
	}
	upgrade, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	var upgradedID int64
	var beforeCTID string
	if err := upgrade.QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Upgrade account', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days')
RETURNING id, ctid::text`).Scan(&upgradedID, &beforeCTID); err != nil {
		t.Fatal(err)
	}
	_ = upgrade.Close(ctx)
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("upgrade through migration 000011: %v", err)
	}

	connections := make([]*pgx.Conn, 4)
	for index := range connections {
		connections[index], err = pgx.ConnectConfig(ctx, testConfig)
		if err != nil {
			t.Fatal(err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}
	var afterCTID string
	var upgradedStart *time.Time
	var upgradedCount int32
	if err := connections[0].QueryRow(ctx, `SELECT ctid::text, publication_window_started_at, publication_count FROM public.users WHERE id=$1`, upgradedID).Scan(&afterCTID, &upgradedStart, &upgradedCount); err != nil || afterCTID != beforeCTID || upgradedStart != nil || upgradedCount != 0 {
		t.Fatalf("upgrade tuple = (ctid %q/%q start %v count %d error %v)", beforeCTID, afterCTID, upgradedStart, upgradedCount, err)
	}
	_, checkErr := connections[0].Exec(ctx, `UPDATE public.users SET publication_count=1 WHERE id=$1`, upgradedID)
	assertPublicationCheckViolation(t, checkErr)
	_, checkErr = connections[0].Exec(ctx, `UPDATE public.users SET publication_window_started_at='infinity', publication_count=1 WHERE id=$1`, upgradedID)
	assertPublicationCheckViolation(t, checkErr)
	_, checkErr = connections[0].Exec(ctx, `INSERT INTO public.users (display_name, created_at) VALUES ('Infinite creation', 'infinity')`)
	assertPublicationCheckViolation(t, checkErr)

	var ownerID, newID, establishedID, mutedID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, role) VALUES ('Owner', 'administrator') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('New account', clock_timestamp() - interval '1 hour', clock_timestamp() - interval '1 hour', clock_timestamp() - interval '1 hour') RETURNING id`).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Established account', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days') RETURNING id`).Scan(&establishedID); err != nil {
		t.Fatal(err)
	}
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, muted_until) VALUES ('Muted account', clock_timestamp() + interval '1 hour') RETURNING id`).Scan(&mutedID); err != nil {
		t.Fatal(err)
	}
	if _, err := connections[0].Exec(ctx, `INSERT INTO public.areas (slug, name, posting_mode, created_by, updated_by)
VALUES ('normal', 'Normal', 'normal', $1, $1), ('staff-only', 'Staff only', 'read_only', $1, $1)`, ownerID); err != nil {
		t.Fatal(err)
	}
	limits, err := abuse.NewPublicationPolicy(3, 2, 10*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	newActor := policy.AccessContext{Authenticated: true, UserID: newID, Role: policy.RoleMember}
	first, err := CreateTopic(ctx, connections[0], limits, newActor, "normal", "First new topic", "body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateReply(ctx, connections[0], limits, newActor, first.TopicID, first.PostID, "second publication"); err != nil {
		t.Fatal(err)
	}
	assertPublicationTuple(t, ctx, connections[0], newID, 2)
	if _, err := EditPost(ctx, connections[0], time.Now, newActor, first.PostID, 1, "edited without publication capacity"); err != nil {
		t.Fatalf("edit without publication capacity: %v", err)
	}
	if _, err := RenderTopicDraft("normal", "Preview without publication capacity", "preview"); err != nil {
		t.Fatalf("preview without publication capacity: %v", err)
	}
	assertPublicationTuple(t, ctx, connections[0], newID, 2)
	if result, err := CreateTopic(ctx, connections[0], limits, newActor, "normal", "Limited new topic", "body"); result != (PublishResult{}) || !errors.Is(err, ErrPublicationRateLimited) {
		t.Fatalf("new-account limit = (%+v, %v)", result, err)
	} else {
		var limited PublicationRateLimitError
		if !errors.As(err, &limited) || limited.RetryAfterSeconds < 1 || limited.RetryAfterSeconds > 600 || strings.Contains(err.Error(), "New account") {
			t.Fatalf("bounded rate error = (%+v, %v)", limited, err)
		}
	}
	assertPublicationTuple(t, ctx, connections[0], newID, 2)

	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET publication_window_started_at=clock_timestamp()-interval '10 minutes', publication_count=2 WHERE id=$1`, newID); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateReply(ctx, connections[0], limits, newActor, first.TopicID, first.PostID, "reset window"); err != nil {
		t.Fatalf("window equality reset: %v", err)
	}
	assertPublicationTuple(t, ctx, connections[0], newID, 1)

	establishedActor := policy.AccessContext{Authenticated: true, UserID: establishedID, Role: policy.RoleMember}
	var emptyConcurrentID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Empty concurrent account', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days') RETURNING id`).Scan(&emptyConcurrentID); err != nil {
		t.Fatal(err)
	}
	emptyActor := policy.AccessContext{Authenticated: true, UserID: emptyConcurrentID, Role: policy.RoleMember}
	emptySucceeded, emptyLimited := runConcurrentPublicationBatch(t, ctx, testConfig, limits, emptyActor, first, 8)
	if emptySucceeded != 3 || emptyLimited != 5 {
		t.Fatalf("empty concurrent outcomes = success %d limited %d", emptySucceeded, emptyLimited)
	}
	assertPublicationTuple(t, ctx, connections[0], emptyConcurrentID, 3)

	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET publication_window_started_at=clock_timestamp(), publication_count=2 WHERE id=$1`, establishedID); err != nil {
		t.Fatal(err)
	}
	succeeded, limited := runConcurrentPublicationBatch(t, ctx, testConfig, limits, establishedActor, first, 8)
	if succeeded != 1 || limited != 7 {
		t.Fatalf("near-limit concurrent outcomes = success %d limited %d", succeeded, limited)
	}
	assertPublicationTuple(t, ctx, connections[0], establishedID, 3)

	for _, role := range []string{"member", "moderator", "administrator"} {
		var roleID int64
		if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, role, created_at, updated_at, last_login_at, publication_window_started_at, publication_count)
VALUES ($1, $2, clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp(), 3) RETURNING id`, "Limited "+role, role).Scan(&roleID); err != nil {
			t.Fatal(err)
		}
		roleValue, valid := publishingRole(role)
		if !valid {
			t.Fatalf("test role %q is invalid", role)
		}
		roleActor := policy.AccessContext{Authenticated: true, UserID: roleID, Role: roleValue}
		if _, err := CreateTopic(ctx, connections[0], limits, roleActor, "normal", "Role limit "+role, "body"); !errors.Is(err, ErrPublicationRateLimited) {
			t.Fatalf("%s publication limit = %v", role, err)
		}
		assertPublicationTuple(t, ctx, connections[0], roleID, 3)
	}

	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET publication_window_started_at=NULL, publication_count=0 WHERE id=$1`, establishedID); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTopic(ctx, connections[0], limits, establishedActor, "staff-only", "Denied target", "body"); !errors.Is(err, ErrPublishingDenied) {
		t.Fatalf("denied target error = %v", err)
	}
	assertPublicationTuple(t, ctx, connections[0], establishedID, 0)

	mutedActor := policy.AccessContext{Authenticated: true, UserID: mutedID, Role: policy.RoleMember}
	if _, err := CreateTopic(ctx, connections[0], limits, mutedActor, "normal", "Cached actor cannot bypass mute", "body"); !errors.Is(err, ErrPublishingDenied) {
		t.Fatalf("live mute revalidation error = %v", err)
	}
	assertPublicationTuple(t, ctx, connections[0], mutedID, 0)
	var suspendedID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name) VALUES ('Suspended account') RETURNING id`).Scan(&suspendedID); err != nil {
		t.Fatal(err)
	}
	if _, err := connections[0].Exec(ctx, `UPDATE public.users SET suspended_at=clock_timestamp(), suspended_until=clock_timestamp()+interval '1 hour', suspension_reason='Publication test' WHERE id=$1`, suspendedID); err != nil {
		t.Fatal(err)
	}
	suspendedActor := policy.AccessContext{Authenticated: true, UserID: suspendedID, Role: policy.RoleMember}
	if _, err := CreateTopic(ctx, connections[0], limits, suspendedActor, "normal", "Cached actor cannot bypass suspension", "body"); !errors.Is(err, ErrPublishingDenied) {
		t.Fatalf("live suspension revalidation error = %v", err)
	}
	assertPublicationTuple(t, ctx, connections[0], suspendedID, 0)

	var transitionID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Concurrent suspension account', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days') RETURNING id`).Scan(&transitionID); err != nil {
		t.Fatal(err)
	}
	transitionActor := policy.AccessContext{Authenticated: true, UserID: transitionID, Role: policy.RoleMember}
	transitionLocker, err := connections[0].Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transitionLocker.Exec(ctx, `UPDATE public.users SET publication_count=publication_count WHERE id=$1`, transitionID); err != nil {
		t.Fatal(err)
	}
	var publishingBackendPID int32
	if err := connections[3].QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&publishingBackendPID); err != nil {
		t.Fatal(err)
	}
	transitionResult := make(chan error, 1)
	go func() {
		_, publishErr := CreateTopic(ctx, connections[3], limits, transitionActor, "normal", "Concurrent suspension", "body")
		transitionResult <- publishErr
	}()
	waitForPublicationLock(t, ctx, connections[1], publishingBackendPID)
	if _, err := transitionLocker.Exec(ctx, `UPDATE public.users SET suspended_at=clock_timestamp(), suspended_until=clock_timestamp()+interval '1 hour', suspension_reason='Concurrent publication test' WHERE id=$1`, transitionID); err != nil {
		t.Fatal(err)
	}
	if err := transitionLocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-transitionResult; !errors.Is(err, ErrPublishingDenied) {
		t.Fatalf("concurrent suspension publication error = %v", err)
	}
	assertPublicationTuple(t, ctx, connections[0], transitionID, 0)

	restarted, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicationTuple(t, ctx, restarted, newID, 1)
	stricter, err := abuse.NewPublicationPolicy(1, 1, 10*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateReply(ctx, restarted, stricter, newActor, first.TopicID, first.PostID, "new policy applies after restart"); !errors.Is(err, ErrPublicationRateLimited) {
		t.Fatalf("restarted stricter policy error = %v", err)
	}
	assertPublicationTuple(t, ctx, restarted, newID, 1)
	if err := restarted.Close(ctx); err != nil {
		t.Fatal(err)
	}

	var unknownCommitID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Unknown commit account', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days') RETURNING id`).Scan(&unknownCommitID); err != nil {
		t.Fatal(err)
	}
	unknownCommitActor := policy.AccessContext{Authenticated: true, UserID: unknownCommitID, Role: policy.RoleMember}
	lostAcknowledgement := errors.New("simulated lost publication commit acknowledgement")
	if result, err := CreateTopic(ctx, publicationUnknownCommitBeginner{connection: connections[0], commitErr: lostAcknowledgement}, limits, unknownCommitActor, "normal", "Unknown commit publication", "body"); result != (PublishResult{}) || !errors.Is(err, lostAcknowledgement) || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("unknown commit publication = (%+v, %v)", result, err)
	}
	var unknownTopics, unknownPosts int64
	if err := connections[0].QueryRow(ctx, `SELECT
    count(DISTINCT topic.id),
    count(post.id)
FROM public.topics AS topic
JOIN public.posts AS post ON post.topic_id=topic.id
WHERE topic.author_id=$1 AND topic.title='Unknown commit publication'`, unknownCommitID).Scan(&unknownTopics, &unknownPosts); err != nil {
		t.Fatal(err)
	}
	if unknownTopics != 1 || unknownPosts != 1 {
		t.Fatalf("unknown commit atomic rows = topics %d posts %d", unknownTopics, unknownPosts)
	}
	assertPublicationTuple(t, ctx, connections[0], unknownCommitID, 1)

	if _, err := connections[0].Exec(ctx, `CREATE FUNCTION public.reject_publication_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reject publication'; END $$;
CREATE TRIGGER reject_publication_test BEFORE INSERT ON public.posts FOR EACH ROW EXECUTE FUNCTION public.reject_publication_test()`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTopic(ctx, connections[0], limits, establishedActor, "normal", "Rollback publication", "body"); err == nil {
		t.Fatal("trigger-rejected publication returned nil")
	}
	assertPublicationTuple(t, ctx, connections[0], establishedID, 0)
	if _, err := connections[0].Exec(ctx, `DROP TRIGGER reject_publication_test ON public.posts; DROP FUNCTION public.reject_publication_test()`); err != nil {
		t.Fatal(err)
	}

	locker, err := connections[0].Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Exec(ctx, `UPDATE public.users SET publication_count=publication_count WHERE id=$1`, establishedID); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, lockErr := CreateTopic(ctx, connections[3], limits, establishedActor, "normal", "Lock timeout", "body")
	if rollbackErr := locker.Rollback(ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	var postgresError *pgconn.PgError
	if !errors.As(lockErr, &postgresError) || postgresError.Code != "55P03" || time.Since(started) > time.Second {
		t.Fatalf("publication lock timeout = (%v, %s)", lockErr, time.Since(started))
	}
	assertPublicationTuple(t, ctx, connections[0], establishedID, 0)

	var independentID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Independent account', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days') RETURNING id`).Scan(&independentID); err != nil {
		t.Fatal(err)
	}
	independentActor := policy.AccessContext{Authenticated: true, UserID: independentID, Role: policy.RoleMember}
	independentLocker, err := connections[0].Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := independentLocker.Exec(ctx, `UPDATE public.users SET publication_count=publication_count WHERE id=$1`, establishedID); err != nil {
		t.Fatal(err)
	}
	independentContext, stopIndependent := context.WithTimeout(ctx, time.Second)
	if _, err := CreateReply(independentContext, connections[3], limits, independentActor, first.TopicID, first.PostID, "separate account does not serialize globally"); err != nil {
		stopIndependent()
		_ = independentLocker.Rollback(ctx)
		t.Fatalf("independent publication while another account is locked: %v", err)
	}
	stopIndependent()
	if err := independentLocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertPublicationTuple(t, ctx, connections[0], independentID, 1)

	var canceledID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
VALUES ('Canceled account', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days', clock_timestamp() - interval '2 days') RETURNING id`).Scan(&canceledID); err != nil {
		t.Fatal(err)
	}
	canceledActor := policy.AccessContext{Authenticated: true, UserID: canceledID, Role: policy.RoleMember}
	cancelLocker, err := connections[0].Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cancelLocker.Exec(ctx, `UPDATE public.users SET publication_count=publication_count WHERE id=$1`, canceledID); err != nil {
		t.Fatal(err)
	}
	canceledContext, cancelPublication := context.WithCancel(ctx)
	cancelTimer := time.AfterFunc(50*time.Millisecond, cancelPublication)
	_, canceledErr := CreateTopic(canceledContext, connections[3], limits, canceledActor, "normal", "Canceled publication", "body")
	cancelTimer.Stop()
	cancelPublication()
	if err := cancelLocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var canceledPostgresError *pgconn.PgError
	if !errors.Is(canceledErr, context.Canceled) && !(errors.As(canceledErr, &canceledPostgresError) && canceledPostgresError.Code == "57014") {
		t.Fatalf("canceled publication error = %v", canceledErr)
	}
	assertPublicationTuple(t, ctx, connections[0], canceledID, 0)
}

func runConcurrentPublicationBatch(t *testing.T, ctx context.Context, config *pgx.ConnConfig, limits abuse.PublicationPolicy, actor policy.AccessContext, target PublishResult, requests int) (int, int) {
	t.Helper()
	connections := make([]*pgx.Conn, requests)
	for index := range connections {
		connection, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		connections[index] = connection
		defer func() { _ = connection.Close(context.Background()) }()
	}
	start := make(chan struct{})
	results := make(chan error, requests)
	var wait sync.WaitGroup
	for index, connection := range connections {
		index, connection := index, connection
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			var publishErr error
			if index%2 == 0 {
				_, publishErr = CreateTopic(ctx, connection, limits, actor, "normal", fmt.Sprintf("Concurrent topic %d/%d", actor.UserID, index), "body")
			} else {
				_, publishErr = CreateReply(ctx, connection, limits, actor, target.TopicID, target.PostID, fmt.Sprintf("concurrent reply %d/%d", actor.UserID, index))
			}
			results <- publishErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, limited := 0, 0
	for publishErr := range results {
		switch {
		case publishErr == nil:
			succeeded++
		case errors.Is(publishErr, ErrPublicationRateLimited):
			limited++
		default:
			t.Fatalf("concurrent publication error: %v", publishErr)
		}
	}
	return succeeded, limited
}

func waitForPublicationLock(t *testing.T, ctx context.Context, observer *pgx.Conn, backendPID int32) {
	t.Helper()
	deadline := time.NewTimer(200 * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := observer.QueryRow(ctx, `SELECT COALESCE(wait_event_type = 'Lock', false) FROM pg_catalog.pg_stat_activity WHERE pid=$1`, backendPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("publication did not wait on the actor row lock")
		case <-ticker.C:
		}
	}
}

type publicationUnknownCommitBeginner struct {
	connection *pgx.Conn
	commitErr  error
}

func (beginner publicationUnknownCommitBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := beginner.connection.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &publicationUnknownCommitTx{Tx: tx, commitErr: beginner.commitErr}, nil
}

type publicationUnknownCommitTx struct {
	pgx.Tx
	commitErr error
}

func (tx *publicationUnknownCommitTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return tx.commitErr
}

func migrationPrefix(t *testing.T, maximum int) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(migrations.Files(), ".")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for index, entry := range entries {
		if index >= maximum {
			break
		}
		body, err := fs.ReadFile(migrations.Files(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = &fstest.MapFile{Data: body, Mode: 0o444}
	}
	return files
}

func assertPublicationTuple(t *testing.T, ctx context.Context, connection *pgx.Conn, userID int64, wantCount int32) {
	t.Helper()
	var startedAt *time.Time
	var count int32
	if err := connection.QueryRow(ctx, `SELECT publication_window_started_at, publication_count FROM public.users WHERE id=$1`, userID).Scan(&startedAt, &count); err != nil || count != wantCount || (wantCount == 0) != (startedAt == nil) {
		t.Fatalf("publication tuple for user %d = (%v, %d, %v), want count %d", userID, startedAt, count, err, wantCount)
	}
}

func assertPublicationCheckViolation(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
		t.Fatalf("publication check violation error = %v", err)
	}
}
