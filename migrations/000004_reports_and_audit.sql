-- Lock risk: creates new relations and indexes only.
-- Rewrite risk: none on a fresh database. Audit rows are append-only to runtime roles.

CREATE TABLE public.reports (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reported_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    topic_id bigint REFERENCES public.topics (id) ON DELETE RESTRICT,
    post_id bigint REFERENCES public.posts (id) ON DELETE RESTRICT,
    user_id bigint REFERENCES public.users (id) ON DELETE RESTRICT,
    reason text NOT NULL,
    status text NOT NULL DEFAULT 'open',
    assigned_to bigint REFERENCES public.users (id) ON DELETE RESTRICT,
    resolution text,
    resolved_by bigint REFERENCES public.users (id) ON DELETE RESTRICT,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    resolved_at timestamp with time zone,
    CONSTRAINT reports_exactly_one_target CHECK (num_nonnulls(topic_id, post_id, user_id) = 1),
    CONSTRAINT reports_reason_length CHECK (char_length(reason) BETWEEN 1 AND 2000),
    CONSTRAINT reports_status_closed CHECK (status IN ('open', 'in_review', 'resolved', 'dismissed')),
    CONSTRAINT reports_resolution_state_consistent CHECK (
        (status IN ('open', 'in_review')
         AND resolution IS NULL AND resolved_by IS NULL AND resolved_at IS NULL)
        OR
        (status IN ('resolved', 'dismissed')
         AND resolution IS NOT NULL
         AND char_length(resolution) BETWEEN 1 AND 2000
         AND resolved_by IS NOT NULL
         AND resolved_at IS NOT NULL
         AND resolved_at >= created_at)
    ),
    CONSTRAINT reports_timestamps_ordered CHECK (updated_at >= created_at)
);

CREATE INDEX reports_queue_idx
    ON public.reports (status, created_at, id)
    WHERE status IN ('open', 'in_review');

CREATE UNIQUE INDEX reports_open_topic_reporter_unique
    ON public.reports (reported_by, topic_id)
    WHERE topic_id IS NOT NULL AND status IN ('open', 'in_review');

CREATE UNIQUE INDEX reports_open_post_reporter_unique
    ON public.reports (reported_by, post_id)
    WHERE post_id IS NOT NULL AND status IN ('open', 'in_review');

CREATE UNIQUE INDEX reports_open_user_reporter_unique
    ON public.reports (reported_by, user_id)
    WHERE user_id IS NOT NULL AND status IN ('open', 'in_review');

CREATE TABLE public.moderation_actions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_kind text NOT NULL,
    actor_user_id bigint REFERENCES public.users (id) ON DELETE RESTRICT,
    operator_identifier text,
    target_type text NOT NULL,
    target_user_id bigint REFERENCES public.users (id) ON DELETE RESTRICT,
    target_group_id bigint REFERENCES public.forum_groups (id) ON DELETE RESTRICT,
    target_area_id bigint REFERENCES public.areas (id) ON DELETE RESTRICT,
    target_topic_id bigint REFERENCES public.topics (id) ON DELETE RESTRICT,
    target_post_id bigint REFERENCES public.posts (id) ON DELETE RESTRICT,
    target_report_id bigint REFERENCES public.reports (id) ON DELETE RESTRICT,
    action_type text NOT NULL,
    reason text,
    previous_state jsonb NOT NULL DEFAULT '{}'::jsonb,
    resulting_state jsonb NOT NULL DEFAULT '{}'::jsonb,
    request_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT moderation_actions_actor_kind_closed CHECK (actor_kind IN ('forum_user', 'operator')),
    CONSTRAINT moderation_actions_actor_consistent CHECK (
        (actor_kind = 'forum_user'
         AND actor_user_id IS NOT NULL
         AND operator_identifier IS NULL)
        OR
        (actor_kind = 'operator'
         AND actor_user_id IS NULL
         AND operator_identifier IS NOT NULL
         AND char_length(operator_identifier) BETWEEN 1 AND 200)
    ),
    CONSTRAINT moderation_actions_target_type_closed CHECK (
        target_type IN ('user', 'group', 'area', 'topic', 'post', 'report')
    ),
    CONSTRAINT moderation_actions_target_consistent CHECK (
        num_nonnulls(target_user_id, target_group_id, target_area_id, target_topic_id, target_post_id, target_report_id) = 1
        AND (target_type <> 'user' OR target_user_id IS NOT NULL)
        AND (target_type <> 'group' OR target_group_id IS NOT NULL)
        AND (target_type <> 'area' OR target_area_id IS NOT NULL)
        AND (target_type <> 'topic' OR target_topic_id IS NOT NULL)
        AND (target_type <> 'post' OR target_post_id IS NOT NULL)
        AND (target_type <> 'report' OR target_report_id IS NOT NULL)
    ),
    CONSTRAINT moderation_actions_action_type_closed CHECK (
        action_type IN (
            'bootstrap_administrator', 'change_role',
            'grant_group_membership', 'revoke_group_membership',
            'create_area', 'update_area', 'change_area_visibility', 'change_area_posting_mode',
            'lock_topic', 'unlock_topic', 'hide_topic', 'restore_topic',
            'pin_topic', 'unpin_topic', 'move_topic', 'archive_topic',
            'hide_post', 'restore_post', 'redact_post',
            'warn_user', 'mute_user', 'suspend_user', 'reinstate_user',
            'assign_report', 'resolve_report', 'dismiss_report'
        )
    ),
    CONSTRAINT moderation_actions_reason_length CHECK (
        reason IS NULL OR char_length(reason) BETWEEN 1 AND 2000
    ),
    CONSTRAINT moderation_actions_reason_required CHECK (
        action_type NOT IN (
            'move_topic', 'hide_topic', 'archive_topic', 'hide_post', 'redact_post',
            'warn_user', 'mute_user', 'suspend_user', 'resolve_report', 'dismiss_report'
        )
        OR reason IS NOT NULL
    ),
    CONSTRAINT moderation_actions_previous_state_object CHECK (
        jsonb_typeof(previous_state) = 'object' AND octet_length(previous_state::text) <= 16384
    ),
    CONSTRAINT moderation_actions_resulting_state_object CHECK (
        jsonb_typeof(resulting_state) = 'object' AND octet_length(resulting_state::text) <= 16384
    )
);

CREATE INDEX moderation_actions_created_idx
    ON public.moderation_actions (created_at DESC, id DESC);

CREATE INDEX moderation_actions_actor_idx
    ON public.moderation_actions (actor_user_id, created_at DESC)
    WHERE actor_user_id IS NOT NULL;

CREATE INDEX moderation_actions_topic_idx
    ON public.moderation_actions (target_topic_id, created_at DESC)
    WHERE target_topic_id IS NOT NULL;

CREATE INDEX moderation_actions_user_idx
    ON public.moderation_actions (target_user_id, created_at DESC)
    WHERE target_user_id IS NOT NULL;
