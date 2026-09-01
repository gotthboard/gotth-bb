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
