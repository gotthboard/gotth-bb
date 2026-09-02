-- name: LockPostForEdit :one
SELECT
    post.id AS post_id,
    post.author_id,
    post.revision,
    post.topic_id,
    post.post_number,
    topic.state AS topic_state,
    area.id AS area_id,
    area.visibility,
    area.posting_mode
FROM public.posts AS post
JOIN public.topics AS topic ON topic.id = post.topic_id
JOIN public.areas AS area ON area.id = topic.area_id
WHERE post.id = sqlc.arg(post_id)
  AND post.deleted_at IS NULL
  AND topic.deleted_at IS NULL
FOR UPDATE OF post
FOR SHARE OF topic, area;

-- name: UpdatePostRevision :one
UPDATE public.posts AS post
SET markdown_source = sqlc.arg(markdown_source),
    rendered_html = sqlc.arg(rendered_html),
    renderer_version = sqlc.arg(renderer_version),
    revision = post.revision + 1,
    updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, post.updated_at, COALESCE(post.edited_at, '-infinity'::timestamptz)),
    edited_at = GREATEST(sqlc.arg(at_time)::timestamptz, post.updated_at, COALESCE(post.edited_at, '-infinity'::timestamptz))
WHERE post.id = sqlc.arg(post_id)
  AND post.revision = sqlc.arg(expected_revision)
  AND post.deleted_at IS NULL
RETURNING post.id AS post_id, post.topic_id, post.post_number, post.revision;

-- name: GetEditablePost :one
SELECT
    post.id AS post_id,
    post.topic_id,
    post.post_number,
    post.markdown_source,
    post.revision
FROM public.posts AS post
JOIN public.topics AS topic ON topic.id = post.topic_id
JOIN public.areas AS area ON area.id = topic.area_id
WHERE post.id = sqlc.arg(post_id)
  AND post.author_id = sqlc.arg(author_id)
  AND post.deleted_at IS NULL
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
  );

-- name: SoftDeletePost :one
UPDATE public.posts AS post
SET deleted_at = GREATEST(
        sqlc.arg(at_time)::timestamptz,
        post.created_at,
        post.updated_at,
        COALESCE(post.edited_at, '-infinity'::timestamptz)
    ),
    deleted_by = sqlc.arg(author_id)::bigint,
    deletion_reason = 'Deleted by author'
WHERE post.id = sqlc.arg(post_id)
  AND post.author_id = sqlc.arg(author_id)::bigint
  AND post.revision = sqlc.arg(expected_revision)
  AND post.deleted_at IS NULL
RETURNING post.id AS post_id, post.topic_id, post.post_number, post.revision;
