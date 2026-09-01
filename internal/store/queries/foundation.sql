-- name: GetUserByExternalIdentity :one
SELECT u.*
FROM public.users AS u
JOIN public.external_identities AS identity ON identity.user_id = u.id
WHERE identity.issuer = sqlc.arg(issuer)
  AND identity.subject = sqlc.arg(subject);

-- name: InsertUser :one
INSERT INTO public.users (display_name, email, avatar_url, created_at, updated_at, last_login_at)
VALUES (
    sqlc.arg(display_name), sqlc.narg(email), sqlc.narg(avatar_url),
    sqlc.arg(login_at), sqlc.arg(login_at), sqlc.arg(login_at)
)
RETURNING *;

-- name: InsertExternalIdentity :exec
INSERT INTO public.external_identities (user_id, issuer, subject, created_at, last_verified_at)
VALUES (
    sqlc.arg(user_id), sqlc.arg(issuer), sqlc.arg(subject),
    sqlc.arg(verified_at), sqlc.arg(verified_at)
);

-- name: LockExternalIdentity :one
WITH acquired AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(
        hashtext(sqlc.arg(issuer)::text),
        hashtext(sqlc.arg(subject)::text)
    ) AS ignored
)
SELECT true::boolean AS locked
FROM acquired;

-- name: UpdateUserFromOIDC :one
UPDATE public.users
SET display_name = sqlc.arg(display_name),
    email = sqlc.narg(email),
    avatar_url = sqlc.narg(avatar_url),
    updated_at = sqlc.arg(login_at),
    last_login_at = sqlc.arg(login_at)
WHERE id = sqlc.arg(user_id)
RETURNING *;

-- name: UpdateExternalIdentityVerification :exec
UPDATE public.external_identities
SET last_verified_at = sqlc.arg(verified_at)
WHERE user_id = sqlc.arg(user_id);

-- name: InsertSession :one
INSERT INTO public.sessions (
    token_hash,
    user_id,
    issued_at,
    last_seen_at,
    validated_at,
    expires_at,
    user_agent_hash,
    ip_prefix
)
VALUES (
    sqlc.arg(token_hash),
    sqlc.arg(user_id),
    sqlc.arg(issued_at),
    sqlc.arg(issued_at),
    sqlc.arg(issued_at),
    sqlc.arg(expires_at),
    sqlc.narg(user_agent_hash),
    sqlc.narg(ip_prefix)
)
RETURNING *;

-- name: LockGovernanceState :one
SELECT singleton
FROM public.governance_state
WHERE singleton
FOR UPDATE;

-- name: CountGovernanceRows :one
SELECT count(*)::bigint
FROM public.governance_state;

-- name: CountActiveAdministrators :one
SELECT count(*)::bigint
FROM public.users
WHERE role = 'administrator'
  AND (
      suspended_at IS NULL
      OR suspended_at > sqlc.arg(at_time)::timestamptz
      OR suspended_until <= sqlc.arg(at_time)::timestamptz
  );
