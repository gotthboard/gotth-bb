-- B1-09-06 replaces the host-owned SMTP tuple with one Board-owned singleton.
-- The migration seeds only the disabled state. Registration must remain closed
-- while an administrator first configures and verifies delivery.

CREATE TABLE public.smtp_settings (
    singleton boolean PRIMARY KEY DEFAULT true,
    host text NOT NULL DEFAULT '',
    port integer NOT NULL DEFAULT 0,
    username text NOT NULL DEFAULT '',
    from_address text NOT NULL DEFAULT '',
    tls_mode text NOT NULL DEFAULT '',
    timeout_seconds integer NOT NULL DEFAULT 0,
    password_envelope bytea,
    administration_revision bigint NOT NULL DEFAULT 1,
    verified_revision bigint,
    updated_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT smtp_settings_singleton_true CHECK (singleton),
    CONSTRAINT smtp_settings_revision_positive CHECK (administration_revision > 0),
    CONSTRAINT smtp_settings_verified_current CHECK (
        verified_revision IS NULL OR verified_revision = administration_revision
    ),
    CONSTRAINT smtp_settings_updated_at_finite CHECK (pg_catalog.isfinite(updated_at)),
    CONSTRAINT smtp_settings_state_closed CHECK (
        (
            host = '' AND port = 0 AND username = '' AND from_address = ''
            AND tls_mode = '' AND timeout_seconds = 0
            AND password_envelope IS NULL AND verified_revision IS NULL
        )
        OR (
            octet_length(host) BETWEEN 1 AND 253
            AND host = pg_catalog.lower(host)
            AND host !~ '[[:cntrl:]]'
            AND port BETWEEN 1 AND 65535
            AND octet_length(username) <= 320
            AND username !~ '[[:cntrl:]]'
            AND username = pg_catalog.btrim(username, ' ')
            AND octet_length(from_address) BETWEEN 3 AND 320
            AND from_address !~ '[[:cntrl:]]'
            AND from_address = pg_catalog.btrim(from_address, ' ')
            AND tls_mode IN ('starttls', 'implicit_tls')
            AND timeout_seconds BETWEEN 1 AND 30
            AND ((username = '' AND password_envelope IS NULL)
                 OR (username <> '' AND octet_length(password_envelope) BETWEEN 30 AND 8192))
        )
    )
);

INSERT INTO public.smtp_settings (singleton) VALUES (true);

ALTER TABLE public.email_test_state
    ADD COLUMN smtp_revision bigint,
    ADD CONSTRAINT email_test_state_smtp_revision_positive CHECK (
        smtp_revision IS NULL OR smtp_revision > 0
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
            'update_site_settings', 'update_control_settings', 'update_smtp_settings',
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
            'update_site_settings', 'update_control_settings', 'update_smtp_settings',
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
    );
