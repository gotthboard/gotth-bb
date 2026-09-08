-- name: ConfigureMarkTopicReadTransaction :exec
SELECT
    set_config('statement_timeout', '2000ms', true),
    set_config('lock_timeout', '250ms', true);

-- name: ConfigureUnreadReadTransaction :exec
SELECT
    set_config('statement_timeout', '5000ms', true),
    set_config('lock_timeout', '250ms', true),
    set_config('work_mem', '4MB', true),
    set_config('max_parallel_workers_per_gather', '0', true),
    set_config('jit', 'off', true);

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

-- name: GetAuthorizedTopicReadState :one
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
)
SELECT
    topic.topic_id,
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
FROM authorized_topic AS topic
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
 AND marker.user_id = sqlc.arg(actor_user_id)::bigint;

-- name: GetFirstUnreadTarget :one
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
topic_state AS MATERIALIZED (
    SELECT
        topic.topic_id,
        topic.next_post_number,
        marker.last_read_post_number,
        marker.read_at,
        head.read_head
    FROM authorized_topic AS topic
    LEFT JOIN public.topic_reads AS marker
      ON marker.topic_id = topic.topic_id
     AND marker.user_id = sqlc.arg(actor_user_id)::bigint
    LEFT JOIN LATERAL (
        SELECT COALESCE(max(post.post_number), 0)::integer AS read_head
        FROM public.posts AS post
        WHERE post.topic_id = topic.topic_id
          AND post.deleted_at IS NULL
          AND post.redacted_at IS NULL
          AND post.author_id <> sqlc.arg(actor_user_id)::bigint
    ) AS head ON true
),
target AS MATERIALIZED (
    SELECT
        post.id AS post_id,
        post.post_number
    FROM topic_state AS state
    JOIN LATERAL (
        SELECT candidate.id, candidate.post_number
        FROM public.posts AS candidate
        WHERE candidate.topic_id = state.topic_id
          AND candidate.deleted_at IS NULL
          AND candidate.redacted_at IS NULL
          AND candidate.author_id <> sqlc.arg(actor_user_id)::bigint
          AND candidate.post_number > COALESCE(state.last_read_post_number, 0)
        ORDER BY candidate.post_number
        LIMIT 1
    ) AS post ON true
),
bounded_thread AS MATERIALIZED (
    SELECT
        post.id AS post_id,
        post.thread_path
    FROM target
    JOIN authorized_topic AS topic ON true
    JOIN public.posts AS post ON post.topic_id = topic.topic_id
    WHERE sqlc.arg(is_staff)::boolean
       OR post.deleted_at IS NULL
       OR EXISTS (
            SELECT 1
            FROM public.posts AS descendant
            WHERE descendant.topic_id = post.topic_id
              AND descendant.deleted_at IS NULL
              AND descendant.id <> post.id
              AND descendant.thread_path[1:cardinality(post.thread_path)] = post.thread_path
       )
    ORDER BY post.thread_path
    LIMIT 250001
),
numbered_thread AS MATERIALIZED (
    SELECT
        node.post_id,
        row_number() OVER (ORDER BY node.thread_path)::bigint AS node_ordinal
    FROM bounded_thread AS node
)
SELECT
    state.topic_id,
    state.next_post_number,
    state.last_read_post_number,
    state.read_at,
    state.read_head,
    target.post_id AS target_post_id,
    target.post_number AS target_post_number,
    node.node_ordinal AS target_node_ordinal
FROM topic_state AS state
LEFT JOIN target ON true
LEFT JOIN numbered_thread AS node ON node.post_id = target.post_id;
