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
