-- name: LoadSuspendedIdentityTarget :one
SELECT identity.subject,
       forum_user.authentik_sync_state,
       forum_user.administration_revision
FROM public.users AS forum_user
JOIN public.external_identities AS identity ON identity.user_id = forum_user.id
WHERE forum_user.id = sqlc.arg(user_id)
  AND forum_user.administration_revision = sqlc.arg(expected_revision)
  AND forum_user.authentik_sync_state = 'removal_required'
  AND forum_user.suspended_at <= sqlc.arg(observed_at)::timestamptz
  AND (forum_user.suspended_until IS NULL OR forum_user.suspended_until > sqlc.arg(observed_at)::timestamptz);

-- name: CompleteIdentityRemoval :one
WITH changed AS (
    UPDATE public.users AS forum_user
    SET authentik_sync_state = 'suspended',
        authentik_sync_last_attempt_at = sqlc.arg(observed_at),
        authentik_sync_next_attempt_at = NULL,
        authentik_sync_failure_class = NULL,
        administration_revision = forum_user.administration_revision + 1
    WHERE forum_user.id = sqlc.arg(user_id)
      AND forum_user.authentik_sync_state = 'removal_required'
      AND forum_user.administration_revision = sqlc.arg(expected_revision)
      AND forum_user.suspended_at <= sqlc.arg(observed_at)::timestamptz
      AND (forum_user.suspended_until IS NULL OR forum_user.suspended_until > sqlc.arg(observed_at)::timestamptz)
      AND forum_user.administration_revision < 9223372036854775807
    RETURNING forum_user.id, forum_user.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'reconcile_identity_access', sqlc.arg(reason),
           pg_catalog.jsonb_build_object('sync_state', 'removal_required'::text, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           pg_catalog.jsonb_build_object('sync_state', 'suspended'::text, 'administration_revision', changed.administration_revision, 'result', 'confirmed'::text),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed RETURNING id
)
SELECT changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON true;

-- name: RecordIdentityReconciliationFailure :one
WITH changed AS (
    UPDATE public.users AS forum_user
    SET authentik_sync_last_attempt_at = sqlc.arg(observed_at),
        authentik_sync_next_attempt_at = sqlc.arg(next_attempt_at),
        authentik_sync_failure_class = sqlc.arg(failure_class),
        administration_revision = forum_user.administration_revision + 1
    WHERE forum_user.id = sqlc.arg(user_id)
      AND forum_user.authentik_sync_state = sqlc.arg(required_state)
      AND forum_user.administration_revision = sqlc.arg(expected_revision)
      AND forum_user.administration_revision < 9223372036854775807
    RETURNING forum_user.id, forum_user.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'reconcile_identity_access', sqlc.arg(reason),
           pg_catalog.jsonb_build_object('sync_state', sqlc.arg(required_state)::text, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           pg_catalog.jsonb_build_object('sync_state', sqlc.arg(required_state)::text, 'administration_revision', changed.administration_revision, 'result', sqlc.arg(failure_class)::text),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed RETURNING id
)
SELECT changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON true;

-- name: BeginIdentityReinstatement :one
WITH actor AS MATERIALIZED (
    SELECT forum_user.id, forum_user.role
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = sqlc.arg(actor_role)
      AND forum_user.authentik_sync_state = 'accepted'
      AND (forum_user.suspended_at IS NULL OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz)
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), target AS MATERIALIZED (
    SELECT forum_user.id, forum_user.role, forum_user.administration_revision,
           forum_user.authentik_sync_state, identity.subject
    FROM public.users AS forum_user
    JOIN public.external_identities AS identity ON identity.user_id = forum_user.id
    JOIN actor ON actor.id <> forum_user.id
              AND (actor.role = 'administrator' OR actor.role = 'moderator' AND forum_user.role = 'member')
    WHERE forum_user.id = sqlc.arg(user_id)
      AND forum_user.suspended_at <= sqlc.arg(observed_at)::timestamptz
      AND (forum_user.suspended_until IS NULL OR forum_user.suspended_until > sqlc.arg(observed_at)::timestamptz)
      AND forum_user.administration_revision < 9223372036854775807
    FOR UPDATE OF forum_user
), changed AS (
    UPDATE public.users AS forum_user
    SET authentik_sync_state = 'grant_required',
        authentik_sync_last_attempt_at = sqlc.arg(observed_at),
        authentik_sync_next_attempt_at = sqlc.arg(observed_at),
        authentik_sync_failure_class = NULL,
        administration_revision = forum_user.administration_revision + 1
    FROM target
    WHERE forum_user.id = target.id
    RETURNING forum_user.id, forum_user.administration_revision,
              target.authentik_sync_state AS previous_sync_state,
              target.subject
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'request_identity_reinstatement', sqlc.arg(reason),
           pg_catalog.jsonb_build_object('sync_state', changed.previous_sync_state, 'administration_revision', changed.administration_revision - 1),
           pg_catalog.jsonb_build_object('sync_state', 'grant_required'::text, 'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed RETURNING id
)
SELECT changed.subject, changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON true;

-- name: CompleteIdentityReinstatement :one
WITH actor AS MATERIALIZED (
    SELECT forum_user.id, forum_user.role
    FROM public.users AS forum_user
    WHERE forum_user.id = sqlc.arg(actor_user_id)
      AND forum_user.role = sqlc.arg(actor_role)
      AND forum_user.authentik_sync_state = 'accepted'
      AND (forum_user.suspended_at IS NULL OR forum_user.suspended_at > sqlc.arg(observed_at)::timestamptz OR forum_user.suspended_until <= sqlc.arg(observed_at)::timestamptz)
      AND (forum_user.muted_until IS NULL OR forum_user.muted_until <= sqlc.arg(observed_at)::timestamptz)
), changed AS (
    UPDATE public.users AS forum_user
    SET suspended_at = NULL, suspended_until = NULL, suspension_reason = NULL,
        authentik_sync_state = 'accepted',
        authentik_sync_last_attempt_at = sqlc.arg(observed_at),
        authentik_sync_next_attempt_at = NULL,
        authentik_sync_failure_class = NULL,
        updated_at = GREATEST(sqlc.arg(observed_at)::timestamptz, forum_user.updated_at),
        administration_revision = forum_user.administration_revision + 1
    FROM actor
    WHERE forum_user.id = sqlc.arg(user_id)
      AND actor.id <> forum_user.id
      AND (actor.role = 'administrator' OR actor.role = 'moderator' AND forum_user.role = 'member')
      AND forum_user.authentik_sync_state = 'grant_required'
      AND forum_user.administration_revision = sqlc.arg(expected_revision)
      AND forum_user.suspended_at <= sqlc.arg(observed_at)::timestamptz
      AND (forum_user.suspended_until IS NULL OR forum_user.suspended_until > sqlc.arg(observed_at)::timestamptz)
      AND forum_user.administration_revision < 9223372036854775807
    RETURNING forum_user.id, forum_user.administration_revision
), reconciliation_audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'reconcile_identity_access', sqlc.arg(reason),
           pg_catalog.jsonb_build_object('sync_state', 'grant_required'::text, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           pg_catalog.jsonb_build_object('sync_state', 'accepted'::text, 'administration_revision', changed.administration_revision, 'result', 'confirmed'::text),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed RETURNING id
), reinstatement_audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id,
           'reinstate_user', sqlc.arg(reason),
           pg_catalog.jsonb_build_object('suspended', true, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           pg_catalog.jsonb_build_object('suspended', false, 'administration_revision', changed.administration_revision),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed RETURNING id
)
SELECT changed.id AS user_id, changed.administration_revision,
       reconciliation_audit.id AS reconciliation_audit_id,
       reinstatement_audit.id AS reinstatement_audit_id
FROM changed JOIN reconciliation_audit ON true JOIN reinstatement_audit ON true;
