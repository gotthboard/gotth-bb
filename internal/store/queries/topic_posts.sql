-- name: GetVisibleTopicPostPage :many
WITH visible_topic AS (
    SELECT
        area.id AS area_id,
        area.slug AS area_slug,
        area.name AS area_name,
        area.description AS area_description,
        area.posting_mode AS area_posting_mode,
        topic.id AS topic_id,
        topic.first_post_id AS topic_first_post_id,
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
),
thread_nodes AS (
    SELECT
        post.id AS post_id,
        post.topic_id,
        post.post_number,
        post.parent_post_id,
        post.thread_path,
        (post.deleted_at IS NOT NULL)::boolean AS is_tombstone,
        CASE WHEN post.deleted_at IS NULL THEN post.rendered_html ELSE NULL::text END AS rendered_html,
        CASE WHEN post.deleted_at IS NULL THEN post.renderer_version ELSE NULL::text END AS renderer_version,
        CASE WHEN post.deleted_at IS NULL THEN post.revision ELSE NULL::integer END AS revision,
        CASE WHEN post.deleted_at IS NULL THEN post.created_at ELSE NULL::timestamptz END AS post_created_at,
        CASE WHEN post.deleted_at IS NULL THEN post.updated_at ELSE NULL::timestamptz END AS post_updated_at,
        CASE WHEN post.deleted_at IS NULL THEN post.edited_at ELSE NULL::timestamptz END AS post_edited_at,
        CASE WHEN post.deleted_at IS NULL THEN post.author_id ELSE NULL::bigint END AS post_author_id,
        CASE WHEN post.deleted_at IS NULL THEN author.display_name ELSE NULL::text END AS post_author_display_name
    FROM visible_topic
    JOIN public.posts AS post ON post.topic_id = visible_topic.topic_id
    LEFT JOIN public.users AS author ON author.id = post.author_id
    WHERE post.deleted_at IS NULL
       OR EXISTS (
            SELECT 1
            FROM public.posts AS descendant
            WHERE descendant.topic_id = post.topic_id
              AND descendant.deleted_at IS NULL
              AND descendant.id <> post.id
              AND descendant.thread_path[1:cardinality(post.thread_path)] = post.thread_path
       )
),
numbered_nodes AS (
    SELECT
        thread_nodes.*,
        row_number() OVER (ORDER BY thread_path)::bigint AS node_ordinal,
        count(*) OVER ()::bigint AS total_visible_posts
    FROM thread_nodes
),
page_nodes AS (
    SELECT *
    FROM numbered_nodes
    WHERE node_ordinal > sqlc.arg(page_offset)::integer
      AND node_ordinal <= sqlc.arg(page_offset)::integer + sqlc.arg(page_limit)::integer
)
SELECT
    visible_topic.area_id,
    visible_topic.area_slug,
    visible_topic.area_name,
    visible_topic.area_description,
    visible_topic.area_posting_mode,
    visible_topic.topic_id,
    visible_topic.topic_first_post_id,
    visible_topic.topic_title,
    visible_topic.topic_state,
    visible_topic.topic_pinned_at,
    visible_topic.topic_created_at,
    visible_topic.topic_author_display_name,
    page_nodes.post_id,
    page_nodes.post_number,
    page_nodes.parent_post_id,
    CASE WHEN page_nodes.post_id IS NOT NULL THEN cardinality(page_nodes.thread_path) ELSE NULL::integer END AS thread_depth,
    page_nodes.is_tombstone,
    page_nodes.rendered_html,
    page_nodes.renderer_version,
    page_nodes.revision,
    page_nodes.post_created_at,
    page_nodes.post_updated_at,
    page_nodes.post_edited_at,
    page_nodes.post_author_id,
    page_nodes.post_author_display_name,
    parent.post_number AS parent_post_number,
    parent.post_author_display_name AS parent_author_display_name,
    parent.node_ordinal AS parent_node_ordinal,
    page_nodes.node_ordinal,
    COALESCE(page_nodes.total_visible_posts, 0)::bigint AS total_visible_posts
FROM visible_topic
LEFT JOIN page_nodes ON page_nodes.topic_id = visible_topic.topic_id
LEFT JOIN numbered_nodes AS parent ON parent.post_id = page_nodes.parent_post_id
WHERE sqlc.arg(page_offset)::integer = 0 OR page_nodes.post_id IS NOT NULL
ORDER BY page_nodes.node_ordinal ASC NULLS FIRST;
