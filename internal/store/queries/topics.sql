-- name: ListVisibleTopicsByAreaSlug :many
SELECT
    topic.id AS topic_id,
    topic.title,
    topic.slug,
    topic.state,
    topic.pinned_at,
    topic.reply_count,
    author.display_name AS author_display_name,
    topic.last_activity_at,
    count(*) OVER ()::bigint AS total_visible_topics
FROM public.areas AS area
JOIN public.topics AS topic ON topic.area_id = area.id
JOIN public.users AS author ON author.id = topic.author_id
WHERE area.slug = sqlc.arg(area_slug)
  AND (
    sqlc.arg(is_staff)::boolean
    OR area.visibility = 'public'
    OR (
        sqlc.arg(is_member)::boolean
        AND area.visibility = 'authenticated'
    )
    OR (
        sqlc.arg(is_member)::boolean
        AND area.visibility = 'groups'
        AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
        AND EXISTS (
            SELECT 1
            FROM public.area_groups AS membership
            WHERE membership.area_id = area.id
              AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])
        )
    )
  )
  AND topic.deleted_at IS NULL
  AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
ORDER BY topic.pinned_at DESC NULLS LAST, topic.last_activity_at DESC, topic.id DESC
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: ListAuthenticatedVisibleTopicsByAreaSlug :many
WITH visible_area AS MATERIALIZED (
    SELECT area.id
    FROM public.areas AS area
    WHERE area.slug = sqlc.arg(area_slug)
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
visible_topics AS MATERIALIZED (
    SELECT
        topic.id AS topic_id,
        topic.title,
        topic.slug,
        topic.state,
        topic.pinned_at,
        topic.reply_count,
        topic.next_post_number,
        author.display_name AS author_display_name,
        topic.last_activity_at
    FROM visible_area AS area
    JOIN public.topics AS topic ON topic.area_id = area.id
    JOIN public.users AS author ON author.id = topic.author_id
    WHERE topic.deleted_at IS NULL
      AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
),
paged_topics AS MATERIALIZED (
    SELECT
        topic.*,
        count(*) OVER ()::bigint AS total_visible_topics
    FROM visible_topics AS topic
    ORDER BY topic.pinned_at DESC NULLS LAST, topic.last_activity_at DESC, topic.topic_id DESC
    LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer
)
SELECT
    topic.topic_id,
    topic.title,
    topic.slug,
    topic.state,
    topic.pinned_at,
    topic.reply_count,
    topic.author_display_name,
    topic.last_activity_at,
    topic.total_visible_topics,
    topic.next_post_number,
    head.read_head,
    marker.last_read_post_number,
    marker.read_at,
    CASE
        WHEN head.read_head = 0 THEN 'read'
        WHEN marker.user_id IS NULL THEN 'new'
        WHEN marker.last_read_post_number < head.read_head THEN 'unread'
        ELSE 'read'
    END::text AS read_state
FROM paged_topics AS topic
LEFT JOIN LATERAL (
    SELECT COALESCE(max(post.post_number), 0)::integer AS read_head
    FROM public.posts AS post
    WHERE post.topic_id = topic.topic_id
      AND post.deleted_at IS NULL
      AND post.redacted_at IS NULL
      AND post.author_id <> sqlc.arg(actor_user_id)::bigint
) AS head ON true
LEFT JOIN public.topic_reads AS marker
  ON marker.topic_id = topic.topic_id
 AND marker.user_id = sqlc.arg(actor_user_id)::bigint
ORDER BY topic.pinned_at DESC NULLS LAST, topic.last_activity_at DESC, topic.topic_id DESC;
