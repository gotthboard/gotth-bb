-- Lock risk: ALTER TABLE takes brief ACCESS EXCLUSIVE locks on reports,
-- moderation_actions, and posts. Existing post rows are not rewritten because
-- every added column is nullable without a volatile default.
-- Rewrite risk: none. New relations and indexes are built from empty tables;
-- constraint validation scans only the affected existing relations.

CREATE TABLE public.report_notes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    report_id bigint NOT NULL REFERENCES public.reports (id) ON DELETE RESTRICT,
    author_id bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    body text NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT report_notes_body_length CHECK (char_length(body) BETWEEN 1 AND 2000)
);

CREATE INDEX report_notes_report_created_idx
    ON public.report_notes (report_id, created_at, id);

CREATE TABLE public.user_warnings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    warned_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    reason text NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT user_warnings_reason_length CHECK (char_length(reason) BETWEEN 1 AND 2000)
);

CREATE INDEX user_warnings_user_created_idx
    ON public.user_warnings (user_id, created_at DESC, id DESC);

-- Alpha.1 exposed the report schema before it exposed report processing. Its
-- constraints permitted assignment/state combinations that AN-01 closes.
-- Preserve assignment when present, reopen unassigned work, and recover a
-- terminal assignee from the already-required resolver before validation.
UPDATE public.reports
SET status = 'in_review'
WHERE status = 'open' AND assigned_to IS NOT NULL;

UPDATE public.reports
SET status = 'open'
WHERE status = 'in_review' AND assigned_to IS NULL;

UPDATE public.reports
SET assigned_to = resolved_by
WHERE status IN ('resolved', 'dismissed') AND assigned_to IS NULL;

ALTER TABLE public.reports
    ADD CONSTRAINT reports_assignment_state_consistent CHECK (
        (status = 'open' AND assigned_to IS NULL)
        OR
        (status IN ('in_review', 'resolved', 'dismissed') AND assigned_to IS NOT NULL)
    );

ALTER TABLE public.posts
    ADD COLUMN redacted_at timestamp with time zone,
    ADD COLUMN redacted_by bigint REFERENCES public.users (id) ON DELETE RESTRICT,
    ADD COLUMN redaction_reason text,
    ADD CONSTRAINT posts_redaction_state_consistent CHECK (
        (redacted_at IS NULL AND redacted_by IS NULL AND redaction_reason IS NULL)
        OR
        (redacted_at IS NOT NULL
         AND redacted_by IS NOT NULL
         AND redaction_reason IS NOT NULL
         AND char_length(redaction_reason) BETWEEN 1 AND 500
         AND markdown_source = '[Content removed by moderation]'
         AND rendered_html = '<p>Content removed by moderation.</p>'
         AND renderer_version = 'moderation-redaction-v1'
         AND redacted_at >= created_at
         AND deleted_at IS NOT DISTINCT FROM redacted_at
         AND deleted_by IS NOT DISTINCT FROM redacted_by
         AND deletion_reason IS NOT DISTINCT FROM redaction_reason)
    );

ALTER TABLE public.moderation_actions
    DROP CONSTRAINT moderation_actions_action_type_closed,
    ADD CONSTRAINT moderation_actions_action_type_closed CHECK (
        action_type IN (
            'bootstrap_administrator', 'change_role',
            'grant_group_membership', 'revoke_group_membership',
            'create_area', 'update_area', 'change_area_visibility', 'change_area_posting_mode',
            'lock_topic', 'unlock_topic', 'hide_topic', 'restore_topic',
            'pin_topic', 'unpin_topic', 'move_topic', 'archive_topic',
            'hide_post', 'restore_post', 'redact_post',
            'warn_user', 'mute_user', 'suspend_user', 'reinstate_user',
            'assign_report', 'note_report', 'resolve_report', 'dismiss_report'
        )
    );
