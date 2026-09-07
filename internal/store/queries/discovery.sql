-- name: ParseDiscoveryQuery :one
SELECT
    parsed.query::text AS parsed_query,
    numnode(parsed.query)::integer AS node_count,
    querytree(parsed.query)::text AS positive_query
FROM (
    SELECT websearch_to_tsquery('pg_catalog.simple'::regconfig, sqlc.arg(query_text)::text) AS query
) AS parsed;

-- name: SearchDiscoveryPage :many
WITH candidate AS MATERIALIZED (
    SELECT identity.*
    FROM (
        SELECT
            0::smallint AS kind_order,
            'topic'::text AS result_kind,
            topic.id AS result_id,
            topic.created_at,
            topic.search_vector
        FROM public.topics AS topic
        JOIN public.areas AS area ON area.id = topic.area_id
        WHERE topic.deleted_at IS NULL
          AND topic.search_projection_version = 'search-v1-pg17-simple-u15-p2'
          AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
          AND (
            sqlc.arg(is_staff)::boolean
            OR area.visibility = 'public'
            OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
            OR (
                sqlc.arg(is_member)::boolean
                AND area.visibility = 'groups'
                AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
                AND EXISTS (
                    SELECT 1 FROM public.area_groups AS membership
                    WHERE membership.area_id = area.id
                      AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])
                )
            )
          )
          AND (sqlc.arg(author_id)::bigint = 0 OR topic.author_id = sqlc.arg(author_id)::bigint)
          AND (sqlc.arg(area_slug)::text = '' OR area.slug = sqlc.arg(area_slug)::text)
          AND (NOT sqlc.arg(has_from)::boolean OR topic.created_at >= sqlc.arg(from_time)::timestamptz)
          AND (
            NOT sqlc.arg(has_to)::boolean
            OR (sqlc.arg(to_inclusive)::boolean AND topic.created_at <= sqlc.arg(to_time)::timestamptz)
            OR (NOT sqlc.arg(to_inclusive)::boolean AND topic.created_at < sqlc.arg(to_time)::timestamptz)
          )
          AND (NOT sqlc.arg(has_query)::boolean OR topic.search_vector @@ sqlc.arg(parsed_query)::text::tsquery)
        UNION ALL
        SELECT
            1::smallint AS kind_order,
            'post'::text AS result_kind,
            post.id AS result_id,
            post.created_at,
            post.search_vector
        FROM public.posts AS post
        JOIN public.topics AS topic ON topic.id = post.topic_id
        JOIN public.areas AS area ON area.id = topic.area_id
        WHERE post.deleted_at IS NULL
          AND post.redacted_at IS NULL
          AND post.search_projection_version = 'search-v1-pg17-simple-u15-p2'
          AND topic.deleted_at IS NULL
          AND topic.search_projection_version = 'search-v1-pg17-simple-u15-p2'
          AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
          AND (
            sqlc.arg(is_staff)::boolean
            OR area.visibility = 'public'
            OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
            OR (
                sqlc.arg(is_member)::boolean
                AND area.visibility = 'groups'
                AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
                AND EXISTS (
                    SELECT 1 FROM public.area_groups AS membership
                    WHERE membership.area_id = area.id
                      AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])
                )
            )
          )
          AND (sqlc.arg(author_id)::bigint = 0 OR post.author_id = sqlc.arg(author_id)::bigint)
          AND (sqlc.arg(area_slug)::text = '' OR area.slug = sqlc.arg(area_slug)::text)
          AND (NOT sqlc.arg(has_from)::boolean OR post.created_at >= sqlc.arg(from_time)::timestamptz)
          AND (
            NOT sqlc.arg(has_to)::boolean
            OR (sqlc.arg(to_inclusive)::boolean AND post.created_at <= sqlc.arg(to_time)::timestamptz)
            OR (NOT sqlc.arg(to_inclusive)::boolean AND post.created_at < sqlc.arg(to_time)::timestamptz)
          )
          AND (NOT sqlc.arg(has_query)::boolean OR post.search_vector @@ sqlc.arg(parsed_query)::text::tsquery)
          AND NOT (
            sqlc.arg(has_query)::boolean
            AND post.id = topic.first_post_id
            AND topic.search_vector @@ sqlc.arg(parsed_query)::text::tsquery
            AND (sqlc.arg(author_id)::bigint = 0 OR topic.author_id = sqlc.arg(author_id)::bigint)
          )
    ) AS identity
    ORDER BY identity.created_at DESC, identity.kind_order, identity.result_id DESC
    LIMIT 51
),
rankable AS (
    SELECT * FROM candidate
    ORDER BY created_at DESC, kind_order, result_id DESC
    LIMIT 50
),
ordered AS (
    SELECT
        rankable.*,
        CASE WHEN sqlc.arg(has_query)::boolean
            THEN ts_rank_cd(rankable.search_vector, sqlc.arg(parsed_query)::text::tsquery, 32)
            ELSE NULL::real
        END AS result_rank,
        row_number() OVER (
            ORDER BY
                CASE WHEN sqlc.arg(has_query)::boolean
                    THEN ts_rank_cd(rankable.search_vector, sqlc.arg(parsed_query)::text::tsquery, 32)
                    ELSE NULL::real
                END DESC NULLS LAST,
                rankable.created_at DESC,
                rankable.kind_order,
                rankable.result_id DESC
        )::integer AS result_ordinal
    FROM rankable
),
expected_page AS (
    SELECT * FROM ordered
    WHERE result_ordinal > sqlc.arg(page_offset)::integer
      AND result_ordinal <= sqlc.arg(page_offset)::integer + 25
)
SELECT
    expected_page.result_kind AS expected_kind,
    expected_page.result_id AS expected_id,
    expected_page.result_ordinal,
    COALESCE(expected_page.result_rank, 0::real)::real AS result_rank,
    (sqlc.arg(page_offset)::integer = 0 AND (SELECT count(*) FROM candidate) > 25)::boolean AS has_next_page,
    ((SELECT count(*) FROM candidate) > 50)::boolean AS beyond_window,
    selected.result_kind,
    selected.result_id,
    selected.area_id,
    selected.area_slug,
    selected.area_name,
    selected.topic_id,
    selected.topic_title,
    selected.post_id,
    selected.author_id,
    selected.author_name,
    selected.created_at,
    selected.rendered_html
