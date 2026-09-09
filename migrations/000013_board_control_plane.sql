-- Lock risk: this stopped-writer migration adds and validates checks on
-- site_settings and users, backfills every users row, and replaces checks on
-- moderation_actions under ACCESS EXCLUSIVE locks. Apply only after the
-- release preflight has proved every local external identity belongs to the
-- dedicated Authentik accepted group.
-- Rewrite risk: the users sync-state backfill writes every existing account.
-- The new control relations contain no rows on upgrade. Measure row counts,
-- WAL growth, lock time, and total migration time on representative data.

ALTER TABLE public.site_settings
    ADD COLUMN registration_mode text NOT NULL DEFAULT 'closed',
    ADD COLUMN maintenance_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN maintenance_message text NOT NULL DEFAULT '',
    ADD COLUMN publish_rate_limit integer NOT NULL DEFAULT 10,
    ADD COLUMN new_account_publish_rate_limit integer NOT NULL DEFAULT 3,
    ADD COLUMN publish_window_seconds integer NOT NULL DEFAULT 600,
    ADD COLUMN new_account_period_seconds integer NOT NULL DEFAULT 86400,
    ADD COLUMN session_idle_seconds integer NOT NULL DEFAULT 28800,
    ADD COLUMN auth_revalidate_seconds integer NOT NULL DEFAULT 1800,
    ADD CONSTRAINT site_settings_registration_mode_closed CHECK (
        registration_mode IN (
            'closed', 'verified_email_open',
            'administrator_approval', 'invitation_only'
        )
    ),
    ADD CONSTRAINT site_settings_maintenance_message_shape CHECK (
        char_length(maintenance_message) <= 280
        AND maintenance_message !~ '[[:cntrl:]]'
    ),
    ADD CONSTRAINT site_settings_publication_limits_positive CHECK (
        publish_rate_limit BETWEEN 1 AND 100000
        AND new_account_publish_rate_limit BETWEEN 1 AND publish_rate_limit
        AND publish_window_seconds BETWEEN 1 AND 86400
        AND new_account_period_seconds BETWEEN 60 AND 2592000
    ),
    ADD CONSTRAINT site_settings_session_limits_positive CHECK (
        session_idle_seconds > 0
        AND auth_revalidate_seconds > 0
    );

ALTER TABLE public.users
    ADD COLUMN authentik_sync_state text NOT NULL DEFAULT 'unknown',
    ADD COLUMN authentik_sync_last_attempt_at timestamp with time zone,
    ADD COLUMN authentik_sync_next_attempt_at timestamp with time zone,
    ADD COLUMN authentik_sync_failure_class text,
    ADD CONSTRAINT users_authentik_sync_state_closed CHECK (
        authentik_sync_state IN (
            'unknown', 'accepted', 'removal_required',
            'grant_required', 'suspended'
        )
    ),
    ADD CONSTRAINT users_authentik_sync_attempts_finite CHECK (
        (authentik_sync_last_attempt_at IS NULL OR pg_catalog.isfinite(authentik_sync_last_attempt_at))
        AND (authentik_sync_next_attempt_at IS NULL OR pg_catalog.isfinite(authentik_sync_next_attempt_at))
    ),
    ADD CONSTRAINT users_authentik_sync_failure_shape CHECK (
        authentik_sync_failure_class IS NULL
        OR (
            octet_length(authentik_sync_failure_class) BETWEEN 1 AND 128
            AND authentik_sync_failure_class !~ '[[:cntrl:]]'
            AND authentik_sync_failure_class = pg_catalog.btrim(authentik_sync_failure_class, ' ')
        )
    );

UPDATE public.users
SET authentik_sync_state = CASE
    WHEN suspended_at IS NOT NULL
     AND suspended_at <= clock_timestamp()
     AND (suspended_until IS NULL OR suspended_until > clock_timestamp())
    THEN 'removal_required'
    ELSE 'accepted'
END;

-- Runtime must compare opaque session credentials without receiving SELECT on
-- sessions.token_hash. These fixed-shape functions retain that comparison at
-- the migration-owner boundary; callers can neither enumerate hashes nor
-- substitute a relation or operation.
CREATE FUNCTION public.session_id_for_token(p_token_hash bytea)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT session.id
    FROM public.sessions AS session
    WHERE session.token_hash = p_token_hash
$$;

CREATE FUNCTION public.revoke_session_by_token(
    p_token_hash bytea,
    p_observed_at timestamp with time zone
)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    WITH revoked AS (
        UPDATE public.sessions
        SET revoked_at = p_observed_at
        WHERE token_hash = p_token_hash
          AND revoked_at IS NULL
          AND issued_at <= p_observed_at
        RETURNING 1
    )
    SELECT count(*)::bigint FROM revoked
$$;

