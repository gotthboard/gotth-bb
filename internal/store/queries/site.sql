-- name: LoadSiteShellPresentation :one
SELECT site_name, site_description, brand_theme
FROM public.site_settings
WHERE singleton;

-- name: LoadPublicRules :one
SELECT site_name, site_description, brand_theme,
       rules_markdown, rules_html, rules_renderer_version
FROM public.site_settings
WHERE singleton;

-- name: LoadEditableSiteSettings :one
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
)
SELECT (settings.singleton IS TRUE)::boolean AS settings_present,
       COALESCE(settings.site_name, '')::text AS site_name,
       COALESCE(settings.site_description, '')::text AS site_description,
       COALESCE(settings.brand_theme, '')::text AS brand_theme,
       COALESCE(settings.rules_markdown, '')::text AS rules_markdown,
       COALESCE(settings.rules_html, '')::text AS rules_html,
       COALESCE(settings.rules_renderer_version, '')::text AS rules_renderer_version,
       COALESCE(settings.administration_revision, 0)::bigint AS administration_revision
FROM actor
LEFT JOIN LATERAL (
    SELECT site_name, site_description, brand_theme,
           rules_markdown, rules_html, rules_renderer_version,
           administration_revision
    FROM public.site_settings
    WHERE singleton
) AS settings ON true;

-- name: ConfigureAdministrationTransaction :one
SELECT
    set_config('statement_timeout', '2000ms', true),
    set_config('lock_timeout', '250ms', true);

-- name: LockSiteSettingsAdministrator :one
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
FOR UPDATE OF forum_user;

-- name: LockSiteSettings :one
SELECT site_name, site_description, brand_theme,
       rules_markdown, rules_html, rules_renderer_version,
       administration_revision, updated_at
FROM public.site_settings
WHERE singleton
FOR UPDATE;

-- name: UpdateSiteSettingsAndAudit :one
WITH updated AS (
    UPDATE public.site_settings AS settings
    SET site_name = sqlc.arg(site_name),
        site_description = sqlc.arg(site_description),
        brand_theme = sqlc.arg(brand_theme),
        rules_markdown = sqlc.arg(rules_markdown),
        rules_html = sqlc.arg(rules_html),
        rules_renderer_version = sqlc.arg(rules_renderer_version),
        administration_revision = settings.administration_revision + 1,
        updated_at = sqlc.arg(observed_at)::timestamptz
    WHERE settings.singleton
      AND settings.administration_revision = sqlc.arg(expected_revision)
      AND settings.administration_revision < 9223372036854775807
    RETURNING settings.administration_revision
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind,
        actor_user_id,
        target_type,
        target_site,
        action_type,
        reason,
        previous_state,
        resulting_state,
        request_id,
        created_at
    )
    SELECT
        'forum_user',
        sqlc.arg(actor_user_id),
        'site',
        true,
        'update_site_settings',
        sqlc.arg(reason),
        jsonb_build_object(
            'site_name', sqlc.arg(previous_site_name)::text,
            'site_description', sqlc.arg(previous_site_description)::text,
            'brand_theme', sqlc.arg(previous_brand_theme)::text,
            'rules_renderer_version', sqlc.arg(previous_rules_renderer_version)::text,
            'rules_markdown_sha256', sqlc.arg(previous_rules_markdown_sha256)::text,
            'rules_html_sha256', sqlc.arg(previous_rules_html_sha256)::text
        ),
        jsonb_build_object(
            'site_name', sqlc.arg(site_name)::text,
            'site_description', sqlc.arg(site_description)::text,
            'brand_theme', sqlc.arg(brand_theme)::text,
            'rules_renderer_version', sqlc.arg(rules_renderer_version)::text,
            'rules_markdown_sha256', sqlc.arg(rules_markdown_sha256)::text,
            'rules_html_sha256', sqlc.arg(rules_html_sha256)::text
        ),
        sqlc.arg(request_id),
        sqlc.arg(observed_at)::timestamptz
    FROM updated
    RETURNING id
)
SELECT updated.administration_revision, audit.id AS audit_id
FROM updated
JOIN audit ON true;
