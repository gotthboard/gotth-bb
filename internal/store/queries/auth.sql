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
    forum_user.muted_until
FROM public.sessions AS session
JOIN public.users AS forum_user ON forum_user.id = session.user_id
WHERE session.token_hash = sqlc.arg(token_hash)
  AND session.revoked_at IS NULL
  AND session.expires_at > sqlc.arg(observed_at)
  AND session.last_seen_at > sqlc.arg(idle_cutoff)
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
