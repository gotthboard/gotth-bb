-- name: GetVisibleAreaBySlug :one
SELECT
    a.id,
    a.slug,
    a.name,
    a.description,
    a.display_order,
    a.visibility,
    a.posting_mode,
    a.created_by,
    a.updated_by,
    a.created_at,
    a.updated_at
FROM public.areas AS a
WHERE a.slug = sqlc.arg(slug)
  AND (
    sqlc.arg(is_staff)::boolean
    OR a.visibility = 'public'
    OR (
        sqlc.arg(is_member)::boolean
        AND a.visibility = 'authenticated'
    )
    OR (
        sqlc.arg(is_member)::boolean
        AND a.visibility = 'groups'
        AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
        AND EXISTS (
            SELECT 1
            FROM public.area_groups AS ag
            WHERE ag.area_id = a.id
              AND ag.group_id = ANY(sqlc.arg(group_ids)::bigint[])
        )
    )
  );

-- name: ListVisibleAreas :many
SELECT
    a.id,
    a.slug,
    a.name,
    a.description,
    a.display_order,
    a.visibility,
    a.posting_mode,
    a.created_by,
    a.updated_by,
    a.created_at,
    a.updated_at
FROM public.areas AS a
WHERE
    sqlc.arg(is_staff)::boolean
    OR a.visibility = 'public'
    OR (
        sqlc.arg(is_member)::boolean
        AND a.visibility = 'authenticated'
    )
    OR (
        sqlc.arg(is_member)::boolean
        AND a.visibility = 'groups'
        AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
        AND EXISTS (
            SELECT 1
            FROM public.area_groups AS ag
            WHERE ag.area_id = a.id
              AND ag.group_id = ANY(sqlc.arg(group_ids)::bigint[])
        )
    )
ORDER BY a.display_order, a.id;

