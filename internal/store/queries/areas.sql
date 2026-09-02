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
