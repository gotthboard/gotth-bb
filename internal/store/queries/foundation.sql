-- name: GetUserByExternalIdentity :one
SELECT u.*
FROM public.users AS u
JOIN public.external_identities AS identity ON identity.user_id = u.id
WHERE identity.issuer = sqlc.arg(issuer)
  AND identity.subject = sqlc.arg(subject);

-- name: GetPendingRegistrationLoginState :one
SELECT status
FROM public.pending_registrations
WHERE authentik_subject::text = sqlc.arg(subject_text)::text;

-- name: InsertUser :one
INSERT INTO public.users (
    display_name, email, avatar_url, created_at, updated_at, last_login_at,
    authentik_sync_state
)
VALUES (
    sqlc.arg(display_name), sqlc.narg(email), sqlc.narg(avatar_url),
    sqlc.arg(login_at), sqlc.arg(login_at), sqlc.arg(login_at), 'accepted'
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
  AND authentik_sync_state = 'accepted'
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
RETURNING id, user_id;

-- name: GetActiveSessionForRotation :one
	SELECT
	    session.user_id,
	    identity.issuer,
	    identity.subject,
    session.expires_at
FROM public.sessions AS session
JOIN public.users AS forum_user ON forum_user.id = session.user_id
JOIN public.external_identities AS identity ON identity.user_id = session.user_id
WHERE session.id = sqlc.arg(session_id)
  AND session.id = public.session_id_for_token(sqlc.arg(token_hash))
  AND session.revoked_at IS NULL
  AND session.issued_at <= sqlc.arg(observed_at)
  AND session.last_seen_at <= sqlc.arg(observed_at)
  AND session.validated_at <= sqlc.arg(observed_at)
  AND session.expires_at > sqlc.arg(observed_at)
  AND session.last_seen_at > sqlc.arg(idle_cutoff)
  AND forum_user.authentik_sync_state = 'accepted'
  AND (
      forum_user.suspended_at IS NULL
      OR forum_user.suspended_at > sqlc.arg(observed_at)
      OR forum_user.suspended_until <= sqlc.arg(observed_at)
  )
FOR UPDATE OF session, forum_user, identity;

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
  AND authentik_sync_state = 'accepted'
  AND (
      suspended_at IS NULL
      OR suspended_at > sqlc.arg(at_time)::timestamptz
      OR suspended_until <= sqlc.arg(at_time)::timestamptz
  );

-- name: CountAdministratorBootstraps :one
SELECT count(*)::bigint
FROM public.moderation_actions
WHERE action_type = 'bootstrap_administrator';

-- name: BootstrapAdministratorAndAudit :one
WITH target AS MATERIALIZED (
    SELECT forum_user.id, forum_user.role AS previous_role
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(user_id)
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(at_time)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(at_time)::timestamptz
      )
    FOR UPDATE OF forum_user
),
updated AS (
    UPDATE public.users AS forum_user
    SET role = 'administrator',
        updated_at = sqlc.arg(at_time)::timestamptz
    FROM target
    WHERE forum_user.id = target.id
    RETURNING forum_user.id
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind,
        operator_identifier,
        target_type,
        target_user_id,
        action_type,
        previous_state,
        resulting_state,
        request_id,
        created_at
    )
    SELECT
        'operator',
        sqlc.arg(operator_identifier),
        'user',
        updated.id,
        'bootstrap_administrator',
        jsonb_build_object('role', target.previous_role),
        jsonb_build_object('role', 'administrator'),
        sqlc.arg(request_id),
        sqlc.arg(at_time)::timestamptz
    FROM updated
    JOIN target ON target.id = updated.id
    RETURNING id, target_user_id
)
SELECT updated.id AS user_id, audit.id AS audit_id
FROM updated
JOIN audit ON audit.target_user_id = updated.id;

