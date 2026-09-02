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
