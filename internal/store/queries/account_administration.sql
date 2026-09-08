-- name: ListAccountsForAdministration :many
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
      )
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), account AS MATERIALIZED (
    SELECT target.id, target.display_name, target.role,
           target.suspended_at, target.suspended_until,
           target.created_at, target.updated_at,
           target.administration_revision
    FROM actor
    JOIN LATERAL (
        SELECT forum_user.id, forum_user.display_name, forum_user.role,
               forum_user.suspended_at, forum_user.suspended_until,
               forum_user.created_at, forum_user.updated_at,
               forum_user.administration_revision
        FROM public.users AS forum_user
        WHERE forum_user.id > sqlc.arg(after_user_id)
        ORDER BY forum_user.id
        LIMIT sqlc.arg(page_limit)
    ) AS target ON true
)
SELECT (account.id IS NOT NULL)::boolean AS account_present,
       COALESCE(account.id, 0)::bigint AS id,
       COALESCE(account.display_name, '')::text AS display_name,
       COALESCE(account.role, '')::text AS role,
       COALESCE(account.suspended_at <= sqlc.arg(observed_at)::timestamptz
                AND (account.suspended_until IS NULL OR account.suspended_until > sqlc.arg(observed_at)::timestamptz), false)::boolean AS suspended,
       account.created_at, account.updated_at,
       COALESCE(account.administration_revision, 0)::bigint AS administration_revision
FROM actor
LEFT JOIN account ON true
ORDER BY account.id NULLS LAST;

-- name: LoadAccountForAdministration :one
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
      )
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), target AS MATERIALIZED (
    SELECT forum_user.id, forum_user.display_name, forum_user.role,
           forum_user.suspended_at, forum_user.suspended_until,
           forum_user.created_at, forum_user.updated_at,
           forum_user.administration_revision
    FROM actor
    JOIN public.users AS forum_user ON forum_user.id = sqlc.arg(target_user_id)
)
SELECT (target.id IS NOT NULL)::boolean AS account_present,
       COALESCE(target.id, 0)::bigint AS id,
       COALESCE(target.display_name, '')::text AS display_name,
       COALESCE(target.role, '')::text AS role,
       COALESCE(target.suspended_at <= sqlc.arg(observed_at)::timestamptz
                AND (target.suspended_until IS NULL OR target.suspended_until > sqlc.arg(observed_at)::timestamptz), false)::boolean AS suspended,
       target.created_at, target.updated_at,
       COALESCE(target.administration_revision, 0)::bigint AS administration_revision
FROM actor
LEFT JOIN target ON true;

-- name: ListAccountGroupsForAdministration :many
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
      )
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), target AS MATERIALIZED (
    SELECT forum_user.id
    FROM actor
    JOIN public.users AS forum_user ON forum_user.id = sqlc.arg(target_user_id)
), candidate AS MATERIALIZED (
    SELECT forum_group.id, forum_group.name,
           (membership.user_id IS NOT NULL)::boolean AS member
    FROM target
    JOIN LATERAL (
        SELECT group_row.id, group_row.name
        FROM public.forum_groups AS group_row
        WHERE group_row.id > sqlc.arg(after_group_id)
        ORDER BY group_row.id
        LIMIT sqlc.arg(page_limit)
    ) AS forum_group ON true
    LEFT JOIN public.forum_group_members AS membership
      ON membership.group_id = forum_group.id
     AND membership.user_id = target.id
)
SELECT (target.id IS NOT NULL)::boolean AS account_present,
       (candidate.id IS NOT NULL)::boolean AS group_present,
       COALESCE(candidate.id, 0)::bigint AS group_id,
       COALESCE(candidate.name, '')::text AS group_name,
       COALESCE(candidate.member, false)::boolean AS member
FROM actor
LEFT JOIN target ON true
LEFT JOIN candidate ON true
ORDER BY candidate.id NULLS LAST;

