-- name: MarkTopicReadBoundary :one
WITH authorized_topic AS MATERIALIZED (
    SELECT
        topic.id AS topic_id,
        topic.next_post_number
    FROM public.topics AS topic
    JOIN public.areas AS area ON area.id = topic.area_id
    WHERE topic.id = sqlc.arg(topic_id)
      AND topic.deleted_at IS NULL
      AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
      AND (
        sqlc.arg(is_staff)::boolean
        OR area.visibility IN ('public', 'authenticated')
        OR (
            area.visibility = 'groups'
            AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
            AND EXISTS (
                SELECT 1
                FROM public.area_groups AS membership
                WHERE membership.area_id = area.id
                  AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])
            )
        )
      )
),
boundary AS MATERIALIZED (
    SELECT
        topic.topic_id,
        topic.next_post_number,
        head.selected_post_number
    FROM authorized_topic AS topic
    LEFT JOIN LATERAL (
        SELECT COALESCE(max(post.post_number), 0)::integer AS selected_post_number
        FROM public.posts AS post
        WHERE post.topic_id = topic.topic_id
          AND post.deleted_at IS NULL
          AND post.redacted_at IS NULL
          AND post.author_id <> sqlc.arg(actor_user_id)::bigint
    ) AS head ON true
),
upserted AS (
    INSERT INTO public.topic_reads (
        user_id,
        topic_id,
        last_read_post_number,
        read_at
    )
    SELECT
        sqlc.arg(actor_user_id)::bigint,
        boundary.topic_id,
        boundary.selected_post_number,
        statement_timestamp()
    FROM boundary
    WHERE boundary.selected_post_number > 0
    ON CONFLICT (user_id, topic_id) DO UPDATE
    SET last_read_post_number = GREATEST(
            public.topic_reads.last_read_post_number,
            EXCLUDED.last_read_post_number
        ),
        read_at = EXCLUDED.read_at
    WHERE EXCLUDED.last_read_post_number > public.topic_reads.last_read_post_number
    RETURNING user_id
)
SELECT
    boundary.topic_id,
    boundary.next_post_number,
    boundary.selected_post_number,
    EXISTS (SELECT 1 FROM upserted)::boolean AS advanced
FROM boundary;

-- name: GetTopicReadMarker :one
SELECT
    marker.last_read_post_number,
    marker.read_at
FROM public.topic_reads AS marker
WHERE marker.user_id = sqlc.arg(actor_user_id)
  AND marker.topic_id = sqlc.arg(topic_id);