-- name: ListVisibleAreaSummaries :many
WITH visible_areas AS (
    SELECT
        a.id,
        a.slug,
        a.name,
        a.description,
        a.display_order,
        a.visibility,
        a.posting_mode,
        a.created_by,
        a.updated_by,
        a.created_at,
        a.updated_at
    FROM public.areas AS a
    WHERE
        sqlc.arg(is_staff)::boolean
        OR a.visibility = 'public'
        OR (
            sqlc.arg(is_member)::boolean
            AND a.visibility = 'authenticated'
        )
        OR (
            sqlc.arg(is_member)::boolean
            AND a.visibility = 'groups'
            AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
            AND EXISTS (
                SELECT 1
                FROM public.area_groups AS ag
                WHERE ag.area_id = a.id
                  AND ag.group_id = ANY(sqlc.arg(group_ids)::bigint[])
            )
        )
),
visible_topics AS (
    SELECT topic.id, topic.area_id, topic.title
    FROM public.topics AS topic
    JOIN visible_areas AS area ON area.id = topic.area_id
    WHERE topic.deleted_at IS NULL
      AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
),
topic_counts AS (
    SELECT topic.area_id, count(*)::bigint AS topic_count
    FROM visible_topics AS topic
    GROUP BY topic.area_id
),
visible_posts AS (
    SELECT post.id, post.topic_id, post.post_number, post.thread_path, post.author_id, post.created_at
    FROM public.posts AS post
    JOIN visible_topics AS topic ON topic.id = post.topic_id
    WHERE post.deleted_at IS NULL
),
visible_thread_nodes AS (
    SELECT post.id, post.topic_id, post.thread_path
    FROM public.posts AS post
    JOIN visible_topics AS topic ON topic.id = post.topic_id
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
numbered_thread_nodes AS (
    SELECT
        node.id,
        row_number() OVER (PARTITION BY node.topic_id ORDER BY node.thread_path)::bigint AS node_ordinal
    FROM visible_thread_nodes AS node
),
post_counts AS (
    SELECT topic.area_id, count(*)::bigint AS post_count
    FROM visible_posts AS post
    JOIN visible_topics AS topic ON topic.id = post.topic_id
    GROUP BY topic.area_id
),
latest_posts AS (
    SELECT DISTINCT ON (topic.area_id)
        topic.area_id,
        topic.id AS latest_topic_id,
        topic.title AS latest_topic_title,
        post.id AS latest_post_id,
        post.post_number AS latest_post_number,
        numbered.node_ordinal AS latest_post_ordinal,
        author.display_name AS latest_post_author,
        post.created_at AS latest_post_created_at
    FROM visible_posts AS post
    JOIN visible_topics AS topic ON topic.id = post.topic_id
    JOIN numbered_thread_nodes AS numbered ON numbered.id = post.id
    JOIN public.users AS author ON author.id = post.author_id
    ORDER BY topic.area_id, post.created_at DESC, post.id DESC
)
SELECT
    area.id,
    area.slug,
    area.name,
    area.description,
    area.display_order,
    area.visibility,
    area.posting_mode,
    area.created_by,
    area.updated_by,
    area.created_at,
    area.updated_at,
    COALESCE(topic_counts.topic_count, 0)::bigint AS topic_count,
    COALESCE(post_counts.post_count, 0)::bigint AS post_count,
    latest_posts.latest_topic_id,
    latest_posts.latest_topic_title,
    latest_posts.latest_post_id,
    latest_posts.latest_post_number,
    latest_posts.latest_post_ordinal,
    latest_posts.latest_post_author,
    latest_posts.latest_post_created_at
FROM visible_areas AS area
LEFT JOIN topic_counts ON topic_counts.area_id = area.id
LEFT JOIN post_counts ON post_counts.area_id = area.id
LEFT JOIN latest_posts ON latest_posts.area_id = area.id
ORDER BY area.display_order, area.id;

-- name: ListAreasForAdministration :many
SELECT
    a.id,
    a.slug,
    a.name,
    a.description,
    a.display_order,
    a.visibility,
    a.posting_mode,
    a.created_by,
    a.updated_by,
    a.created_at,
    a.updated_at,
    COALESCE(
        ARRAY(
            SELECT ag.group_id
            FROM public.area_groups AS ag
            WHERE ag.area_id = a.id
            ORDER BY ag.group_id
        ),
        ARRAY[]::bigint[]
    )::bigint[] AS group_ids
FROM public.areas AS a
ORDER BY a.display_order, a.id;

-- name: ListForumGroupsForAreaAdministration :many
SELECT id, name
FROM public.forum_groups
ORDER BY lower(name), id;

-- name: CountExistingForumGroups :one
SELECT count(*)
FROM public.forum_groups
WHERE id = ANY(sqlc.arg(group_ids)::bigint[]);

-- name: CreateAreaForAdministration :one
INSERT INTO public.areas (
    slug, name, description, display_order, visibility, posting_mode,
    created_by, updated_by, created_at, updated_at
)
VALUES (
    sqlc.arg(slug), sqlc.arg(name), sqlc.arg(description),
    sqlc.arg(display_order), sqlc.arg(visibility), sqlc.arg(posting_mode),
    sqlc.arg(actor_user_id), sqlc.arg(actor_user_id),
    sqlc.arg(at_time), sqlc.arg(at_time)
)
RETURNING *;

-- name: LockAreaForAdministration :one
SELECT
    a.id,
    a.slug,
    a.name,
    a.description,
    a.display_order,
    a.visibility,
    a.posting_mode,
    a.created_by,
    a.updated_by,
    a.created_at,
    a.updated_at,
    COALESCE(
        ARRAY(
            SELECT ag.group_id
            FROM public.area_groups AS ag
            WHERE ag.area_id = a.id
            ORDER BY ag.group_id
        ),
        ARRAY[]::bigint[]
    )::bigint[] AS group_ids
FROM public.areas AS a
WHERE a.id = sqlc.arg(area_id)
FOR UPDATE OF a;

-- name: DeleteAreaGroupsForAdministration :exec
DELETE FROM public.area_groups
WHERE area_id = sqlc.arg(area_id);

-- name: AddAreaGroupForAdministration :exec
INSERT INTO public.area_groups (area_id, group_id, added_by, created_at)
VALUES (sqlc.arg(area_id), sqlc.arg(group_id), sqlc.arg(actor_user_id), sqlc.arg(at_time));

-- name: UpdateAreaForAdministration :one
UPDATE public.areas
SET name = sqlc.arg(name),
    description = sqlc.arg(description),
    display_order = sqlc.arg(display_order),
    visibility = sqlc.arg(visibility),
    posting_mode = sqlc.arg(posting_mode),
    updated_by = sqlc.arg(actor_user_id),
    updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, updated_at)
WHERE id = sqlc.arg(area_id)
RETURNING *;

-- name: CreateAreaAdministrationAudit :one
INSERT INTO public.moderation_actions (
    actor_kind,
    actor_user_id,
    target_type,
    target_area_id,
    action_type,
    reason,
    previous_state,
    resulting_state,
    request_id,
    created_at
)
VALUES (
    'forum_user',
    sqlc.arg(actor_user_id),
    'area',
    sqlc.arg(area_id),
    sqlc.arg(action_type),
    sqlc.arg(reason),
    sqlc.arg(previous_state)::jsonb,
    sqlc.arg(resulting_state)::jsonb,
    sqlc.arg(request_id),
    sqlc.arg(at_time)
)
RETURNING id;