CREATE FUNCTION public.revoke_session_for_rotation_by_token(
    p_session_id bigint,
    p_token_hash bytea,
    p_observed_at timestamp with time zone
)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    WITH revoked AS (
        UPDATE public.sessions
        SET revoked_at = p_observed_at
        WHERE id = p_session_id
          AND token_hash = p_token_hash
          AND revoked_at IS NULL
          AND issued_at <= p_observed_at
          AND expires_at > p_observed_at
        RETURNING 1
    )
    SELECT count(*)::bigint FROM revoked
$$;

REVOKE ALL ON FUNCTION public.session_id_for_token(bytea) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.revoke_session_by_token(
    bytea, timestamp with time zone
) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.revoke_session_for_rotation_by_token(
    bigint, bytea, timestamp with time zone
) FROM PUBLIC;

CREATE TABLE public.pending_registrations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    authentik_user_id bigint NOT NULL UNIQUE,
    authentik_subject uuid NOT NULL UNIQUE,
    display_name text NOT NULL,
    verified_email text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    administration_revision bigint NOT NULL DEFAULT 1,
    intake_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    decided_at timestamp with time zone,
    deciding_administrator_id bigint REFERENCES public.users (id) ON DELETE RESTRICT,
    transition_request_id uuid,
    reconciliation_class text,
    CONSTRAINT pending_registrations_authentik_user_positive CHECK (
        authentik_user_id > 0
    ),
    CONSTRAINT pending_registrations_display_name_shape CHECK (
        char_length(display_name) BETWEEN 1 AND 80
        AND display_name !~ '[[:cntrl:]]'
    ),
    CONSTRAINT pending_registrations_verified_email_shape CHECK (
        char_length(verified_email) BETWEEN 3 AND 320
        AND verified_email !~ '[[:cntrl:]]'
    ),
    CONSTRAINT pending_registrations_status_closed CHECK (
        status IN (
            'pending', 'approval_required', 'rejection_required',
            'approved', 'rejected'
        )
    ),
    CONSTRAINT pending_registrations_revision_positive CHECK (
        administration_revision > 0
    ),
    CONSTRAINT pending_registrations_times_finite CHECK (
        pg_catalog.isfinite(intake_at)
        AND (decided_at IS NULL OR pg_catalog.isfinite(decided_at))
        AND (decided_at IS NULL OR decided_at >= intake_at)
    ),
    CONSTRAINT pending_registrations_decision_consistent CHECK (
        (
            status IN ('pending', 'approval_required', 'rejection_required')
            AND decided_at IS NULL
            AND deciding_administrator_id IS NULL
        )
        OR (
            status IN ('approved', 'rejected')
            AND decided_at IS NOT NULL
            AND deciding_administrator_id IS NOT NULL
        )
    ),
    CONSTRAINT pending_registrations_reconciliation_shape CHECK (
        reconciliation_class IS NULL
        OR (
            octet_length(reconciliation_class) BETWEEN 1 AND 128
            AND reconciliation_class !~ '[[:cntrl:]]'
            AND reconciliation_class = pg_catalog.btrim(reconciliation_class, ' ')
        )
    )
);

CREATE INDEX pending_registrations_queue_idx
    ON public.pending_registrations (status, intake_at, id)
    WHERE status IN ('pending', 'approval_required', 'rejection_required');

CREATE TABLE public.registration_invitations (
    idempotency_key uuid PRIMARY KEY,
    authentik_invitation_name text NOT NULL UNIQUE,
    transition_state text NOT NULL DEFAULT 'creating',
    delivery_state text NOT NULL DEFAULT 'not_requested',
    flow_identity text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    transitioned_at timestamp with time zone,
    created_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    administration_revision bigint NOT NULL DEFAULT 1,
    request_fingerprint bytea NOT NULL,
    failure_class text,
    CONSTRAINT registration_invitations_name_shape CHECK (
        octet_length(authentik_invitation_name) BETWEEN 1 AND 200
        AND authentik_invitation_name !~ '[[:cntrl:]]'
        AND authentik_invitation_name = pg_catalog.btrim(authentik_invitation_name, ' ')
    ),
    CONSTRAINT registration_invitations_transition_closed CHECK (
        transition_state IN (
            'creating', 'active', 'revoke_required',
            'revoked', 'absent', 'unknown'
        )
    ),
    CONSTRAINT registration_invitations_delivery_closed CHECK (
        delivery_state IN ('not_requested', 'queued', 'failed', 'unknown')
    ),
    CONSTRAINT registration_invitations_flow_shape CHECK (
        octet_length(flow_identity) BETWEEN 1 AND 200
        AND flow_identity !~ '[[:cntrl:]]'
        AND flow_identity = pg_catalog.btrim(flow_identity, ' ')
    ),
    CONSTRAINT registration_invitations_times_ordered CHECK (
        pg_catalog.isfinite(created_at)
        AND pg_catalog.isfinite(expires_at)
        AND expires_at > created_at
        AND (transitioned_at IS NULL OR (
            pg_catalog.isfinite(transitioned_at)
            AND transitioned_at >= created_at
        ))
    ),
    CONSTRAINT registration_invitations_revision_positive CHECK (
        administration_revision > 0
    ),
    CONSTRAINT registration_invitations_fingerprint_length CHECK (
        octet_length(request_fingerprint) = 32
    ),
    CONSTRAINT registration_invitations_failure_shape CHECK (
        failure_class IS NULL
        OR (
            octet_length(failure_class) BETWEEN 1 AND 128
            AND failure_class !~ '[[:cntrl:]]'
            AND failure_class = pg_catalog.btrim(failure_class, ' ')
        )
    )
);