-- name: ListGroupsForAdministration :many
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
      )
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), candidate AS MATERIALIZED (
    SELECT forum_group.id, forum_group.name, forum_group.created_at,
           forum_group.updated_at, forum_group.administration_revision
    FROM actor
    JOIN LATERAL (
        SELECT group_row.id, group_row.name, group_row.created_at,
               group_row.updated_at, group_row.administration_revision
        FROM public.forum_groups AS group_row
        WHERE group_row.id > sqlc.arg(after_group_id)
        ORDER BY group_row.id
        LIMIT sqlc.arg(page_limit)
    ) AS forum_group ON true
)
SELECT (candidate.id IS NOT NULL)::boolean AS group_present,
       COALESCE(candidate.id, 0)::bigint AS id,
       COALESCE(candidate.name, '')::text AS name,
       candidate.created_at, candidate.updated_at,
       COALESCE(candidate.administration_revision, 0)::bigint AS administration_revision
FROM actor
LEFT JOIN candidate ON true
ORDER BY candidate.id NULLS LAST;

-- name: LockAdministrationUser :one
SELECT forum_user.id, forum_user.display_name, forum_user.role,
       forum_user.suspended_at, forum_user.suspended_until,
       forum_user.muted_until, forum_user.created_at, forum_user.updated_at,
       forum_user.administration_revision
FROM public.users AS forum_user
WHERE forum_user.id = sqlc.arg(user_id)
FOR UPDATE OF forum_user;

-- name: LockAdministrationGroup :one
SELECT forum_group.id, forum_group.name, forum_group.created_by,
       forum_group.created_at, forum_group.updated_at,
       forum_group.administration_revision
FROM public.forum_groups AS forum_group
WHERE forum_group.id = sqlc.arg(group_id)
FOR UPDATE OF forum_group;

-- name: AdministrationMembershipExists :one
SELECT EXISTS (
    SELECT 1
    FROM public.forum_group_members AS membership
    WHERE membership.user_id = sqlc.arg(user_id)
      AND membership.group_id = sqlc.arg(group_id)
)::boolean;

-- name: CreateAdministrationGroupAndAudit :one
WITH created AS (
    INSERT INTO public.forum_groups (name, created_by, created_at, updated_at)
    VALUES (sqlc.arg(name), sqlc.arg(actor_user_id), sqlc.arg(observed_at)::timestamptz, sqlc.arg(observed_at)::timestamptz)
    RETURNING id, name, administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_group_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'group', created.id,
           'create_group', sqlc.arg(reason), '{}'::jsonb,
           jsonb_build_object('name', created.name, 'administration_revision', created.administration_revision),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM created
    RETURNING id, target_group_id
)
SELECT created.id AS group_id, created.name,
       created.administration_revision, audit.id AS audit_id
FROM created
JOIN audit ON audit.target_group_id = created.id;

