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

