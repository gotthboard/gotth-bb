package db

import (
	"strings"
	"testing"
)

func TestReportQueriesPreserveAccessConcurrencyAndAuditContracts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, query string
		required    []string
	}{
		{name: "topic submission", query: createTopicReport, required: []string{"topic.deleted_at IS NULL", "area.visibility = 'groups'", "membership.group_id = ANY", "RETURNING id"}},
		{name: "post submission", query: createPostReport, required: []string{"post.deleted_at IS NULL", "topic.deleted_at IS NULL", "area.visibility = 'groups'"}},
		{name: "user submission", query: createUserReport, required: []string{"target.id <> $1", "post.author_id = target.id", "post.deleted_at IS NULL", "membership.group_id = ANY"}},
		{name: "queue", query: listActiveReportsForModeration, required: []string{"actor.role IN ('moderator', 'administrator')", "actor.suspended_at IS NULL", "actor.muted_until IS NULL", "count(*) OVER", "LIMIT", "OFFSET"}},
		{name: "detail", query: getReportForModeration, required: []string{"actor.role IN ('moderator', 'administrator')", "ranked_post.thread_path <= post.thread_path", "(count(*) - 1) / 25"}},
		{name: "notes read", query: listReportNotes, required: []string{"report.id AS authorized_report_id", "JOIN public.users AS actor", "LEFT JOIN public.report_notes", "actor.muted_until IS NULL"}},
		{name: "claim", query: claimReportAndAudit, required: []string{"status = 'in_review'", "status = 'open'", "assigned_to IS NULL", "'assign_report'", "INSERT INTO public.moderation_actions"}},
		{name: "note", query: addReportNoteAndAudit, required: []string{"INSERT INTO public.report_notes", "report.status = 'in_review'", "report.assigned_to IS NOT NULL", "'note_report'", "INSERT INTO public.moderation_actions"}},
		{name: "finish", query: finishReportAndAudit, required: []string{"report.status = 'in_review'", "report.assigned_to IS NOT NULL", "resolved_at = GREATEST", "INSERT INTO public.moderation_actions"}},
		{name: "pin", query: changeTopicPinAndAudit, required: []string{"pinned_at IS NOT DISTINCT FROM", "INSERT INTO public.moderation_actions"}},
		{name: "move", query: moveTopicAndAudit, required: []string{"SET area_id", "slug = NULL", "'move_topic'"}},
		{name: "post visibility", query: changePostVisibilityAndAudit, required: []string{"post.redacted_at IS NULL", "deleted_at IS NOT DISTINCT FROM", "INSERT INTO public.moderation_actions"}},
		{name: "redaction", query: redactPostAndAudit, required: []string{"markdown_source = '[Content removed by moderation]'", "renderer_version = 'moderation-redaction-v1'", "revision = post.revision + 1", "'redact_post'"}},
		{name: "warning", query: warnUserAndAudit, required: []string{"INSERT INTO public.user_warnings", "'warn_user'", "INSERT INTO public.moderation_actions"}},
		{name: "mute", query: muteUserAndAudit, required: []string{"target.muted_until IS NULL", "'mute_user'", "INSERT INTO public.moderation_actions"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, required := range test.required {
				if !strings.Contains(test.query, required) {
					t.Fatalf("query lacks %q", required)
				}
			}
		})
	}
}