CREATE INDEX registration_invitations_state_idx
    ON public.registration_invitations (transition_state, created_at, idempotency_key);

CREATE TABLE public.email_test_state (
    administrator_id bigint PRIMARY KEY REFERENCES public.users (id) ON DELETE RESTRICT,
    idempotency_key uuid NOT NULL UNIQUE,
    status text NOT NULL,
    requested_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    next_allowed_at timestamp with time zone NOT NULL,
    CONSTRAINT email_test_state_status_closed CHECK (
        status IN ('requested', 'accepted', 'failed', 'unknown')
    ),
    CONSTRAINT email_test_state_times_ordered CHECK (
        pg_catalog.isfinite(requested_at)
        AND pg_catalog.isfinite(next_allowed_at)
        AND next_allowed_at = requested_at + interval '5 minutes'
        AND (completed_at IS NULL OR (
            pg_catalog.isfinite(completed_at)
            AND completed_at >= requested_at
        ))
    ),
    CONSTRAINT email_test_state_completion_consistent CHECK (
        (status = 'requested' AND completed_at IS NULL)
        OR (status IN ('accepted', 'failed', 'unknown') AND completed_at IS NOT NULL)
    )
);

ALTER TABLE public.moderation_actions
    DROP CONSTRAINT moderation_actions_action_type_closed,
    ADD CONSTRAINT moderation_actions_action_type_closed CHECK (
        action_type IN (
            'bootstrap_administrator', 'rebind_external_identity', 'change_role',
            'grant_group_membership', 'revoke_group_membership',
            'create_group', 'rename_group',
            'create_area', 'update_area', 'change_area_visibility', 'change_area_posting_mode',
            'grant_area_group', 'revoke_area_group',
            'lock_topic', 'unlock_topic', 'hide_topic', 'restore_topic',
            'pin_topic', 'unpin_topic', 'move_topic', 'archive_topic',
            'hide_post', 'restore_post', 'redact_post',
            'warn_user', 'mute_user', 'suspend_user', 'reinstate_user',
            'assign_report', 'note_report', 'resolve_report', 'dismiss_report',
            'update_site_settings', 'update_control_settings',
            'request_registration_approval', 'approve_registration',
            'request_registration_rejection', 'reject_registration',
            'record_registration_transition_result', 'adopt_pending_registration',
            'request_create_invitation', 'create_invitation',
            'request_revoke_invitation', 'revoke_invitation',
            'record_invitation_absent', 'record_invitation_result',
            'request_identity_reinstatement', 'request_identity_reconciliation',
            'reconcile_identity_access', 'revoke_session', 'revoke_user_sessions',
            'request_test_email', 'test_email'
        )
    ),
    DROP CONSTRAINT moderation_actions_reason_required,
    ADD CONSTRAINT moderation_actions_reason_required CHECK (
        action_type NOT IN (
            'change_role',
            'grant_group_membership', 'revoke_group_membership',
            'create_group', 'rename_group',
            'create_area', 'update_area',
            'grant_area_group', 'revoke_area_group',
            'move_topic', 'hide_topic', 'archive_topic', 'hide_post', 'redact_post',
            'warn_user', 'mute_user', 'suspend_user', 'reinstate_user',
            'resolve_report', 'dismiss_report',
            'update_site_settings', 'update_control_settings',
            'request_registration_approval', 'approve_registration',
            'request_registration_rejection', 'reject_registration',
            'record_registration_transition_result', 'adopt_pending_registration',
            'request_create_invitation', 'create_invitation',
            'request_revoke_invitation', 'revoke_invitation',
            'record_invitation_absent', 'record_invitation_result',
            'request_identity_reinstatement', 'request_identity_reconciliation',
            'reconcile_identity_access', 'revoke_session', 'revoke_user_sessions',
            'request_test_email', 'test_email'
        )
        OR reason IS NOT NULL
    ),
    ADD CONSTRAINT moderation_actions_expiry_actor_consistent CHECK (
        actor_kind <> 'operator'
        OR action_type <> 'reconcile_identity_access'
        OR operator_identifier = 'gotth-bb-expiry-reconciler'
    );