FROM expected_page
LEFT JOIN LATERAL (
    SELECT
        'topic'::text AS result_kind,
        topic.id AS result_id,
        area.id AS area_id,
        area.slug AS area_slug,
        area.name AS area_name,
        topic.id AS topic_id,
        topic.title AS topic_title,
        NULL::bigint AS post_id,
        topic.author_id,
        author.display_name AS author_name,
        topic.created_at,
        NULL::text AS rendered_html
    FROM public.topics AS topic
    JOIN public.areas AS area ON area.id = topic.area_id
    JOIN public.users AS author ON author.id = topic.author_id
    WHERE expected_page.result_kind = 'topic'
      AND topic.id = expected_page.result_id
      AND topic.deleted_at IS NULL
      AND topic.search_projection_version = 'search-v1-pg17-simple-u15-p2'
      AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
      AND (
        sqlc.arg(is_staff)::boolean OR area.visibility = 'public'
        OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
        OR (sqlc.arg(is_member)::boolean AND area.visibility = 'groups'
            AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
            AND EXISTS (SELECT 1 FROM public.area_groups AS membership
                WHERE membership.area_id = area.id AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])))
      )
      AND (NOT sqlc.arg(has_query)::boolean OR topic.search_vector @@ sqlc.arg(parsed_query)::text::tsquery)
    UNION ALL
    SELECT
        'post'::text AS result_kind,
        post.id AS result_id,
        area.id AS area_id,
        area.slug AS area_slug,
        area.name AS area_name,
        topic.id AS topic_id,
        topic.title AS topic_title,
        post.id AS post_id,
        post.author_id,
        author.display_name AS author_name,
        post.created_at,
        post.rendered_html
    FROM public.posts AS post
    JOIN public.topics AS topic ON topic.id = post.topic_id
    JOIN public.areas AS area ON area.id = topic.area_id
    JOIN public.users AS author ON author.id = post.author_id
    WHERE expected_page.result_kind = 'post'
      AND post.id = expected_page.result_id
      AND post.deleted_at IS NULL AND post.redacted_at IS NULL
      AND post.search_projection_version = 'search-v1-pg17-simple-u15-p2'
      AND topic.deleted_at IS NULL
      AND topic.search_projection_version = 'search-v1-pg17-simple-u15-p2'
      AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
      AND (
        sqlc.arg(is_staff)::boolean OR area.visibility = 'public'
        OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
        OR (sqlc.arg(is_member)::boolean AND area.visibility = 'groups'
            AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
            AND EXISTS (SELECT 1 FROM public.area_groups AS membership
                WHERE membership.area_id = area.id AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])))
      )
      AND (NOT sqlc.arg(has_query)::boolean OR post.search_vector @@ sqlc.arg(parsed_query)::text::tsquery)
) AS selected ON true
ORDER BY expected_page.result_ordinal;

