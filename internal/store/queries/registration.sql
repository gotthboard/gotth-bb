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

-- name: UpsertPendingRegistrationIntake :one
WITH admitted AS (
    INSERT INTO public.pending_registrations (
        authentik_user_id, authentik_subject, display_name, verified_email,
        status, administration_revision, intake_at
    )
    VALUES (
        sqlc.arg(authentik_user_id), sqlc.arg(authentik_subject),
        sqlc.arg(display_name), sqlc.arg(verified_email),
        'pending', 1, sqlc.arg(intake_at)
    )
    ON CONFLICT (authentik_subject) DO UPDATE
    SET display_name = EXCLUDED.display_name,
        verified_email = EXCLUDED.verified_email
    WHERE pending_registrations.authentik_user_id = EXCLUDED.authentik_user_id
      AND pending_registrations.status = 'pending'
    RETURNING true AS accepted
)
SELECT COALESCE(
    (SELECT accepted FROM admitted),
    EXISTS (
        SELECT 1
        FROM public.pending_registrations
        WHERE pending_registrations.authentik_subject = sqlc.arg(authentik_subject)
          AND pending_registrations.authentik_user_id = sqlc.arg(authentik_user_id)
    )
)::boolean AS accepted;

-- name: ListPendingRegistrationsForAdministration :many
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
), candidate AS MATERIALIZED (
    SELECT registration.id, registration.display_name,
           registration.verified_email, registration.status,
           registration.administration_revision, registration.intake_at,
           registration.reconciliation_class
    FROM actor
    JOIN LATERAL (
        SELECT pending.id, pending.display_name, pending.verified_email,
               pending.status, pending.administration_revision,
               pending.intake_at, pending.reconciliation_class
        FROM public.pending_registrations AS pending
        WHERE pending.status IN ('pending', 'approval_required', 'rejection_required')
          AND pending.id > sqlc.arg(after_registration_id)
        ORDER BY pending.id
        LIMIT sqlc.arg(page_limit)
    ) AS registration ON true
)
SELECT (candidate.id IS NOT NULL)::boolean AS registration_present,
       COALESCE(candidate.id, 0)::bigint AS id,
       COALESCE(candidate.display_name, '')::text AS display_name,
       COALESCE(candidate.verified_email, '')::text AS verified_email,
       COALESCE(candidate.status, '')::text AS status,
       COALESCE(candidate.administration_revision, 0)::bigint AS administration_revision,
       candidate.intake_at,
       candidate.reconciliation_class
FROM actor
LEFT JOIN candidate ON true
ORDER BY candidate.id NULLS LAST;

-- name: MatchPendingRegistrationCoordinates :many
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
), candidate AS MATERIALIZED (
    SELECT pending.id, pending.authentik_user_id,
           pending.authentik_subject::text AS authentik_subject
    FROM actor
    CROSS JOIN public.pending_registrations AS pending
    WHERE pending.authentik_user_id = ANY(sqlc.arg(authentik_user_ids)::bigint[])
       OR pending.authentik_subject::text = ANY(sqlc.arg(authentik_subjects)::text[])
    ORDER BY pending.id
    LIMIT 103
)
SELECT EXISTS (SELECT 1 FROM actor)::boolean AS actor_present,
       (candidate.id IS NOT NULL)::boolean AS registration_present,
       COALESCE(candidate.id, 0)::bigint AS id,
       COALESCE(candidate.authentik_user_id, 0)::bigint AS authentik_user_id,
       COALESCE(candidate.authentik_subject, '')::text AS authentik_subject
FROM (SELECT 1) AS seed
LEFT JOIN candidate ON true
ORDER BY candidate.id NULLS LAST;

-- name: InsertOrLoadPendingRegistrationAdoption :one
WITH inserted AS (
    INSERT INTO public.pending_registrations (
        authentik_user_id, authentik_subject, display_name, verified_email,
        status, administration_revision, intake_at
    ) VALUES (
        sqlc.arg(authentik_user_id), sqlc.arg(authentik_subject),
        sqlc.arg(display_name), sqlc.arg(verified_email),
        'pending', 1, sqlc.arg(intake_at)
    )
    ON CONFLICT DO NOTHING
    RETURNING id, administration_revision, true AS inserted
), existing AS (
    SELECT pending.id, pending.administration_revision, false AS inserted
    FROM public.pending_registrations AS pending
    WHERE pending.authentik_user_id = sqlc.arg(authentik_user_id)
      AND pending.authentik_subject = sqlc.arg(authentik_subject)
      AND pending.status = 'pending'
)
SELECT id, administration_revision, inserted FROM inserted
UNION ALL
SELECT id, administration_revision, inserted FROM existing
LIMIT 1;

-- name: RecordPendingRegistrationAdoption :one
INSERT INTO public.moderation_actions (
    actor_kind, actor_user_id, target_type, target_site, action_type,
    reason, previous_state, resulting_state, request_id, created_at
) VALUES (
    'forum_user', sqlc.arg(actor_user_id), 'site', true,
    'adopt_pending_registration', sqlc.arg(reason),
    pg_catalog.jsonb_build_object(
        'registration_ref', sqlc.arg(registration_ref)::text,
        'status', 'remote_pending_orphan'::text
    ),
    pg_catalog.jsonb_build_object(
        'registration_ref', sqlc.arg(registration_ref)::text,
        'status', 'pending'::text,
        'administration_revision', sqlc.arg(administration_revision)::bigint
    ),
    sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
)
RETURNING id;

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

