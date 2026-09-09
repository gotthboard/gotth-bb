-- Apply as the schema owner after migrations with psql's runtime_role variable:
-- psql --set=ON_ERROR_STOP=1 --set=runtime_role=gotth_bb_runtime \
--   --file=deploy/postgresql/runtime-grants.sql "$DATABASE_URL"
--
-- This file is the complete runtime privilege contract, not a migration
-- delta. Logical archives deliberately contain no privileges, and an existing
-- deployment may retain broader grants from an older release. Reset both
-- cases to the same closed Beta.1 boundary before granting anything back.
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"runtime_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"runtime_role";
REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public FROM :"runtime_role";

GRANT USAGE ON SCHEMA public TO :"runtime_role";

GRANT SELECT ON TABLE
    public.area_groups,
    public.areas,
    public.content_renderer_state,
    public.external_identities,
    public.forum_group_members,
    public.forum_groups,
    public.gotth_schema_migrations,
    public.governance_state,
    public.moderation_actions,
    public.oidc_login_attempts,
    public.pending_registrations,
    public.posts,
    public.report_notes,
    public.reports,
    public.registration_invitations,
    public.search_projection_state,
    public.site_settings,
    public.topic_reads,
    public.topics,
    public.user_warnings,
    public.users,
    public.email_test_state
TO :"runtime_role";

GRANT SELECT (id, user_id, issued_at, last_seen_at, validated_at, expires_at,
              revoked_at, user_agent_hash, ip_prefix)
ON TABLE public.sessions
TO :"runtime_role";

GRANT EXECUTE ON FUNCTION
    public.session_id_for_token(bytea),
    public.revoke_session_by_token(bytea, timestamp with time zone),
    public.revoke_session_for_rotation_by_token(bigint, bytea, timestamp with time zone)
TO :"runtime_role";

GRANT USAGE, SELECT ON SEQUENCE
    public.areas_id_seq,
    public.forum_groups_id_seq,
    public.moderation_actions_id_seq,
    public.posts_id_seq,
    public.report_notes_id_seq,
    public.reports_id_seq,
    public.pending_registrations_id_seq,
    public.sessions_id_seq,
    public.topics_id_seq,
    public.user_warnings_id_seq,
    public.users_id_seq
TO :"runtime_role";

GRANT INSERT, UPDATE ON TABLE
    public.external_identities,
    public.oidc_login_attempts,
    public.topic_reads
TO :"runtime_role";

GRANT INSERT (token_hash, user_id, issued_at, last_seen_at, validated_at,
              expires_at, revoked_at, user_agent_hash, ip_prefix),
      UPDATE (last_seen_at, validated_at, revoked_at)
ON TABLE public.sessions
TO :"runtime_role";

GRANT INSERT, UPDATE ON TABLE
    public.areas,
    public.posts,
    public.reports,
    public.topics
TO :"runtime_role";

GRANT INSERT ON TABLE
    public.moderation_actions,
    public.report_notes,
    public.user_warnings
TO :"runtime_role";

-- B1-09 control records are updated in place through bounded state machines.
-- Immutable Authentik coordinates, request fingerprints, invitation identity,
-- creators, and primary keys receive no UPDATE authority. Runtime cannot
-- delete rows, so terminal and unknown outcomes remain inspectable.
GRANT INSERT (authentik_user_id, authentik_subject, display_name,
              verified_email, status, administration_revision, intake_at,
              decided_at, deciding_administrator_id, transition_request_id,
              reconciliation_class),
      UPDATE (display_name, verified_email, status,
              administration_revision, decided_at,
              deciding_administrator_id, transition_request_id,
              reconciliation_class)
ON TABLE public.pending_registrations
TO :"runtime_role";

GRANT INSERT (idempotency_key, authentik_invitation_name, transition_state,
              delivery_state, flow_identity, expires_at, created_at,
              transitioned_at, created_by, administration_revision,
              request_fingerprint, failure_class),
      UPDATE (transition_state, delivery_state, transitioned_at,
              administration_revision, failure_class)
ON TABLE public.registration_invitations
TO :"runtime_role";

GRANT INSERT (administrator_id, idempotency_key, status, requested_at,
              completed_at, next_allowed_at),
      UPDATE (idempotency_key, status, requested_at, completed_at,
              next_allowed_at)
ON TABLE public.email_test_state
TO :"runtime_role";

GRANT INSERT, DELETE ON TABLE public.area_groups TO :"runtime_role";

-- OIDC creates the local account and later updates only profile/login state.
-- Governance, moderation, administration, and publication add their named
-- columns; runtime never receives table-wide user UPDATE or any DELETE.
GRANT INSERT (display_name, email, avatar_url, created_at, updated_at,
              last_login_at, authentik_sync_state),
      UPDATE (display_name, email, avatar_url, role, suspended_at,
              suspended_until, suspension_reason, muted_until, updated_at,
              last_login_at, administration_revision,
              publication_window_started_at, publication_count,
              authentik_sync_state, authentik_sync_last_attempt_at,
              authentik_sync_next_attempt_at, authentik_sync_failure_class)
ON TABLE public.users
TO :"runtime_role";

-- PostgreSQL requires UPDATE privilege on at least one selected column for
-- SELECT ... FOR UPDATE. Restrict that privilege to the singleton key; the
-- primary key and CHECK constraint admit only the value true.
GRANT UPDATE (singleton)
ON TABLE public.governance_state
TO :"runtime_role";

-- AN-04 presentation state is one migration-owned singleton. Runtime may read
-- it and update only the bounded presentation tuple; it may not create,
-- delete, or change the singleton key.
GRANT UPDATE (site_name, site_description, brand_theme, rules_markdown,
              rules_html, rules_renderer_version, administration_revision,
              updated_at, registration_mode, maintenance_enabled,
              maintenance_message, publish_rate_limit,
              new_account_publish_rate_limit, publish_window_seconds,
              new_account_period_seconds, session_idle_seconds,
              auth_revalidate_seconds)
ON TABLE public.site_settings
TO :"runtime_role";

-- Local group governance creates and renames groups without granting table-
-- wide UPDATE or DELETE.
GRANT INSERT (name, created_by, created_at, updated_at),
      UPDATE (name, updated_at, administration_revision)
ON TABLE public.forum_groups
TO :"runtime_role";

-- Membership changes are one mapping per audited request. There is no mapping
-- UPDATE authority.
GRANT INSERT (group_id, user_id, granted_by, created_at),
      DELETE
ON TABLE public.forum_group_members
TO :"runtime_role";
