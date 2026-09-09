-- name: LockRegistrationAdministrator :one
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

-- name: LockPendingRegistration :one
SELECT id, authentik_user_id, authentik_subject, status,
       administration_revision, transition_request_id,
       reconciliation_class
FROM public.pending_registrations
WHERE id = sqlc.arg(registration_id)
FOR UPDATE;

-- name: BeginPendingRegistrationDecision :one
WITH changed AS (
    UPDATE public.pending_registrations AS registration
    SET status = sqlc.arg(required_status),
        administration_revision = registration.administration_revision + 1,
        transition_request_id = sqlc.arg(request_id),
        reconciliation_class = NULL
    WHERE registration.id = sqlc.arg(registration_id)
      AND registration.status = 'pending'
      AND registration.administration_revision = sqlc.arg(expected_revision)
      AND registration.administration_revision < 9223372036854775807
    RETURNING registration.id, registration.status,
              registration.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_site, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'site', true,
           sqlc.arg(action_type), sqlc.arg(reason),
           pg_catalog.jsonb_build_object(
               'registration_ref', sqlc.arg(registration_ref)::text,
               'status', 'pending'::text,
               'administration_revision', sqlc.arg(expected_revision)::bigint
           ),
           pg_catalog.jsonb_build_object(
               'registration_ref', sqlc.arg(registration_ref)::text,
               'status', changed.status,
               'administration_revision', changed.administration_revision
           ),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed
    RETURNING id
)
SELECT changed.status, changed.administration_revision, audit.id AS audit_id
FROM changed
JOIN audit ON true;

-- name: CompletePendingRegistrationDecision :one
WITH changed AS (
    UPDATE public.pending_registrations AS registration
    SET status = sqlc.arg(resulting_status),
        administration_revision = registration.administration_revision + 1,
        decided_at = sqlc.arg(observed_at),
        deciding_administrator_id = sqlc.arg(actor_user_id),
        reconciliation_class = NULL
    WHERE registration.id = sqlc.arg(registration_id)
      AND registration.status = sqlc.arg(required_status)
      AND registration.transition_request_id = sqlc.arg(request_id)
      AND registration.administration_revision = sqlc.arg(expected_revision)
      AND registration.administration_revision < 9223372036854775807
    RETURNING registration.id, registration.status,
              registration.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_site, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'site', true,
           sqlc.arg(action_type), sqlc.arg(reason),
           pg_catalog.jsonb_build_object(
               'registration_ref', sqlc.arg(registration_ref)::text,
               'status', sqlc.arg(required_status)::text,
               'administration_revision', sqlc.arg(expected_revision)::bigint
           ),
           pg_catalog.jsonb_build_object(
               'registration_ref', sqlc.arg(registration_ref)::text,
               'status', changed.status,
               'administration_revision', changed.administration_revision,
               'result', 'confirmed'::text
           ),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed
    RETURNING id
)
SELECT changed.status, changed.administration_revision, audit.id AS audit_id
FROM changed
JOIN audit ON true;

-- name: RecordPendingRegistrationDecisionFailure :one
WITH changed AS (
    UPDATE public.pending_registrations AS registration
    SET reconciliation_class = sqlc.arg(failure_class),
        administration_revision = registration.administration_revision + 1
    WHERE registration.id = sqlc.arg(registration_id)
      AND registration.status = sqlc.arg(required_status)
      AND registration.transition_request_id = sqlc.arg(request_id)
      AND registration.administration_revision = sqlc.arg(expected_revision)
      AND registration.administration_revision < 9223372036854775807
    RETURNING registration.status, registration.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_site, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'site', true,
           'record_registration_transition_result', sqlc.arg(reason),
           pg_catalog.jsonb_build_object(
               'registration_ref', sqlc.arg(registration_ref)::text,
               'status', sqlc.arg(required_status)::text,
               'administration_revision', sqlc.arg(expected_revision)::bigint
           ),
           pg_catalog.jsonb_build_object(
               'registration_ref', sqlc.arg(registration_ref)::text,
               'status', changed.status,
               'administration_revision', changed.administration_revision,
               'result', sqlc.arg(failure_class)::text
           ),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed
    RETURNING id
)
SELECT changed.status, changed.administration_revision, audit.id AS audit_id
FROM changed
JOIN audit ON true;
