-- name: LoadAdministrationDashboard :one
WITH observed AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS at_time
), actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    CROSS JOIN observed
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > observed.at_time
          OR forum_user.suspended_until <= observed.at_time
      )
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= observed.at_time)
)
SELECT EXISTS (SELECT 1 FROM actor)::boolean AS actor_present,
       observed.at_time,
       (SELECT count(*) FROM actor CROSS JOIN public.users)::bigint AS users_total,
       (SELECT count(*) FROM actor CROSS JOIN public.users AS u WHERE u.role = 'member')::bigint AS members,
       (SELECT count(*) FROM actor CROSS JOIN public.users AS u WHERE u.role = 'moderator')::bigint AS moderators,
       (SELECT count(*) FROM actor CROSS JOIN public.users AS u WHERE u.role = 'administrator')::bigint AS administrators,
       (SELECT count(*) FROM actor CROSS JOIN public.users AS u
          WHERE u.suspended_at IS NOT NULL AND u.suspended_at <= observed.at_time
            AND (u.suspended_until IS NULL OR u.suspended_until > observed.at_time))::bigint AS suspended_users,
       (SELECT count(*) FROM actor CROSS JOIN public.topics AS t WHERE t.deleted_at IS NULL)::bigint AS topics,
       (SELECT count(*) FROM actor CROSS JOIN public.posts AS p WHERE p.deleted_at IS NULL AND p.redacted_at IS NULL)::bigint AS posts,
       (SELECT count(*) FROM actor CROSS JOIN public.reports AS r WHERE r.status = 'open')::bigint AS open_reports,
       (SELECT count(*) FROM actor CROSS JOIN public.reports AS r WHERE r.status = 'in_review')::bigint AS in_review_reports
FROM observed;

-- name: ListAreasForAdministrationPage :many
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (forum_user.suspended_at IS NULL OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz)
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), candidate AS MATERIALIZED (
    SELECT area.id, area.slug, area.name, area.description, area.display_order,
           area.visibility, area.posting_mode, area.administration_revision,
           (SELECT count(*) FROM public.area_groups AS mapping WHERE mapping.area_id = area.id)::bigint AS group_count
    FROM actor
    JOIN LATERAL (
        SELECT a.id, a.slug, a.name, a.description, a.display_order,
               a.visibility, a.posting_mode, a.administration_revision
        FROM public.areas AS a
        WHERE (a.display_order, a.id) > (sqlc.arg(after_order)::integer, sqlc.arg(after_area_id)::bigint)
        ORDER BY a.display_order, a.id
        LIMIT sqlc.arg(page_limit)
    ) AS area ON true
)
SELECT (candidate.id IS NOT NULL)::boolean AS area_present,
       COALESCE(candidate.id, 0)::bigint AS id,
       COALESCE(candidate.slug, '')::text AS slug,
       COALESCE(candidate.name, '')::text AS name,
       COALESCE(candidate.description, '')::text AS description,
       COALESCE(candidate.display_order, 0)::integer AS display_order,
       COALESCE(candidate.visibility, '')::text AS visibility,
       COALESCE(candidate.posting_mode, '')::text AS posting_mode,
       COALESCE(candidate.administration_revision, 0)::bigint AS administration_revision,
       COALESCE(candidate.group_count, 0)::bigint AS group_count
FROM actor
LEFT JOIN candidate ON true
ORDER BY candidate.display_order NULLS LAST, candidate.id NULLS LAST;

-- name: LoadAreaForAdministrationPage :one
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (forum_user.suspended_at IS NULL OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz)
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), target AS MATERIALIZED (
    SELECT area.id, area.slug, area.name, area.description, area.display_order,
           area.visibility, area.posting_mode, area.administration_revision,
           (SELECT count(*) FROM public.area_groups AS mapping WHERE mapping.area_id = area.id)::bigint AS group_count
    FROM actor
    JOIN public.areas AS area ON area.id = sqlc.arg(area_id)
)
SELECT EXISTS (SELECT 1 FROM actor)::boolean AS actor_present,
       (target.id IS NOT NULL)::boolean AS area_present,
       COALESCE(target.id, 0)::bigint AS id,
       COALESCE(target.slug, '')::text AS slug,
       COALESCE(target.name, '')::text AS name,
       COALESCE(target.description, '')::text AS description,
       COALESCE(target.display_order, 0)::integer AS display_order,
       COALESCE(target.visibility, '')::text AS visibility,
       COALESCE(target.posting_mode, '')::text AS posting_mode,
       COALESCE(target.administration_revision, 0)::bigint AS administration_revision,
       COALESCE(target.group_count, 0)::bigint AS group_count
FROM (SELECT 1) AS seed
LEFT JOIN target ON true;

-- name: LockAdministrationAreaCore :one
SELECT id, slug, name, description, display_order, visibility, posting_mode,
       administration_revision
FROM public.areas
WHERE id = sqlc.arg(area_id)
FOR UPDATE;

