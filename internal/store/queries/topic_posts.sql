-- name: GetVisibleTopicPostPage :many
WITH visible_topic AS (
    SELECT
        area.id AS area_id,
        area.slug AS area_slug,
        area.name AS area_name,
        area.description AS area_description,
        topic.id AS topic_id,
        topic.title AS topic_title,
        topic.state AS topic_state,
        topic.pinned_at AS topic_pinned_at,
        topic.created_at AS topic_created_at,
        starter.display_name AS topic_author_display_name
    FROM public.topics AS topic
    JOIN public.areas AS area ON area.id = topic.area_id
    JOIN public.users AS starter ON starter.id = topic.author_id
    WHERE topic.id = sqlc.arg(topic_id)
      AND topic.deleted_at IS NULL
      AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
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
)
SELECT
    visible_topic.area_id,
    visible_topic.area_slug,
    visible_topic.area_name,
    visible_topic.area_description,
    visible_topic.topic_id,
    visible_topic.topic_title,
    visible_topic.topic_state,
    visible_topic.topic_pinned_at,
    visible_topic.topic_created_at,
    visible_topic.topic_author_display_name,
    post.id AS post_id,
    post.post_number,
    post.rendered_html,
    post.renderer_version,
    post.revision,
    post.created_at AS post_created_at,
    post.updated_at AS post_updated_at,
    post.edited_at AS post_edited_at,
    author.display_name AS post_author_display_name,
    count(post.id) OVER ()::bigint AS total_visible_posts
FROM visible_topic
LEFT JOIN public.posts AS post
    ON post.topic_id = visible_topic.topic_id
   AND post.deleted_at IS NULL
LEFT JOIN public.users AS author ON author.id = post.author_id
ORDER BY post.post_number ASC NULLS FIRST
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;