-- name: ReserveRegistrationInvitation :one
WITH inserted AS (
    INSERT INTO public.registration_invitations (
        idempotency_key, authentik_invitation_name, transition_state,
        delivery_state, flow_identity, expires_at, created_at, created_by,
        administration_revision, request_fingerprint
    ) VALUES (
        sqlc.arg(idempotency_key), sqlc.arg(invitation_name), 'creating',
        'not_requested', sqlc.arg(flow_identity), sqlc.arg(expires_at),
        sqlc.arg(observed_at), sqlc.arg(actor_user_id), 1,
        sqlc.arg(request_fingerprint)
    )
    ON CONFLICT (idempotency_key) DO NOTHING
    RETURNING idempotency_key, authentik_invitation_name, transition_state,
              delivery_state, flow_identity, expires_at,
              administration_revision, request_fingerprint, true AS inserted
), existing AS (
    SELECT invitation.idempotency_key, invitation.authentik_invitation_name,
           invitation.transition_state, invitation.delivery_state,
           invitation.flow_identity, invitation.expires_at,
           invitation.administration_revision, invitation.request_fingerprint,
           false AS inserted
    FROM public.registration_invitations AS invitation
    WHERE invitation.idempotency_key = sqlc.arg(idempotency_key)
      AND NOT EXISTS (SELECT 1 FROM inserted)
    FOR UPDATE OF invitation
)
SELECT * FROM inserted
UNION ALL
SELECT * FROM existing
LIMIT 1;

-- name: RecordRegistrationInvitationRequest :one
INSERT INTO public.moderation_actions (
    actor_kind, actor_user_id, target_type, target_site, action_type,
    reason, previous_state, resulting_state, request_id, created_at
) VALUES (
    'forum_user', sqlc.arg(actor_user_id), 'site', true,
    'request_create_invitation', sqlc.arg(reason),
    pg_catalog.jsonb_build_object('invitation_ref', sqlc.arg(invitation_ref)::text, 'status', 'absent'::text),
    pg_catalog.jsonb_build_object('invitation_ref', sqlc.arg(invitation_ref)::text, 'status', 'creating'::text, 'administration_revision', 1::bigint),
    sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
)
RETURNING id;

-- name: CompleteRegistrationInvitation :one
WITH changed AS (
    UPDATE public.registration_invitations AS invitation
    SET transition_state = 'active',
        delivery_state = sqlc.arg(delivery_state),
        transitioned_at = sqlc.arg(observed_at),
        administration_revision = invitation.administration_revision + 1,
        failure_class = NULL
    WHERE invitation.idempotency_key = sqlc.arg(idempotency_key)
      AND invitation.transition_state IN ('creating', 'unknown')
      AND invitation.administration_revision = sqlc.arg(expected_revision)
      AND invitation.administration_revision < 9223372036854775807
    RETURNING invitation.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_site, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'site', true,
           'create_invitation', sqlc.arg(reason),
           pg_catalog.jsonb_build_object('invitation_ref', sqlc.arg(invitation_ref)::text, 'status', sqlc.arg(previous_state)::text, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           pg_catalog.jsonb_build_object('invitation_ref', sqlc.arg(invitation_ref)::text, 'status', 'active'::text, 'administration_revision', changed.administration_revision, 'delivery', sqlc.arg(delivery_state)::text, 'result', sqlc.arg(result_class)::text),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed RETURNING id
)
SELECT changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON true;

-- name: RecordRegistrationInvitationFailure :one
WITH changed AS (
    UPDATE public.registration_invitations AS invitation
    SET transition_state = 'unknown',
        delivery_state = sqlc.arg(delivery_state),
        transitioned_at = sqlc.arg(observed_at),
        administration_revision = invitation.administration_revision + 1,
        failure_class = sqlc.arg(failure_class)
    WHERE invitation.idempotency_key = sqlc.arg(idempotency_key)
      AND invitation.transition_state IN ('creating', 'unknown')
      AND invitation.administration_revision = sqlc.arg(expected_revision)
      AND invitation.administration_revision < 9223372036854775807
    RETURNING invitation.administration_revision
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_site, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'site', true,
           'record_invitation_result', sqlc.arg(reason),
           pg_catalog.jsonb_build_object('invitation_ref', sqlc.arg(invitation_ref)::text, 'status', sqlc.arg(previous_state)::text, 'administration_revision', sqlc.arg(expected_revision)::bigint),
           pg_catalog.jsonb_build_object('invitation_ref', sqlc.arg(invitation_ref)::text, 'status', 'unknown'::text, 'administration_revision', changed.administration_revision, 'delivery', sqlc.arg(delivery_state)::text, 'result', sqlc.arg(failure_class)::text),
           sqlc.arg(request_id), sqlc.arg(observed_at)::timestamptz
    FROM changed RETURNING id
)
SELECT changed.administration_revision, audit.id AS audit_id
FROM changed JOIN audit ON true;

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