-- name: RebindExternalIdentityAndAudit :one
WITH target AS MATERIALIZED (
    SELECT identity.user_id
    FROM public.external_identities AS identity
    WHERE identity.issuer = sqlc.arg(old_issuer)
      AND identity.subject = sqlc.arg(old_subject)
    FOR UPDATE OF identity
),
updated AS (
    UPDATE public.external_identities AS identity
    SET issuer = sqlc.arg(new_issuer),
        subject = sqlc.arg(new_subject),
        last_verified_at = greatest(identity.last_verified_at, sqlc.arg(at_time)::timestamptz)
    FROM target
    WHERE identity.user_id = target.user_id
      AND NOT EXISTS (
          SELECT 1
          FROM public.external_identities AS collision
          WHERE collision.issuer = sqlc.arg(new_issuer)
            AND collision.subject = sqlc.arg(new_subject)
            AND collision.user_id <> target.user_id
      )
    RETURNING identity.user_id
),
revoked AS (
    UPDATE public.sessions AS session
    SET revoked_at = greatest(session.issued_at, sqlc.arg(at_time)::timestamptz)
    FROM updated
    WHERE session.user_id = updated.user_id
      AND session.revoked_at IS NULL
    RETURNING session.id
),
discarded AS (
    DELETE FROM public.oidc_login_attempts AS attempt
    WHERE attempt.consumed_at IS NULL
      AND EXISTS (SELECT 1 FROM updated)
    RETURNING attempt.state_hash
),
counts AS MATERIALIZED (
    SELECT
        (SELECT count(*)::bigint FROM revoked) AS revoked_sessions,
        (SELECT count(*)::bigint FROM discarded) AS discarded_login_attempts
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind,
        operator_identifier,
        target_type,
        target_user_id,
        action_type,
        previous_state,
        resulting_state,
        request_id,
        created_at
    )
    SELECT
        'operator',
        sqlc.arg(operator_identifier),
        'user',
        updated.user_id,
        'rebind_external_identity',
        jsonb_build_object('provider', 'previous'),
        jsonb_build_object(
            'provider', 'replacement',
            'revoked_sessions', counts.revoked_sessions,
            'discarded_login_attempts', counts.discarded_login_attempts
        ),
        sqlc.arg(request_id),
        sqlc.arg(at_time)::timestamptz
    FROM updated
    CROSS JOIN counts
    RETURNING id, target_user_id
)
SELECT
    updated.user_id,
    audit.id AS audit_id,
    counts.revoked_sessions,
    counts.discarded_login_attempts
FROM updated
JOIN audit ON audit.target_user_id = updated.user_id
CROSS JOIN counts;

-- name: ClaimInitialAdministratorAndAudit :one
WITH current_session AS MATERIALIZED (
    SELECT session.id, session.user_id
    FROM public.sessions AS session
    WHERE session.id = sqlc.arg(session_id)
      AND session.user_id = sqlc.arg(user_id)
      AND session.revoked_at IS NULL
      AND session.issued_at <= sqlc.arg(at_time)::timestamptz
      AND session.expires_at > sqlc.arg(at_time)::timestamptz
    FOR UPDATE OF session
),
target AS MATERIALIZED (
    SELECT forum_user.id, forum_user.role AS previous_role
    FROM public.users AS forum_user
    JOIN public.external_identities AS identity ON identity.user_id = forum_user.id
    JOIN current_session ON current_session.user_id = forum_user.id
    WHERE forum_user.id = sqlc.arg(user_id)
      AND identity.issuer = sqlc.arg(issuer)
      AND identity.subject = sqlc.arg(subject)
      AND forum_user.role = 'member'
      AND (
          forum_user.suspended_at IS NULL
          OR forum_user.suspended_at > sqlc.arg(at_time)::timestamptz
          OR forum_user.suspended_until <= sqlc.arg(at_time)::timestamptz
      )
    FOR UPDATE OF forum_user, identity
),
updated AS (
    UPDATE public.users AS forum_user
    SET role = 'administrator',
        updated_at = sqlc.arg(at_time)::timestamptz
    FROM target
    WHERE forum_user.id = target.id
    RETURNING forum_user.id
),
revoked AS (
    UPDATE public.sessions AS session
    SET revoked_at = sqlc.arg(at_time)::timestamptz
    FROM current_session, updated
    WHERE session.id = current_session.id
      AND current_session.user_id = updated.id
    RETURNING session.id
),
audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind,
        actor_user_id,
        target_type,
        target_user_id,
        action_type,
        previous_state,
        resulting_state,
        request_id,
        created_at
    )
    SELECT
        'forum_user',
        updated.id,
        'user',
        updated.id,
        'bootstrap_administrator',
        jsonb_build_object('role', target.previous_role),
        jsonb_build_object('role', 'administrator'),
        sqlc.arg(request_id),
        sqlc.arg(at_time)::timestamptz
    FROM updated
    JOIN target ON target.id = updated.id
    JOIN revoked ON true
    RETURNING id, target_user_id
)
SELECT updated.id AS user_id, audit.id AS audit_id, revoked.id AS revoked_session_id
FROM updated
JOIN audit ON audit.target_user_id = updated.id
JOIN revoked ON true;
