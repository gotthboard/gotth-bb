-- name: LockTopicForModeration :one
SELECT topic.id, topic.state
FROM public.topics AS topic
WHERE topic.id = sqlc.arg(topic_id)
  AND topic.deleted_at IS NULL
FOR UPDATE OF topic;

-- name: ChangeTopicStateAndAudit :one
WITH changed AS (
    UPDATE public.topics AS topic
    SET state = sqlc.arg(resulting_state),
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, topic.updated_at)
    WHERE topic.id = sqlc.arg(topic_id)
      AND topic.state = sqlc.arg(previous_state)
      AND topic.deleted_at IS NULL
    RETURNING topic.id, topic.state, topic.updated_at
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind,
        actor_user_id,
        target_type,
        target_topic_id,
        action_type,
        reason,
        previous_state,
        resulting_state,
        request_id,
        created_at
    )
    SELECT
        'forum_user',
        sqlc.arg(actor_user_id),
        'topic',
        changed.id,
        sqlc.arg(action_type),
        sqlc.arg(reason),
        jsonb_build_object('state', sqlc.arg(previous_state)::text),
        jsonb_build_object('state', changed.state),
        sqlc.arg(request_id),
        changed.updated_at
    FROM changed
    RETURNING id, target_topic_id
)
SELECT changed.id AS topic_id, changed.state, changed.updated_at, audit.id AS audit_id
FROM changed
JOIN audit ON audit.target_topic_id = changed.id;

-- name: LockUserForSuspension :one
SELECT
    forum_user.id,
    forum_user.role,
    forum_user.suspended_at,
    forum_user.suspended_until,
    forum_user.suspension_reason,
    forum_user.muted_until,
    forum_user.created_at,
    forum_user.updated_at
FROM public.users AS forum_user
WHERE forum_user.id = sqlc.arg(user_id)
FOR UPDATE OF forum_user;

-- name: SuspendUserAndAudit :one
WITH changed AS (
    UPDATE public.users AS forum_user
    SET suspended_at = GREATEST(sqlc.arg(suspended_at)::timestamptz, forum_user.created_at),
        suspended_until = NULL,
        suspension_reason = sqlc.arg(reason),
        updated_at = GREATEST(sqlc.arg(updated_at)::timestamptz, forum_user.updated_at)
    WHERE forum_user.id = sqlc.arg(user_id)
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
      )
    RETURNING forum_user.id, forum_user.suspended_at, forum_user.suspended_until,
              forum_user.suspension_reason, forum_user.updated_at
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind,
        actor_user_id,
        target_type,
        target_user_id,
        action_type,
        reason,
        previous_state,
        resulting_state,
        request_id,
        created_at
    )
    SELECT
        'forum_user',
        sqlc.arg(actor_user_id),
        'user',
        changed.id,
        'suspend_user',
        sqlc.arg(reason),
        jsonb_build_object(
            'suspended_at', sqlc.narg(previous_suspended_at)::timestamptz,
            'suspended_until', sqlc.narg(previous_suspended_until)::timestamptz,
            'suspension_reason', sqlc.narg(previous_suspension_reason)::text
        ),
        jsonb_build_object(
            'suspended_at', changed.suspended_at,
            'suspended_until', changed.suspended_until,
            'suspension_reason', changed.suspension_reason
        ),
        sqlc.arg(request_id),
        changed.updated_at
    FROM changed
    RETURNING id, target_user_id
)
SELECT changed.id AS user_id, changed.suspended_at, changed.suspended_until,
       changed.suspension_reason, changed.updated_at, audit.id AS audit_id
FROM changed
JOIN audit ON audit.target_user_id = changed.id;

-- name: GetModerationUserStatus :one
SELECT
    target.id,
    target.display_name,
    target.role,
    target.suspended_at,
    target.suspended_until,
    target.suspension_reason,
    target.muted_until,
    target.created_at,
    target.updated_at,
    target.last_login_at
FROM public.users AS target
WHERE target.id = sqlc.arg(target_user_id)
  AND target.id <> sqlc.arg(actor_user_id)
  AND (
      sqlc.arg(is_administrator)::boolean
      OR (sqlc.arg(is_moderator)::boolean AND target.role = 'member')
  );

-- name: ReinstateUserAndAudit :one
WITH changed AS (
    UPDATE public.users AS forum_user
    SET suspended_at = NULL,
        suspended_until = NULL,
        suspension_reason = NULL,
        updated_at = GREATEST(sqlc.arg(updated_at)::timestamptz, forum_user.updated_at)
    WHERE forum_user.id = sqlc.arg(user_id)
      AND forum_user.suspended_at <= sqlc.arg(observed_at)::timestamptz
      AND (
          forum_user.suspended_until IS NULL
          OR forum_user.suspended_until > sqlc.arg(observed_at)::timestamptz
      )
    RETURNING forum_user.id, forum_user.suspended_at, forum_user.suspended_until,
              forum_user.suspension_reason, forum_user.updated_at
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind,
        actor_user_id,
        target_type,
        target_user_id,
        action_type,
        reason,
        previous_state,
        resulting_state,
        request_id,
        created_at
    )
    SELECT
        'forum_user',
        sqlc.arg(actor_user_id),
        'user',
        changed.id,
        'reinstate_user',
        sqlc.arg(reason),
        jsonb_build_object(
            'suspended_at', sqlc.arg(previous_suspended_at)::timestamptz,
            'suspended_until', sqlc.narg(previous_suspended_until)::timestamptz,
            'suspension_reason', sqlc.arg(previous_suspension_reason)::text
        ),
        jsonb_build_object(
            'suspended_at', changed.suspended_at,
            'suspended_until', changed.suspended_until,
            'suspension_reason', changed.suspension_reason
        ),
        sqlc.arg(request_id),
        changed.updated_at
    FROM changed
    RETURNING id, target_user_id
)
SELECT changed.id AS user_id, changed.suspended_at, changed.suspended_until,
       changed.suspension_reason, changed.updated_at, audit.id AS audit_id
FROM changed
JOIN audit ON audit.target_user_id = changed.id;
