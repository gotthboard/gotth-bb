-- Apply as the schema owner after migrations with psql's runtime_role variable:
-- psql --set=ON_ERROR_STOP=1 --set=runtime_role=gotth_bb_runtime \
--   --file=deploy/postgresql/runtime-grants.sql "$DATABASE_URL"
--
-- PostgreSQL requires UPDATE privilege on at least one selected column for
-- SELECT ... FOR UPDATE. Restrict that privilege to the singleton key; the
-- primary key and CHECK constraint admit only the value true. Do not replace
-- this with table-wide UPDATE or DELETE privilege.
GRANT UPDATE (singleton)
ON TABLE public.governance_state
TO :"runtime_role";

-- Readiness must observe the migration-owned renderer completion singleton.
-- Keep this read-only and table-specific; runtime never owns or mutates it.
GRANT SELECT
ON TABLE public.content_renderer_state
TO :"runtime_role";

-- Readiness observes the migration-owned search projection singleton. Runtime
-- writers maintain topic/post tuples but never mutate migration progress.
GRANT SELECT
ON TABLE public.search_projection_state
TO :"runtime_role";

-- AN-04 presentation state is one migration-owned singleton. Runtime may read
-- it and update only the bounded presentation tuple; it may not create,
-- delete, or change the singleton key.
GRANT SELECT,
      UPDATE (site_name, site_description, brand_theme, rules_markdown,
              rules_html, rules_renderer_version, administration_revision,
              updated_at)
ON TABLE public.site_settings
TO :"runtime_role";

-- Local group governance creates and renames groups without granting table-
-- wide UPDATE or DELETE.
GRANT SELECT,
      INSERT (name, created_by, created_at, updated_at),
      UPDATE (name, updated_at, administration_revision)
ON TABLE public.forum_groups
TO :"runtime_role";

GRANT USAGE, SELECT
ON SEQUENCE public.forum_groups_id_seq
TO :"runtime_role";

-- Membership changes are one mapping per audited request. There is no mapping
-- UPDATE authority.
GRANT SELECT,
      INSERT (group_id, user_id, granted_by, created_at),
      DELETE
ON TABLE public.forum_group_members
TO :"runtime_role";
