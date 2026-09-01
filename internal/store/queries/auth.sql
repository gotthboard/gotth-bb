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
