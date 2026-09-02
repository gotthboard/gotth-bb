-- name: LockAreaForTopicCreation :one
SELECT
    area.id,
    area.visibility,
    area.posting_mode
FROM public.areas AS area
WHERE area.slug = sqlc.arg(area_slug)
FOR SHARE OF area;

-- name: LockAreaGroupIDs :many
SELECT mapping.group_id
FROM public.area_groups AS mapping
WHERE mapping.area_id = sqlc.arg(area_id)
ORDER BY mapping.group_id
FOR SHARE OF mapping;

-- name: CreateTopicAndFirstPost :one
WITH identifiers AS (
    SELECT
        nextval(pg_get_serial_sequence('public.topics', 'id'))::bigint AS topic_id,
        nextval(pg_get_serial_sequence('public.posts', 'id'))::bigint AS post_id
),
inserted_topic AS (
    INSERT INTO public.topics (
        id, area_id, author_id, title, state, first_post_id, latest_post_id,
        reply_count, next_post_number, created_at, updated_at, last_activity_at
    )
    SELECT
        identifiers.topic_id,
        sqlc.arg(area_id),
        sqlc.arg(author_id),
        sqlc.arg(title),
        'open',
        identifiers.post_id,
        identifiers.post_id,
        0,
        2,
        sqlc.arg(at_time),
        sqlc.arg(at_time),
        sqlc.arg(at_time)
    FROM identifiers
    RETURNING id
),
inserted_post AS (
    INSERT INTO public.posts (
        id, topic_id, author_id, post_number, markdown_source, rendered_html,
        renderer_version, revision, created_at, updated_at
    )
    SELECT
        identifiers.post_id,
        inserted_topic.id,
        sqlc.arg(author_id),
        1,
        sqlc.arg(markdown_source),
        sqlc.arg(rendered_html),
        sqlc.arg(renderer_version),
        1,
        sqlc.arg(at_time),
        sqlc.arg(at_time)
    FROM identifiers
    JOIN inserted_topic ON inserted_topic.id = identifiers.topic_id
    RETURNING id, topic_id, post_number
)
SELECT topic_id, id AS post_id, post_number
FROM inserted_post;

-- name: LockTopicForReply :one
SELECT
    topic.id AS topic_id,
    topic.state AS topic_state,
    area.id AS area_id,
    area.visibility,
    area.posting_mode
FROM public.topics AS topic
JOIN public.areas AS area ON area.id = topic.area_id
WHERE topic.id = sqlc.arg(topic_id)
  AND topic.deleted_at IS NULL
FOR UPDATE OF topic
FOR SHARE OF area;

-- name: CreateReplyAndAdvanceTopic :one
WITH inserted_post AS (
    INSERT INTO public.posts (
        topic_id, author_id, post_number, markdown_source, rendered_html,
        renderer_version, revision, created_at, updated_at
    )
    SELECT
        topic.id,
        sqlc.arg(author_id),
        topic.next_post_number,
        sqlc.arg(markdown_source),
        sqlc.arg(rendered_html),
        sqlc.arg(renderer_version),
        1,
        GREATEST(sqlc.arg(at_time)::timestamptz, topic.last_activity_at),
        GREATEST(sqlc.arg(at_time)::timestamptz, topic.last_activity_at)
    FROM public.topics AS topic
    WHERE topic.id = sqlc.arg(topic_id)
    RETURNING id, topic_id, post_number, created_at AS post_created_at
),
advanced_topic AS (
    UPDATE public.topics AS topic
    SET latest_post_id = inserted_post.id,
        reply_count = inserted_post.post_number - 1,
        next_post_number = inserted_post.post_number + 1,
        updated_at = inserted_post.post_created_at,
        last_activity_at = inserted_post.post_created_at
    FROM inserted_post
    WHERE topic.id = inserted_post.topic_id
    RETURNING topic.id AS topic_id, inserted_post.id AS post_id, inserted_post.post_number
)
SELECT topic_id, post_id, post_number
FROM advanced_topic;
