//go:build integration

package forum

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const an05AdmissionDatabase = "gotth_bb_an05_04_publication_admission"

func TestAN05PublicationPopulationAdmissionOnPostgreSQL17(t *testing.T) {
	if os.Getenv("GOTTH_BB_RUN_AN05_ADMISSION_EVIDENCE") != "1" {
		t.Skip("set GOTTH_BB_RUN_AN05_ADMISSION_EVIDENCE=1 on the designated evidence host")
	}
	const (
		accounts       = 1000
		repliesPerUser = 2
		maximumConns   = 32
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+an05AdmissionDatabase+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+an05AdmissionDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+an05AdmissionDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = an05AdmissionDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	observer, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close(context.Background()) })

	var ownerID int64
	if err := observer.QueryRow(ctx, `INSERT INTO public.users (display_name, role, created_at)
VALUES ('AN05 admission owner', 'administrator', clock_timestamp() - interval '2 days') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Exec(ctx, `INSERT INTO public.areas (slug, name, posting_mode, created_by, updated_by)
VALUES ('an05-admission', 'AN05 admission', 'normal', $1, $1)`, ownerID); err != nil {
		t.Fatal(err)
	}
	rows, err := observer.Query(ctx, `INSERT INTO public.users (display_name, created_at, updated_at, last_login_at)
SELECT 'AN05 account ' || series,
       clock_timestamp() - interval '2 days',
       clock_timestamp() - interval '2 days',
       clock_timestamp() - interval '2 days'
FROM generate_series(1, $1) AS series
RETURNING id`, accounts)
	if err != nil {
		t.Fatal(err)
	}
	userIDs := make([]int64, 0, accounts)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(userIDs) != accounts {
		t.Fatalf("inserted accounts = %d", len(userIDs))
	}
	var baselineConnections int64
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_stat_activity
WHERE datname=current_database()`).Scan(&baselineConnections); err != nil {
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(testConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = an05AdmissionDatabase
	poolConfig.MaxConns = maximumConns
	poolConfig.MinConns = maximumConns
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	limits, err := abuse.NewPublicationPolicy(3, 2, 10*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	targets := make([]PublishResult, accounts)
	actors := make([]policy.AccessContext, accounts)
	for index, userID := range userIDs {
		actors[index] = policy.AccessContext{Authenticated: true, UserID: userID, Role: policy.RoleMember}
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	beforeRSS := an05AdmissionRSSKiB()
	var peakConnections, peakLocks, peakWaiting atomic.Int64
	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			var connections, locks, waiting int64
			err := observer.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname=current_database()),
    (SELECT count(*) FROM pg_catalog.pg_locks WHERE database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())),
    (SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock')`).Scan(&connections, &locks, &waiting)
			if err == nil {
				retainAN05Peak(&peakConnections, connections)
				retainAN05Peak(&peakLocks, locks)
				retainAN05Peak(&peakWaiting, waiting)
			}
			select {
			case <-stopMonitor:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	started := time.Now()
	runAN05PublicationWave(t, accounts, func(index int) error {
		created, err := CreateTopic(ctx, pool, limits, testDestinationPolicy, actors[index], "an05-admission", fmt.Sprintf("AN05 admission topic %d", index+1), "population root")
		targets[index] = created
		return err
	})
	topicElapsed := time.Since(started)
	replyStarted := time.Now()
	runAN05PublicationWave(t, accounts*repliesPerUser, func(task int) error {
		index := task / repliesPerUser
		_, err := CreateReply(ctx, pool, limits, testDestinationPolicy, actors[index], targets[index].TopicID, targets[index].PostID, fmt.Sprintf("population reply %d", task%repliesPerUser+1))
		return err
	})
	replyElapsed := time.Since(replyStarted)
	close(stopMonitor)
	<-monitorDone

	var beforeCanceled int32
	if err := observer.QueryRow(ctx, `SELECT publication_count FROM public.users WHERE id=$1`, userIDs[0]).Scan(&beforeCanceled); err != nil {
		t.Fatal(err)
	}
	canceled, stopCanceled := context.WithCancel(ctx)
	stopCanceled()
	if _, err := CreateReply(canceled, pool, limits, testDestinationPolicy, actors[0], targets[0].TopicID, targets[0].PostID, "canceled reply"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publication = %v", err)
	}
	var afterCanceled int32
	if err := observer.QueryRow(ctx, `SELECT publication_count FROM public.users WHERE id=$1`, userIDs[0]).Scan(&afterCanceled); err != nil || afterCanceled != beforeCanceled {
		t.Fatalf("canceled publication tuple = before %d after %d error %v", beforeCanceled, afterCanceled, err)
	}

	topicIDs := make([]int64, accounts)
	for index, target := range targets {
		if target.TopicID <= 0 || target.PostID <= 0 {
			t.Fatalf("target %d = %+v", index, target)
		}
		topicIDs[index] = target.TopicID
	}
	var admittedUsers, admittedTopics, admittedPosts int64
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM public.users
WHERE id=ANY($1::bigint[]) AND publication_count=3 AND publication_window_started_at IS NOT NULL`, userIDs).Scan(&admittedUsers); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM public.topics
WHERE id=ANY($1::bigint[]) AND next_post_number=4 AND reply_count=2`, topicIDs).Scan(&admittedTopics); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM public.posts WHERE topic_id=ANY($1::bigint[])`, topicIDs).Scan(&admittedPosts); err != nil {
		t.Fatal(err)
	}
	if admittedUsers != accounts || admittedTopics != accounts || admittedPosts != accounts*(repliesPerUser+1) {
		t.Fatalf("admitted state = users %d topics %d posts %d", admittedUsers, admittedTopics, admittedPosts)
	}
	pool.Close()
	finalConnections := waitForAN05AdmissionConnectionBaseline(t, ctx, observer, baselineConnections)
	var databaseBytes, userBytes, topicBytes, postBytes, tempFiles, tempBytes int64
	if err := observer.QueryRow(ctx, `SELECT
    pg_database_size(current_database()),
    pg_total_relation_size('public.users'),
    pg_total_relation_size('public.topics'),
    pg_total_relation_size('public.posts'),
    temp_files,
    temp_bytes
FROM pg_stat_database WHERE datname=current_database()`).Scan(&databaseBytes, &userBytes, &topicBytes, &postBytes, &tempFiles, &tempBytes); err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("AN05_PUBLICATION_POPULATION accounts=%d publications=%d topic_elapsed=%s reply_elapsed=%s max_pool_connections=%d baseline_database_connections=%d peak_database_connections=%d final_database_connections=%d peak_database_locks=%d peak_lock_waiters=%d cancellation=pass statement_timeout=2s lock_timeout=250ms database_bytes=%d users_bytes=%d topics_bytes=%d posts_bytes=%d temp_files=%d temp_bytes=%d heap_alloc_delta=%d total_alloc_delta=%d sys_delta=%d rss_kib_before=%s rss_kib_after=%s",
		accounts, accounts*(repliesPerUser+1), topicElapsed, replyElapsed, maximumConns,
		baselineConnections, peakConnections.Load(), finalConnections, peakLocks.Load(), peakWaiting.Load(), databaseBytes, userBytes, topicBytes, postBytes,
		tempFiles, tempBytes, int64(after.HeapAlloc)-int64(before.HeapAlloc), after.TotalAlloc-before.TotalAlloc,
		int64(after.Sys)-int64(before.Sys), beforeRSS, an05AdmissionRSSKiB())
}

func waitForAN05AdmissionConnectionBaseline(t *testing.T, ctx context.Context, observer *pgx.Conn, baseline int64) int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var connections int64
		if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_stat_activity
WHERE datname=current_database()`).Scan(&connections); err != nil {
			t.Fatal(err)
		}
		if connections == baseline {
			return connections
		}
		if time.Now().After(deadline) {
			t.Fatalf("database connections after pool close = %d, want baseline %d", connections, baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runAN05PublicationWave(t *testing.T, tasks int, operation func(int) error) {
	t.Helper()
	start := make(chan struct{})
	errorsFound := make(chan error, tasks)
	var group sync.WaitGroup
	for task := range tasks {
		task := task
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if err := operation(task); err != nil {
				errorsFound <- fmt.Errorf("task %d: %w", task, err)
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
}

func retainAN05Peak(peak *atomic.Int64, value int64) {
	for value > peak.Load() {
		if peak.CompareAndSwap(peak.Load(), value) {
			return
		}
	}
}

func an05AdmissionRSSKiB() string {
	contents, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
		}
	}
	return "unavailable"
}