-- name: ListAreaGroupsForAdministrationPage :many
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (forum_user.suspended_at IS NULL OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz)
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), target AS MATERIALIZED (
    SELECT area.id FROM actor JOIN public.areas AS area ON area.id = sqlc.arg(area_id)
), candidate AS MATERIALIZED (
    SELECT forum_group.id, forum_group.name,
           EXISTS (SELECT 1 FROM public.area_groups AS mapping WHERE mapping.area_id = sqlc.arg(area_id) AND mapping.group_id = forum_group.id)::boolean AS assigned
    FROM target
    JOIN LATERAL (
        SELECT g.id, g.name FROM public.forum_groups AS g
        WHERE g.id > sqlc.arg(after_group_id)
        ORDER BY g.id LIMIT sqlc.arg(page_limit)
    ) AS forum_group ON true
)
SELECT EXISTS (SELECT 1 FROM actor)::boolean AS actor_present,
       EXISTS (SELECT 1 FROM target)::boolean AS area_present,
       (candidate.id IS NOT NULL)::boolean AS group_present,
       COALESCE(candidate.id, 0)::bigint AS group_id,
       COALESCE(candidate.name, '')::text AS group_name,
       COALESCE(candidate.assigned, false)::boolean AS assigned
FROM (SELECT 1) AS seed
LEFT JOIN candidate ON true
ORDER BY candidate.id NULLS LAST;

-- name: UpdateAdministrationAreaAndAudit :one
WITH changed AS (
    UPDATE public.areas AS area
    SET name = sqlc.arg(name), description = sqlc.arg(description),
        display_order = sqlc.arg(display_order), visibility = sqlc.arg(visibility),
        posting_mode = sqlc.arg(posting_mode), updated_by = sqlc.arg(actor_user_id),
        updated_at = GREATEST(area.updated_at, sqlc.arg(observed_at)::timestamptz),
        administration_revision = area.administration_revision + 1
    WHERE area.id = sqlc.arg(area_id)
      AND area.administration_revision = sqlc.arg(expected_revision)
      AND area.administration_revision < 9223372036854775807
    RETURNING area.id, area.slug, area.administration_revision, area.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_area_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'area', changed.id, 'update_area',
           sqlc.arg(reason), sqlc.arg(previous_state)::jsonb, sqlc.arg(resulting_state)::jsonb,
           sqlc.arg(request_id), changed.updated_at
    FROM changed RETURNING id, target_area_id
)
SELECT changed.id AS area_id, changed.slug, changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_area_id = changed.id;

-- name: GrantAdministrationAreaGroupAndAudit :one
WITH mapping AS (
    INSERT INTO public.area_groups (area_id, group_id, added_by, created_at)
    VALUES (sqlc.arg(area_id), sqlc.arg(group_id), sqlc.arg(actor_user_id), sqlc.arg(observed_at)::timestamptz)
    ON CONFLICT (area_id, group_id) DO NOTHING
    RETURNING area_id, group_id
), changed AS (
    UPDATE public.areas AS area
    SET administration_revision = area.administration_revision + 1,
        updated_at = GREATEST(area.updated_at, sqlc.arg(observed_at)::timestamptz), updated_by = sqlc.arg(actor_user_id)
    FROM mapping
    WHERE area.id = mapping.area_id AND area.administration_revision = sqlc.arg(expected_revision)
      AND area.administration_revision < 9223372036854775807
    RETURNING area.id, area.administration_revision, area.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (actor_kind, actor_user_id, target_type, target_area_id, action_type, reason, previous_state, resulting_state, request_id, created_at)
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'area', changed.id, 'grant_area_group', sqlc.arg(reason),
           jsonb_build_object('group_id', mapping.group_id, 'assigned', false, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           jsonb_build_object('group_id', mapping.group_id, 'assigned', true, 'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), changed.updated_at FROM changed JOIN mapping ON mapping.area_id = changed.id
    RETURNING id, target_area_id
)
SELECT changed.id AS area_id, changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_area_id = changed.id;

-- name: AdministrationAreaGroupExists :one
SELECT EXISTS (
    SELECT 1 FROM public.area_groups
    WHERE area_id = sqlc.arg(area_id) AND group_id = sqlc.arg(group_id)
)::boolean;

-- name: RevokeAdministrationAreaGroupAndAudit :one
WITH mapping AS (
    DELETE FROM public.area_groups AS area_group
    WHERE area_group.area_id = sqlc.arg(area_id) AND area_group.group_id = sqlc.arg(group_id)
      AND (SELECT count(*) FROM public.area_groups AS remaining WHERE remaining.area_id = sqlc.arg(area_id)) > 1
    RETURNING area_group.area_id, area_group.group_id
), changed AS (
    UPDATE public.areas AS area
    SET administration_revision = area.administration_revision + 1,
        updated_at = GREATEST(area.updated_at, sqlc.arg(observed_at)::timestamptz), updated_by = sqlc.arg(actor_user_id)
    FROM mapping
    WHERE area.id = mapping.area_id AND area.administration_revision = sqlc.arg(expected_revision)
      AND area.administration_revision < 9223372036854775807
    RETURNING area.id, area.administration_revision, area.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (actor_kind, actor_user_id, target_type, target_area_id, action_type, reason, previous_state, resulting_state, request_id, created_at)
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'area', changed.id, 'revoke_area_group', sqlc.arg(reason),
           jsonb_build_object('group_id', mapping.group_id, 'assigned', true, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           jsonb_build_object('group_id', mapping.group_id, 'assigned', false, 'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), changed.updated_at FROM changed JOIN mapping ON mapping.area_id = changed.id
    RETURNING id, target_area_id
)
SELECT changed.id AS area_id, changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_area_id = changed.id;
