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
