-- name: ListSessionsForAdministration :many
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND forum_user.authentik_sync_state = 'accepted'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
      )
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), target AS MATERIALIZED (
    SELECT forum_user.id, forum_user.display_name,
           forum_user.administration_revision
    FROM actor
    JOIN public.users AS forum_user
      ON forum_user.id = sqlc.arg(target_user_id)
), settings AS MATERIALIZED (
    SELECT session_idle_seconds
    FROM public.site_settings
    WHERE singleton
      AND (SELECT count(*) FROM public.site_settings) = 1
), candidate AS MATERIALIZED (
    SELECT local_session.id, local_session.issued_at,
           local_session.last_seen_at, local_session.validated_at,
           local_session.expires_at
    FROM target
    JOIN settings ON true
    JOIN LATERAL (
        SELECT session_row.id, session_row.issued_at,
               session_row.last_seen_at, session_row.validated_at,
               session_row.expires_at
        FROM public.sessions AS session_row
        WHERE session_row.user_id = target.id
          AND session_row.revoked_at IS NULL
          AND session_row.issued_at <= sqlc.arg(observed_at)::timestamptz
          AND session_row.last_seen_at <= sqlc.arg(observed_at)::timestamptz
          AND session_row.validated_at <= sqlc.arg(observed_at)::timestamptz
          AND session_row.expires_at > sqlc.arg(observed_at)::timestamptz
          AND session_row.last_seen_at >
              sqlc.arg(observed_at)::timestamptz
              - pg_catalog.make_interval(secs => settings.session_idle_seconds)
        ORDER BY session_row.id DESC
        LIMIT sqlc.arg(page_limit)
    ) AS local_session ON true
)
SELECT (actor.id IS NOT NULL)::boolean AS actor_present,
       (target.id IS NOT NULL)::boolean AS target_present,
       (settings.session_idle_seconds IS NOT NULL)::boolean AS settings_present,
       COALESCE(target.id, 0)::bigint AS target_user_id,
       COALESCE(target.display_name, '')::text AS display_name,
       COALESCE(target.administration_revision, 0)::bigint AS target_revision,
       (candidate.id IS NOT NULL)::boolean AS session_present,
       COALESCE(candidate.id, 0)::bigint AS session_id,
       candidate.issued_at,
       candidate.last_seen_at,
       candidate.validated_at,
       candidate.expires_at
FROM (VALUES (true)) AS anchor(singleton)
LEFT JOIN actor ON true
LEFT JOIN target ON true
LEFT JOIN settings ON true
LEFT JOIN candidate ON true
ORDER BY candidate.id DESC NULLS LAST;

-- name: RevokeOneSessionForAdministrationAndAudit :one
WITH settings AS MATERIALIZED (
    SELECT session_idle_seconds
    FROM public.site_settings
    WHERE singleton
      AND (SELECT count(*) FROM public.site_settings) = 1
), revoked AS (
    UPDATE public.sessions AS local_session
    SET revoked_at = GREATEST(local_session.issued_at, sqlc.arg(observed_at)::timestamptz)
    FROM settings
    WHERE local_session.id = sqlc.arg(session_id)
      AND local_session.user_id = sqlc.arg(target_user_id)
      AND local_session.revoked_at IS NULL
      AND local_session.issued_at <= sqlc.arg(observed_at)::timestamptz
      AND local_session.last_seen_at <= sqlc.arg(observed_at)::timestamptz
      AND local_session.validated_at <= sqlc.arg(observed_at)::timestamptz
      AND local_session.expires_at > sqlc.arg(observed_at)::timestamptz
      AND local_session.last_seen_at >
          sqlc.arg(observed_at)::timestamptz
          - pg_catalog.make_interval(secs => settings.session_idle_seconds)
    RETURNING local_session.id
), changed AS (
    SELECT count(*)::bigint AS revoked_count
    FROM revoked
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user',
           sqlc.arg(target_user_id), 'revoke_session', sqlc.arg(reason),
           jsonb_build_object('active_sessions_revoked', 0),
           jsonb_build_object('active_sessions_revoked', changed.revoked_count),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed
    WHERE changed.revoked_count = 1
    RETURNING id
)
SELECT changed.revoked_count, audit.id AS audit_id
FROM changed
JOIN audit ON true;