-- name: RenameAdministrationGroupAndAudit :one
WITH changed AS (
    UPDATE public.forum_groups AS forum_group
    SET name = sqlc.arg(name),
        updated_at = GREATEST(forum_group.updated_at, sqlc.arg(observed_at)::timestamptz),
        administration_revision = forum_group.administration_revision + 1
    WHERE forum_group.id = sqlc.arg(group_id)
      AND forum_group.administration_revision = sqlc.arg(expected_revision)
      AND forum_group.administration_revision < 9223372036854775807
    RETURNING forum_group.id, forum_group.name, forum_group.updated_at,
              forum_group.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_group_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'group', changed.id,
           'rename_group', sqlc.arg(reason),
           jsonb_build_object('name', sqlc.arg(previous_name)::text,
                              'administration_revision', sqlc.arg(expected_revision)::bigint),
           jsonb_build_object('name', changed.name,
                              'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), changed.updated_at
    FROM changed
    RETURNING id, target_group_id
)
SELECT changed.id AS group_id, changed.name,
       changed.administration_revision, audit.id AS audit_id
FROM changed
JOIN audit ON audit.target_group_id = changed.id;

-- name: GrantAdministrationMembershipAndAudit :one
WITH mapping AS (
    INSERT INTO public.forum_group_members (group_id, user_id, granted_by, created_at)
    VALUES (sqlc.arg(group_id), sqlc.arg(target_user_id), sqlc.arg(actor_user_id), sqlc.arg(observed_at)::timestamptz)
    ON CONFLICT (group_id, user_id) DO NOTHING
    RETURNING group_id, user_id
), changed AS (
    UPDATE public.users AS forum_user
    SET administration_revision = forum_user.administration_revision + 1,
        updated_at = GREATEST(forum_user.updated_at, sqlc.arg(observed_at)::timestamptz)
    FROM mapping
    WHERE forum_user.id = mapping.user_id
      AND forum_user.administration_revision = sqlc.arg(expected_revision)
      AND forum_user.administration_revision < 9223372036854775807
    RETURNING forum_user.id, forum_user.updated_at,
              forum_user.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'grant_group_membership', sqlc.arg(reason),
           jsonb_build_object('group_id', mapping.group_id, 'member', false,
                              'administration_revision', sqlc.arg(expected_revision)::bigint),
           jsonb_build_object('group_id', mapping.group_id, 'member', true,
                              'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), changed.updated_at
    FROM changed
    JOIN mapping ON mapping.user_id = changed.id
    RETURNING id, target_user_id
)
SELECT changed.id AS user_id, changed.administration_revision,
       audit.id AS audit_id
FROM changed
JOIN audit ON audit.target_user_id = changed.id;

-- name: RevokeAdministrationMembershipAndAudit :one
WITH mapping AS (
    DELETE FROM public.forum_group_members AS membership
    WHERE membership.group_id = sqlc.arg(group_id)
      AND membership.user_id = sqlc.arg(target_user_id)
    RETURNING membership.group_id, membership.user_id
), changed AS (
    UPDATE public.users AS forum_user
    SET administration_revision = forum_user.administration_revision + 1,
        updated_at = GREATEST(forum_user.updated_at, sqlc.arg(observed_at)::timestamptz)
    FROM mapping
    WHERE forum_user.id = mapping.user_id
      AND forum_user.administration_revision = sqlc.arg(expected_revision)
      AND forum_user.administration_revision < 9223372036854775807
    RETURNING forum_user.id, forum_user.updated_at,
              forum_user.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'revoke_group_membership', sqlc.arg(reason),
           jsonb_build_object('group_id', mapping.group_id, 'member', true,
                              'administration_revision', sqlc.arg(expected_revision)::bigint),
           jsonb_build_object('group_id', mapping.group_id, 'member', false,
                              'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), changed.updated_at
    FROM changed
    JOIN mapping ON mapping.user_id = changed.id
    RETURNING id, target_user_id
)
SELECT changed.id AS user_id, changed.administration_revision,
       audit.id AS audit_id
FROM changed
JOIN audit ON audit.target_user_id = changed.id;

-- name: ChangeAdministrationRoleAndAudit :one
WITH changed AS (
    UPDATE public.users AS forum_user
    SET role = sqlc.arg(role),
        updated_at = GREATEST(forum_user.updated_at, sqlc.arg(observed_at)::timestamptz),
        administration_revision = forum_user.administration_revision + 1
    WHERE forum_user.id = sqlc.arg(target_user_id)
      AND forum_user.role = sqlc.arg(expected_role)
      AND forum_user.administration_revision = sqlc.arg(expected_revision)
      AND forum_user.administration_revision < 9223372036854775807
    RETURNING forum_user.id, forum_user.role, forum_user.updated_at,
              forum_user.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'change_role', sqlc.arg(reason),
           jsonb_build_object('role', sqlc.arg(expected_role)::text,
                              'administration_revision', sqlc.arg(expected_revision)::bigint),
           jsonb_build_object('role', changed.role,
                              'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), changed.updated_at
    FROM changed
    RETURNING id, target_user_id
), revoked AS (
    UPDATE public.sessions AS session
    SET revoked_at = GREATEST(session.issued_at, sqlc.arg(observed_at)::timestamptz)
    FROM changed
    WHERE session.user_id = changed.id
      AND session.revoked_at IS NULL
    RETURNING session.id
)
SELECT changed.id AS user_id, changed.role,
       changed.administration_revision, audit.id AS audit_id,
       (SELECT count(*)::bigint FROM revoked) AS revoked_sessions
FROM changed
JOIN audit ON audit.target_user_id = changed.id;
