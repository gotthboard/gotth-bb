-- name: LoadRuntimeControlSettings :one
SELECT registration_mode,
       maintenance_enabled,
       maintenance_message,
       publish_rate_limit,
       new_account_publish_rate_limit,
       publish_window_seconds,
       new_account_period_seconds,
       session_idle_seconds,
       auth_revalidate_seconds,
       administration_revision
FROM public.site_settings
WHERE singleton
  AND (SELECT count(*) FROM public.site_settings) = 1;

-- name: LoadEditableControlSettings :one
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
SELECT (settings.singleton IS TRUE)::boolean AS settings_present,
       COALESCE(settings.registration_mode, '')::text AS registration_mode,
       COALESCE(settings.maintenance_enabled, false)::boolean AS maintenance_enabled,
       COALESCE(settings.maintenance_message, '')::text AS maintenance_message,
       COALESCE(settings.publish_rate_limit, 0)::integer AS publish_rate_limit,
       COALESCE(settings.new_account_publish_rate_limit, 0)::integer AS new_account_publish_rate_limit,
       COALESCE(settings.publish_window_seconds, 0)::integer AS publish_window_seconds,
       COALESCE(settings.new_account_period_seconds, 0)::integer AS new_account_period_seconds,
       COALESCE(settings.session_idle_seconds, 0)::integer AS session_idle_seconds,
       COALESCE(settings.auth_revalidate_seconds, 0)::integer AS auth_revalidate_seconds,
       COALESCE(settings.administration_revision, 0)::bigint AS administration_revision
FROM actor
LEFT JOIN LATERAL (
    SELECT singleton, registration_mode, maintenance_enabled,
           maintenance_message, publish_rate_limit,
           new_account_publish_rate_limit, publish_window_seconds,
           new_account_period_seconds, session_idle_seconds,
           auth_revalidate_seconds, administration_revision
    FROM public.site_settings
    WHERE singleton
) AS settings ON true;

-- name: LockControlSettingsAdministrator :one
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
FOR UPDATE OF forum_user;

-- name: LockRuntimeControlSettings :one
SELECT registration_mode, maintenance_enabled, maintenance_message,
       publish_rate_limit, new_account_publish_rate_limit,
       publish_window_seconds, new_account_period_seconds,
       session_idle_seconds, auth_revalidate_seconds,
       administration_revision
FROM public.site_settings
WHERE singleton
FOR UPDATE;

-- name: UpdateControlSettingsAndAudit :one
WITH updated AS (
    UPDATE public.site_settings AS settings
    SET registration_mode = sqlc.arg(registration_mode),
        maintenance_enabled = sqlc.arg(maintenance_enabled),
        maintenance_message = sqlc.arg(maintenance_message),
        publish_rate_limit = sqlc.arg(publish_rate_limit),
        new_account_publish_rate_limit = sqlc.arg(new_account_publish_rate_limit),
        publish_window_seconds = sqlc.arg(publish_window_seconds),
        new_account_period_seconds = sqlc.arg(new_account_period_seconds),
        session_idle_seconds = sqlc.arg(session_idle_seconds),
        auth_revalidate_seconds = sqlc.arg(auth_revalidate_seconds),
        administration_revision = settings.administration_revision + 1,
        updated_at = sqlc.arg(observed_at)::timestamptz
    WHERE settings.singleton
      AND settings.administration_revision = sqlc.arg(expected_revision)
      AND settings.administration_revision < 9223372036854775807
    RETURNING settings.administration_revision
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_site, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'site', true,
           'update_control_settings', sqlc.arg(reason),
           jsonb_build_object(
               'registration_mode', sqlc.arg(previous_registration_mode)::text,
               'maintenance_enabled', sqlc.arg(previous_maintenance_enabled)::boolean,
               'maintenance_message_sha256', sqlc.arg(previous_maintenance_message_sha256)::text,
               'publish_rate_limit', sqlc.arg(previous_publish_rate_limit)::integer,
               'new_account_publish_rate_limit', sqlc.arg(previous_new_account_publish_rate_limit)::integer,
               'publish_window_seconds', sqlc.arg(previous_publish_window_seconds)::integer,
               'new_account_period_seconds', sqlc.arg(previous_new_account_period_seconds)::integer,
               'session_idle_seconds', sqlc.arg(previous_session_idle_seconds)::integer,
               'auth_revalidate_seconds', sqlc.arg(previous_auth_revalidate_seconds)::integer
           ),
           jsonb_build_object(
               'registration_mode', sqlc.arg(registration_mode)::text,
               'maintenance_enabled', sqlc.arg(maintenance_enabled)::boolean,
               'maintenance_message_sha256', sqlc.arg(maintenance_message_sha256)::text,
               'publish_rate_limit', sqlc.arg(publish_rate_limit)::integer,
               'new_account_publish_rate_limit', sqlc.arg(new_account_publish_rate_limit)::integer,
               'publish_window_seconds', sqlc.arg(publish_window_seconds)::integer,
               'new_account_period_seconds', sqlc.arg(new_account_period_seconds)::integer,
               'session_idle_seconds', sqlc.arg(session_idle_seconds)::integer,
               'auth_revalidate_seconds', sqlc.arg(auth_revalidate_seconds)::integer
           ),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM updated
    RETURNING id
)
SELECT updated.administration_revision, audit.id AS audit_id
FROM updated
JOIN audit ON true;
