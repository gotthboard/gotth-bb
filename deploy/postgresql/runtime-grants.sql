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
    public.posts,
    public.report_notes,
    public.reports,
    public.search_projection_state,
    public.sessions,
    public.site_settings,
    public.topic_reads,
    public.topics,
    public.user_warnings,
    public.users
TO :"runtime_role";

GRANT USAGE, SELECT ON SEQUENCE
    public.areas_id_seq,
    public.forum_groups_id_seq,
    public.moderation_actions_id_seq,
    public.posts_id_seq,
    public.report_notes_id_seq,
    public.reports_id_seq,
    public.sessions_id_seq,
    public.topics_id_seq,
    public.user_warnings_id_seq,
    public.users_id_seq
TO :"runtime_role";

GRANT INSERT, UPDATE ON TABLE
    public.external_identities,
    public.oidc_login_attempts,
    public.sessions,
    public.topic_reads
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

GRANT INSERT, DELETE ON TABLE public.area_groups TO :"runtime_role";

-- OIDC creates the local account and later updates only profile/login state.
-- Governance, moderation, administration, and publication add their named
-- columns; runtime never receives table-wide user UPDATE or any DELETE.
GRANT INSERT (display_name, email, avatar_url, created_at, updated_at, last_login_at),
      UPDATE (display_name, email, avatar_url, role, suspended_at,
              suspended_until, suspension_reason, muted_until, updated_at,
              last_login_at, administration_revision,
              publication_window_started_at, publication_count)
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
              updated_at)
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
