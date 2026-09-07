//go:build integration

package moderation

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/gotthboard/gotth-bb/internal/migration"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const reportModerationTestDatabase = "gotth_bb_an01_report_moderation_test"

func TestReportWorkflowAndExtendedModerationOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+reportModerationTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+reportModerationTestDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, "DROP DATABASE IF EXISTS "+reportModerationTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = reportModerationTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.ConnectConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	createdAt := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	var moderatorID, reporterID, targetUserID int64
	for _, user := range []struct {
		name, role string
		id         *int64
	}{{"Moderator", "moderator", &moderatorID}, {"Reporter", "member", &reporterID}, {"Target", "member", &targetUserID}} {
		if err := connection.QueryRow(ctx, `INSERT INTO public.users (display_name, role, created_at, updated_at, last_login_at) VALUES ($1,$2,$3,$3,$3) RETURNING id`, user.name, user.role, createdAt).Scan(user.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.Exec(ctx, `INSERT INTO public.areas (slug,name,created_by,updated_by) VALUES ('source','Source',$1,$1),('destination','Destination',$1,$1)`, moderatorID); err != nil {
		t.Fatal(err)
	}
	reporter := policy.AccessContext{Authenticated: true, UserID: reporterID, Role: policy.RoleMember}
	target := policy.AccessContext{Authenticated: true, UserID: targetUserID, Role: policy.RoleMember}
	staff := policy.AccessContext{Authenticated: true, UserID: moderatorID, Role: policy.RoleModerator}
	topic, err := forum.CreateTopic(ctx, connection, func() time.Time { return createdAt.Add(time.Minute) }, reporter, "source", "Reported topic", "root")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := forum.CreateReply(ctx, connection, func() time.Time { return createdAt.Add(2 * time.Minute) }, target, topic.TopicID, topic.PostID, "reply")
	if err != nil {
		t.Fatal(err)
	}
	hiddenTopic, err := forum.CreateTopic(ctx, connection, func() time.Time { return createdAt.Add(2 * time.Minute) }, target, "source", "Hidden topic", "hidden root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.topics SET state='hidden' WHERE id=$1`, hiddenTopic.TopicID); err != nil {
		t.Fatal(err)
	}
	if denied, deniedErr := CreateReport(ctx, connection, time.Now, reporter, ReportTopic, hiddenTopic.TopicID, "Cannot see this"); !errors.Is(deniedErr, pgx.ErrNoRows) || denied != (ReportResult{}) {
		t.Fatalf("hidden topic report = (%+v, %v), want zero/not found", denied, deniedErr)
	}
	if denied, deniedErr := CreateReport(ctx, connection, time.Now, reporter, ReportUser, reporterID, "Self report"); !errors.Is(deniedErr, pgx.ErrNoRows) || denied != (ReportResult{}) {
		t.Fatalf("self report = (%+v, %v), want zero/not found", denied, deniedErr)
	}
	report, err := CreateReport(ctx, connection, func() time.Time { return createdAt.Add(3 * time.Minute) }, reporter, ReportTopic, topic.TopicID, "Needs review")
	if err != nil {
		t.Fatal(err)
	}
	postReport, err := CreateReport(ctx, connection, func() time.Time { return createdAt.Add(3*time.Minute + time.Second) }, reporter, ReportPost, reply.PostID, "Post needs review")
	if err != nil || postReport.Target != ReportPost || postReport.TargetID != reply.PostID {
		t.Fatalf("post report = (%+v, %v)", postReport, err)
	}
	userReport, err := CreateReport(ctx, connection, func() time.Time { return createdAt.Add(3*time.Minute + 2*time.Second) }, reporter, ReportUser, targetUserID, "Author needs review")
	if err != nil || userReport.Target != ReportUser || userReport.TargetID != targetUserID {
		t.Fatalf("user report = (%+v, %v)", userReport, err)
	}
	if _, duplicateErr := CreateReport(ctx, connection, time.Now, reporter, ReportTopic, topic.TopicID, "Again"); !errors.Is(duplicateErr, ErrReportDuplicate) {
		t.Fatalf("duplicate = %v", duplicateErr)
	}
	queries := db.New(connection)
	queue, err := store.ListModerationReports(ctx, queries, staff, 1, createdAt.Add(4*time.Minute))
	if err != nil || queue.Total != 3 || len(queue.Reports) != 3 ||
		queue.Reports[0].ID != report.ReportID || queue.Reports[1].ID != postReport.ReportID ||
		queue.Reports[2].ID != userReport.ReportID {
		t.Fatalf("queue = (%+v, %v)", queue, err)
	}
	emptyDetail, err := store.GetModerationReport(ctx, queries, staff, report.ReportID, createdAt.Add(4*time.Minute))
	if err != nil || emptyDetail.ID != report.ReportID || emptyDetail.TargetPage != 0 || len(emptyDetail.Notes) != 0 {
		t.Fatalf("empty report detail = (%+v, %v)", emptyDetail, err)
	}
	postDetail, err := store.GetModerationReport(ctx, queries, staff, postReport.ReportID, createdAt.Add(4*time.Minute))
	if err != nil || postDetail.TargetType != "post" || postDetail.TargetTopicID != topic.TopicID ||
		postDetail.TargetID != reply.PostID || postDetail.TargetPage != 1 || len(postDetail.Notes) != 0 {
		t.Fatalf("post report detail = (%+v, %v)", postDetail, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.users SET muted_until=$1 WHERE id=$2`, createdAt.Add(time.Hour), moderatorID); err != nil {
		t.Fatal(err)
	}
	if deniedDetail, deniedErr := store.GetModerationReport(ctx, queries, staff, report.ReportID, createdAt.Add(4*time.Minute)); !errors.Is(deniedErr, pgx.ErrNoRows) || deniedDetail.ID != 0 {
		t.Fatalf("muted report detail = (%+v, %v), want zero/not found", deniedDetail, deniedErr)
	}
	if _, err := connection.Exec(ctx, `UPDATE public.users SET muted_until=NULL WHERE id=$1`, moderatorID); err != nil {
		t.Fatal(err)
	}
	request := func(value byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{value}, Valid: true} }
	claimed, err := ProcessReport(ctx, connection, func() time.Time { return createdAt.Add(5 * time.Minute) }, staff, report.ReportID, ClaimReport, "", request(1))
	if err != nil || claimed.Status != "in_review" {
		t.Fatalf("claim = (%+v, %v)", claimed, err)
	}
	queue, err = store.ListModerationReports(ctx, queries, staff, 1, createdAt.Add(5*time.Minute))
	if err != nil || len(queue.Reports) != 3 || queue.Reports[0].Status != "open" ||
		queue.Reports[1].Status != "open" || queue.Reports[2].ID != report.ReportID ||
		queue.Reports[2].Status != "in_review" {
		t.Fatalf("queue after claim = (%+v, %v)", queue, err)
	}
	noted, err := ProcessReport(ctx, connection, func() time.Time { return createdAt.Add(6 * time.Minute) }, staff, report.ReportID, NoteReport, "Checked context", request(2))
	if err != nil || noted.NoteID <= 0 {
		t.Fatalf("note = (%+v, %v)", noted, err)
	}
	detail, err := store.GetModerationReport(ctx, queries, staff, report.ReportID, createdAt.Add(7*time.Minute))
	if err != nil || len(detail.Notes) != 1 || detail.Notes[0].Body != "Checked context" {
		t.Fatalf("detail = (%+v, %v)", detail, err)
	}
	finished, err := ProcessReport(ctx, connection, func() time.Time { return createdAt.Add(8 * time.Minute) }, staff, report.ReportID, ResolveReport, "Handled", request(3))
	if err != nil || finished.Status != "resolved" {
		t.Fatalf("resolve = (%+v, %v)", finished, err)
	}
	if repeated, repeatedErr := ProcessReport(ctx, connection, time.Now, staff, report.ReportID, DismissReport, "Changed mind", request(4)); !errors.Is(repeatedErr, ErrReportConflict) || repeated != (ReportActionResult{}) {
		t.Fatalf("terminal report transition = (%+v, %v), want zero/conflict", repeated, repeatedErr)
	}
	for index, input := range []ExtendedActionInput{
		{Action: PinTopic, TargetID: topic.TopicID, Reason: "Important"},
		{Action: UnpinTopic, TargetID: topic.TopicID, Reason: "No longer important"},
		{Action: MoveTopic, TargetID: topic.TopicID, Reason: "Better location", DestinationAreaSlug: "destination"},
		{Action: HidePost, TargetID: reply.PostID, Reason: "Review"},
		{Action: RestorePost, TargetID: reply.PostID, Reason: "Restored"},
		{Action: RedactPost, TargetID: reply.PostID, Reason: "Sensitive content"},
		{Action: WarnUser, TargetID: targetUserID, Reason: "Policy warning"},
		{Action: MuteUser, TargetID: targetUserID, Reason: "Cooling off", MuteDuration: 24 * time.Hour},
	} {
		result, actionErr := ApplyExtendedAction(ctx, connection, func() time.Time { return createdAt.Add(time.Duration(10+index) * time.Minute) }, staff, input, request(byte(10+index)))
		if actionErr != nil || result.Action != input.Action || result.TargetID != input.TargetID || result.AuditID <= 0 {
			t.Fatalf("action %s = (%+v, %v)", input.Action, result, actionErr)
		}
		switch input.Action {
		case PinTopic, UnpinTopic, MoveTopic:
			if result.TopicID != topic.TopicID || result.TargetPage != 0 || result.WarningID != 0 || result.MutedUntil != nil {
				t.Fatalf("topic action %s returned malformed variant: %+v", input.Action, result)
			}
		case HidePost, RestorePost, RedactPost:
			if result.TopicID != topic.TopicID || result.TargetPage != 1 || result.WarningID != 0 || result.MutedUntil != nil {
				t.Fatalf("post action %s returned malformed variant: %+v", input.Action, result)
			}
		case WarnUser:
			if result.TopicID != 0 || result.TargetPage != 0 || result.WarningID <= 0 || result.MutedUntil != nil {
				t.Fatalf("warning returned malformed variant: %+v", result)
			}
		case MuteUser:
			if result.TopicID != 0 || result.TargetPage != 0 || result.WarningID != 0 || result.MutedUntil == nil {
				t.Fatalf("mute returned malformed variant: %+v", result)
			}
		}
	}
	var areaSlug, markdown, rendered, renderer string
	var redacted, redactionProjectionExact bool
	var warningCount, auditCount int64
	if err := connection.QueryRow(ctx, `SELECT area.slug, post.markdown_source, post.rendered_html, post.renderer_version,
post.redacted_at IS NOT NULL,
post.search_vector = pg_catalog.to_tsvector('pg_catalog.simple'::pg_catalog.regconfig, '')
    AND post.search_projection_version = $4,
(SELECT count(*) FROM public.user_warnings WHERE user_id=$2),
(SELECT count(*) FROM public.moderation_actions)
FROM public.topics topic
JOIN public.areas area ON area.id=topic.area_id
JOIN public.posts post ON post.id=$3
WHERE topic.id=$1`, topic.TopicID, targetUserID, reply.PostID, render.SearchProjectionVersion).Scan(&areaSlug, &markdown, &rendered, &renderer, &redacted, &redactionProjectionExact, &warningCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if areaSlug != "destination" || markdown != "[Content removed by moderation]" || rendered != "<p>Content removed by moderation.</p>" || renderer != "moderation-redaction-v1" || !redacted || !redactionProjectionExact || warningCount != 1 || auditCount != 11 {
		t.Fatalf("persisted state = %q %q %q %q redacted=%t projected=%t warnings=%d audits=%d", areaSlug, markdown, rendered, renderer, redacted, redactionProjectionExact, warningCount, auditCount)
	}
	staffPage, err := store.GetVisibleTopicPostPage(ctx, queries, topic.TopicID, 1, staff)
	if err != nil || len(staffPage.Rows) != 2 || !staffPage.Rows[1].IsTombstone.Valid || !staffPage.Rows[1].IsTombstone.Bool || !staffPage.Rows[1].IsRedacted.Valid || !staffPage.Rows[1].IsRedacted.Bool {
		t.Fatalf("staff redacted-post view = (%+v, %v)", staffPage, err)
	}
	memberPage, err := store.GetVisibleTopicPostPage(ctx, queries, topic.TopicID, 1, reporter)
	if err != nil || len(memberPage.Rows) != 1 || memberPage.TotalPosts != 1 {
		t.Fatalf("member redacted-leaf view = (%+v, %v)", memberPage, err)
	}
}
