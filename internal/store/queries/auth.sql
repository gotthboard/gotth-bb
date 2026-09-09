-- name: InsertOIDCLoginAttempt :exec
INSERT INTO public.oidc_login_attempts (
    state_hash,
    nonce_ciphertext,
    pkce_verifier_ciphertext,
    purpose,
    session_id,
    return_path,
    created_at,
    expires_at
)
VALUES (
    sqlc.arg(state_hash),
    sqlc.arg(nonce_ciphertext),
    sqlc.arg(pkce_verifier_ciphertext),
    sqlc.arg(purpose),
    sqlc.narg(session_id),
    sqlc.arg(return_path),
    sqlc.arg(created_at),
    sqlc.arg(expires_at)
);

-- name: ConsumeOIDCLoginAttempt :one
UPDATE public.oidc_login_attempts
SET consumed_at = sqlc.arg(consumed_at)
WHERE state_hash = sqlc.arg(state_hash)
  AND consumed_at IS NULL
  AND created_at <= sqlc.arg(consumed_at)
  AND expires_at > sqlc.arg(consumed_at)
RETURNING *;

-- name: GetActiveSession :one
SELECT
    session.id AS session_id,
    session.user_id,
    session.issued_at,
    session.last_seen_at,
    session.validated_at,
    session.expires_at,
    forum_user.role,
    forum_user.muted_until,
    ARRAY(
        SELECT membership.group_id
        FROM public.forum_group_members AS membership
        WHERE membership.user_id = forum_user.id
        ORDER BY membership.group_id
    )::bigint[] AS group_ids
FROM public.sessions AS session
JOIN public.users AS forum_user ON forum_user.id = session.user_id
WHERE session.id = public.session_id_for_token(sqlc.arg(token_hash))
  AND session.revoked_at IS NULL
  AND session.expires_at > sqlc.arg(observed_at)
  AND session.last_seen_at > sqlc.arg(idle_cutoff)
  AND forum_user.authentik_sync_state = 'accepted'
  AND (
      forum_user.suspended_at IS NULL
      OR forum_user.suspended_at > sqlc.arg(observed_at)
      OR forum_user.suspended_until <= sqlc.arg(observed_at)
  );

-- name: GetActiveSessionWithControl :one
SELECT
    session.id AS session_id,
    session.user_id,
    session.issued_at,
    session.last_seen_at,
    session.validated_at,
    session.expires_at,
    forum_user.role,
    forum_user.muted_until,
    ARRAY(
        SELECT membership.group_id
        FROM public.forum_group_members AS membership
        WHERE membership.user_id = forum_user.id
        ORDER BY membership.group_id
    )::bigint[] AS group_ids,
    settings.session_idle_seconds,
    settings.auth_revalidate_seconds
FROM public.sessions AS session
JOIN public.users AS forum_user ON forum_user.id = session.user_id
JOIN public.site_settings AS settings ON settings.singleton
WHERE session.id = public.session_id_for_token(sqlc.arg(token_hash))
  AND (SELECT count(*) FROM public.site_settings) = 1
  AND session.revoked_at IS NULL
  AND session.expires_at > sqlc.arg(observed_at)
  AND session.last_seen_at > sqlc.arg(observed_at) - pg_catalog.make_interval(secs => settings.session_idle_seconds)
  AND forum_user.authentik_sync_state = 'accepted'
  AND (
      forum_user.suspended_at IS NULL
      OR forum_user.suspended_at > sqlc.arg(observed_at)
      OR forum_user.suspended_until <= sqlc.arg(observed_at)
  );

-- name: TouchSession :execrows
UPDATE public.sessions
SET last_seen_at = sqlc.arg(observed_at)
WHERE id = sqlc.arg(session_id)
  AND revoked_at IS NULL
  AND expires_at > sqlc.arg(observed_at)
  AND last_seen_at <= sqlc.arg(touch_before)
  AND last_seen_at < sqlc.arg(observed_at);

-- name: RevokeSession :one
SELECT public.revoke_session_by_token(
    sqlc.arg(token_hash), sqlc.arg(observed_at)
)::bigint;

-- name: RevokeSessionForRotation :one
SELECT public.revoke_session_for_rotation_by_token(
    sqlc.arg(session_id), sqlc.arg(token_hash), sqlc.arg(observed_at)
)::bigint;