-- name: GetDiscoveryDatabaseTime :one
SELECT clock_timestamp()::timestamptz;

-- name: ListRecentActivityInitial :many
SELECT
    post.id AS post_id,
    post.created_at,
    post.author_id,
    author.display_name AS author_name,
    topic.id AS topic_id,
    topic.title AS topic_title,
    area.id AS area_id,
    area.slug AS area_slug,
    area.name AS area_name
FROM public.posts AS post
JOIN public.users AS author ON author.id = post.author_id
JOIN public.topics AS topic ON topic.id = post.topic_id
JOIN public.areas AS area ON area.id = topic.area_id
WHERE post.deleted_at IS NULL
  AND post.redacted_at IS NULL
  AND post.search_projection_version = 'search-v1-pg17-simple-u15-p2'
  AND topic.deleted_at IS NULL
  AND topic.search_projection_version = 'search-v1-pg17-simple-u15-p2'
  AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
  AND (
    sqlc.arg(is_staff)::boolean
    OR area.visibility = 'public'
    OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
    OR (
        sqlc.arg(is_member)::boolean AND area.visibility = 'groups'
        AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
        AND EXISTS (SELECT 1 FROM public.area_groups AS membership
            WHERE membership.area_id = area.id AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[]))
    )
  )
ORDER BY post.created_at DESC, post.id DESC
LIMIT 26;

-- name: ListRecentActivityAfter :many
SELECT
    post.id AS post_id,
    post.created_at,
    post.author_id,
    author.display_name AS author_name,
    topic.id AS topic_id,
    topic.title AS topic_title,
    area.id AS area_id,
    area.slug AS area_slug,
    area.name AS area_name
FROM public.posts AS post
JOIN public.users AS author ON author.id = post.author_id
JOIN public.topics AS topic ON topic.id = post.topic_id
JOIN public.areas AS area ON area.id = topic.area_id
WHERE post.deleted_at IS NULL
  AND post.redacted_at IS NULL
  AND post.search_projection_version = 'search-v1-pg17-simple-u15-p2'
  AND topic.deleted_at IS NULL
  AND topic.search_projection_version = 'search-v1-pg17-simple-u15-p2'
  AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
  AND (
    sqlc.arg(is_staff)::boolean
    OR area.visibility = 'public'
    OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
    OR (
        sqlc.arg(is_member)::boolean AND area.visibility = 'groups'
        AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
        AND EXISTS (SELECT 1 FROM public.area_groups AS membership
            WHERE membership.area_id = area.id AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[]))
    )
  )
  AND (post.created_at, post.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_post_id)::bigint)
ORDER BY post.created_at DESC, post.id DESC
LIMIT 26;

-- name: GetDirectPost :one
SELECT
    post.id AS post_id,
    post.created_at,
    post.updated_at,
    post.edited_at,
    post.revision,
    post.rendered_html,
    post.renderer_version,
    post.author_id,
    author.display_name AS author_name,
    topic.id AS topic_id,
    topic.title AS topic_title,
    area.id AS area_id,
    area.slug AS area_slug,
    area.name AS area_name
FROM public.posts AS post
JOIN public.users AS author ON author.id = post.author_id
JOIN public.topics AS topic ON topic.id = post.topic_id
JOIN public.areas AS area ON area.id = topic.area_id
WHERE post.id = sqlc.arg(post_id)::bigint
  AND post.deleted_at IS NULL
  AND post.redacted_at IS NULL
  AND topic.deleted_at IS NULL
  AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
  AND (
    sqlc.arg(is_staff)::boolean
    OR area.visibility = 'public'
    OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
    OR (
        sqlc.arg(is_member)::boolean AND area.visibility = 'groups'
        AND COALESCE(cardinality(sqlc.arg(group_ids)::bigint[]), 0) > 0
        AND EXISTS (SELECT 1 FROM public.area_groups AS membership
            WHERE membership.area_id = area.id AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[]))
    )
  );
