-- name: LoadRuntimeSMTPSettings :one
SELECT host, port, username, from_address, tls_mode, timeout_seconds,
       password_envelope, administration_revision, verified_revision
FROM public.smtp_settings
WHERE singleton
  AND (SELECT count(*) FROM public.smtp_settings) = 1;

-- name: LockRuntimeSMTPSettings :one
SELECT host, port, username, from_address, tls_mode, timeout_seconds,
       password_envelope, administration_revision, verified_revision
FROM public.smtp_settings
WHERE singleton
  AND (SELECT count(*) FROM public.smtp_settings) = 1
FOR UPDATE;

-- name: LoadEditableSMTPSettings :one
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
       (settings.singleton IS TRUE)::boolean AS settings_present,
       COALESCE(settings.host, '')::text AS host,
       COALESCE(settings.port, 0)::integer AS port,
       COALESCE(settings.username, '')::text AS username,
       COALESCE(settings.from_address, '')::text AS from_address,
       COALESCE(settings.tls_mode, '')::text AS tls_mode,
       COALESCE(settings.timeout_seconds, 0)::integer AS timeout_seconds,
       (settings.password_envelope IS NOT NULL)::boolean AS password_present,
       COALESCE(settings.administration_revision, 0)::bigint AS administration_revision,
       settings.verified_revision
FROM (VALUES (true)) AS anchor(singleton)
LEFT JOIN actor ON true
LEFT JOIN public.smtp_settings AS settings ON settings.singleton;

-- name: UpdateSMTPSettingsAndAudit :one
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
    FOR UPDATE OF forum_user
), registration AS MATERIALIZED (
    SELECT registration_mode
    FROM public.site_settings
    WHERE singleton
    FOR UPDATE
), prior AS MATERIALIZED (
    SELECT administration_revision,
           (host <> '')::boolean AS configured,
           (password_envelope IS NOT NULL)::boolean AS password_present,
           (verified_revision = administration_revision)::boolean AS verified
    FROM public.smtp_settings
    WHERE singleton
), updated AS (
    UPDATE public.smtp_settings AS settings
    SET host = sqlc.arg(host), port = sqlc.arg(port),
        username = sqlc.arg(username), from_address = sqlc.arg(from_address),
        tls_mode = sqlc.arg(tls_mode), timeout_seconds = sqlc.arg(timeout_seconds),
        password_envelope = sqlc.arg(password_envelope),
        administration_revision = settings.administration_revision + 1,
        verified_revision = NULL,
        updated_at = sqlc.arg(observed_at)::timestamptz
    FROM actor, registration
    WHERE settings.singleton
      AND registration.registration_mode = 'closed'
      AND settings.administration_revision = sqlc.arg(expected_revision)
      AND settings.administration_revision < 9223372036854775807
    RETURNING settings.administration_revision,
              (settings.host <> '')::boolean AS configured,
              (settings.password_envelope IS NOT NULL)::boolean AS password_present
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_site,
        action_type, reason, previous_state, resulting_state,
        request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'site', true,
           'update_smtp_settings', sqlc.arg(reason),
           jsonb_build_object(
               'revision', prior.administration_revision,
               'configured', prior.configured,
               'password_present', prior.password_present,
               'verified', prior.verified
           ),
           jsonb_build_object(
               'revision', updated.administration_revision,
               'configured', updated.configured,
               'password_present', updated.password_present,
               'verified', false
           ),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM updated
    JOIN prior ON true
    RETURNING id
)
SELECT updated.administration_revision, audit.id AS audit_id
FROM updated
JOIN audit ON true;