-- name: RevokeAllSessionsForAdministrationAndAudit :one
WITH settings AS MATERIALIZED (
    SELECT session_idle_seconds
    FROM public.site_settings
    WHERE singleton
      AND (SELECT count(*) FROM public.site_settings) = 1
), revoked AS (
    UPDATE public.sessions AS local_session
    SET revoked_at = GREATEST(local_session.issued_at, sqlc.arg(observed_at)::timestamptz)
    FROM settings
    WHERE local_session.user_id = sqlc.arg(target_user_id)
      AND local_session.revoked_at IS NULL
      AND local_session.issued_at <= sqlc.arg(observed_at)::timestamptz
      AND local_session.last_seen_at <= sqlc.arg(observed_at)::timestamptz
      AND local_session.validated_at <= sqlc.arg(observed_at)::timestamptz
      AND local_session.expires_at > sqlc.arg(observed_at)::timestamptz
      AND local_session.last_seen_at >
          sqlc.arg(observed_at)::timestamptz
          - pg_catalog.make_interval(secs => settings.session_idle_seconds)
    RETURNING local_session.id
), changed AS (
    SELECT count(*)::bigint AS revoked_count
    FROM revoked
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user',
           sqlc.arg(target_user_id), 'revoke_user_sessions', sqlc.arg(reason),
           jsonb_build_object('active_sessions_revoked', 0),
           jsonb_build_object('active_sessions_revoked', changed.revoked_count),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed
    WHERE changed.revoked_count > 0
    RETURNING id
)
SELECT changed.revoked_count, audit.id AS audit_id
FROM changed
JOIN audit ON true;

-- name: LoadEmailTestStateForAdministration :one
WITH actor AS MATERIALIZED (
    SELECT forum_user.id
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = 'administrator'
      AND forum_user.authentik_sync_state = 'accepted'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
      )
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
)
SELECT (actor.id IS NOT NULL)::boolean AS actor_present,
       (state.administrator_id IS NOT NULL)::boolean AS state_present,
       COALESCE(state.status, '')::text AS status,
       state.requested_at, state.completed_at, state.next_allowed_at
FROM (VALUES (true)) AS anchor(singleton)
LEFT JOIN actor ON true
LEFT JOIN public.email_test_state AS state
  ON state.administrator_id = actor.id;

-- name: LockEmailTestAdministrator :one
SELECT forum_user.id, forum_user.email
FROM public.users AS forum_user
WHERE forum_user.id = sqlc.arg(actor_user_id)
  AND forum_user.role = 'administrator'
  AND forum_user.authentik_sync_state = 'accepted'
  AND (
      forum_user.suspended_at IS NULL
      OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz
      OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz
  )
  AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
FOR UPDATE OF forum_user;

-- name: ReserveEmailTestAndAudit :one
WITH reserved AS (
    INSERT INTO public.email_test_state (
        administrator_id, idempotency_key, status,
        requested_at, completed_at, next_allowed_at
    )
    VALUES (
        sqlc.arg(actor_user_id), sqlc.arg(idempotency_key), 'requested',
        sqlc.arg(observed_at)::timestamptz, NULL,
        sqlc.arg(observed_at)::timestamptz + interval '5 minutes'
    )
    ON CONFLICT (administrator_id) DO UPDATE
    SET idempotency_key = EXCLUDED.idempotency_key,
        status = EXCLUDED.status,
        requested_at = EXCLUDED.requested_at,
        completed_at = EXCLUDED.completed_at,
        next_allowed_at = EXCLUDED.next_allowed_at
    WHERE email_test_state.next_allowed_at <= EXCLUDED.requested_at
    RETURNING administrator_id, requested_at, next_allowed_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', reserved.administrator_id, 'user',
           reserved.administrator_id, 'request_test_email', sqlc.arg(reason),
           '{}'::jsonb,
           jsonb_build_object(
               'status', 'requested',
               'idempotency_sha256', sqlc.arg(idempotency_sha256)::text
           ),
           sqlc.arg(request_id), reserved.requested_at
    FROM reserved
    RETURNING id
)
SELECT reserved.requested_at, reserved.next_allowed_at, audit.id AS audit_id
FROM reserved
JOIN audit ON true;

-- name: CompleteEmailTestAndAudit :one
WITH completed AS (
    UPDATE public.email_test_state AS state
    SET status = sqlc.arg(status),
        completed_at = sqlc.arg(observed_at)::timestamptz
    WHERE state.administrator_id = sqlc.arg(actor_user_id)
      AND state.idempotency_key = sqlc.arg(idempotency_key)
      AND state.status = 'requested'
      AND sqlc.arg(status)::text IN ('accepted', 'failed', 'unknown')
    RETURNING state.administrator_id, state.status, state.completed_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', completed.administrator_id, 'user',
           completed.administrator_id, 'test_email', sqlc.arg(reason),
           jsonb_build_object(
               'status', 'requested',
               'idempotency_sha256', sqlc.arg(idempotency_sha256)::text
           ),
           jsonb_build_object(
               'status', completed.status,
               'idempotency_sha256', sqlc.arg(idempotency_sha256)::text
           ),
           sqlc.arg(request_id), completed.completed_at
    FROM completed
    RETURNING id
)
SELECT completed.status, completed.completed_at, audit.id AS audit_id
FROM completed
JOIN audit ON true;
