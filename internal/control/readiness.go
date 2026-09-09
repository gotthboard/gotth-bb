package control

import (
	"context"
	"fmt"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
)

type readinessDatabase interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const controlCatalogReadySQL = `WITH expected_relations(name, columns) AS (
    VALUES ('users'::text, 20), ('site_settings'::text, 18),
           ('pending_registrations'::text, 12),
           ('registration_invitations'::text, 12),
           ('email_test_state'::text, 6)
), expected_constraints(name) AS (
    VALUES
      ('users_authentik_sync_state_closed'::text),
      ('users_authentik_sync_attempts_finite'::text),
      ('users_authentik_sync_failure_shape'::text),
      ('site_settings_registration_mode_closed'::text),
      ('site_settings_maintenance_message_shape'::text),
      ('site_settings_publication_limits_positive'::text),
      ('site_settings_session_limits_positive'::text),
      ('pending_registrations_authentik_user_positive'::text),
      ('pending_registrations_display_name_shape'::text),
      ('pending_registrations_verified_email_shape'::text),
      ('pending_registrations_status_closed'::text),
      ('pending_registrations_revision_positive'::text),
      ('pending_registrations_times_finite'::text),
      ('pending_registrations_decision_consistent'::text),
      ('pending_registrations_reconciliation_shape'::text),
      ('registration_invitations_name_shape'::text),
      ('registration_invitations_transition_closed'::text),
      ('registration_invitations_delivery_closed'::text),
      ('registration_invitations_flow_shape'::text),
      ('registration_invitations_times_ordered'::text),
      ('registration_invitations_revision_positive'::text),
      ('registration_invitations_fingerprint_length'::text),
      ('registration_invitations_failure_shape'::text),
      ('email_test_state_status_closed'::text),
      ('email_test_state_times_ordered'::text),
      ('email_test_state_completion_consistent'::text),
      ('moderation_actions_expiry_actor_consistent'::text)
), expected_indexes(name) AS (
    VALUES ('pending_registrations_queue_idx'::text),
           ('registration_invitations_state_idx'::text)
)
SELECT
    (SELECT count(*) FROM expected_relations AS expected
     JOIN pg_catalog.pg_class AS relation ON relation.relname = expected.name
     JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
     WHERE namespace.nspname = 'public' AND relation.relkind = 'r'
       AND (SELECT count(*) FROM pg_catalog.pg_attribute AS attribute
            WHERE attribute.attrelid = relation.oid AND attribute.attnum > 0
              AND NOT attribute.attisdropped) = expected.columns) = 5
    AND (SELECT count(*) FROM expected_constraints AS expected
         JOIN pg_catalog.pg_constraint AS actual ON actual.conname = expected.name
         WHERE actual.connamespace = 'public'::regnamespace
           AND actual.convalidated) = 27
    AND (SELECT count(*) FROM expected_indexes AS expected
         JOIN pg_catalog.pg_class AS actual ON actual.relname = expected.name
         WHERE actual.relnamespace = 'public'::regnamespace
           AND actual.relkind = 'i') = 2
    AND (SELECT count(*) FROM pg_catalog.pg_proc AS function_state
         WHERE function_state.oid IN (
             'public.session_id_for_token(bytea)'::regprocedure,
             'public.revoke_session_by_token(bytea,timestamp with time zone)'::regprocedure,
             'public.revoke_session_for_rotation_by_token(bigint,bytea,timestamp with time zone)'::regprocedure
         )
           AND function_state.prosecdef
           AND function_state.proconfig = ARRAY['search_path=pg_catalog, pg_temp']::text[]
           AND NOT pg_catalog.pg_has_role(current_user, function_state.proowner, 'USAGE')
           AND NOT has_function_privilege('public', function_state.oid, 'EXECUTE')) = 3`

const controlStateReadySQL = `SELECT registration_mode, maintenance_enabled,
       maintenance_message, publish_rate_limit,
       new_account_publish_rate_limit, publish_window_seconds,
       new_account_period_seconds, session_idle_seconds,
       auth_revalidate_seconds, administration_revision
FROM public.site_settings
WHERE singleton
  AND (SELECT count(*) FROM public.site_settings) = 1`

const controlPrivilegeReadySQL = `WITH owners AS (
    SELECT bool_or(pg_catalog.pg_has_role(current_user, relation.relowner, 'USAGE')) AS owns_control_relation
    FROM pg_catalog.pg_class AS relation
    WHERE relation.oid IN (
        'public.site_settings'::regclass,
        'public.pending_registrations'::regclass,
        'public.registration_invitations'::regclass,
        'public.email_test_state'::regclass,
        'public.sessions'::regclass
    )
)
SELECT NOT owners.owns_control_relation
   AND has_table_privilege(current_user, 'public.site_settings', 'SELECT')
   AND has_column_privilege(current_user, 'public.site_settings', 'registration_mode', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'maintenance_enabled', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'auth_revalidate_seconds', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.site_settings', 'INSERT')
   AND NOT has_table_privilege(current_user, 'public.site_settings', 'DELETE')
   AND has_table_privilege(current_user, 'public.pending_registrations', 'SELECT')
   AND has_column_privilege(current_user, 'public.pending_registrations', 'status', 'INSERT')
   AND has_column_privilege(current_user, 'public.pending_registrations', 'status', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.pending_registrations', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.pending_registrations', 'DELETE')
   AND has_table_privilege(current_user, 'public.registration_invitations', 'SELECT')
   AND has_column_privilege(current_user, 'public.registration_invitations', 'idempotency_key', 'INSERT')
   AND has_column_privilege(current_user, 'public.registration_invitations', 'transition_state', 'UPDATE')
   AND NOT has_column_privilege(current_user, 'public.registration_invitations', 'idempotency_key', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.registration_invitations', 'DELETE')
   AND has_table_privilege(current_user, 'public.email_test_state', 'SELECT')
   AND has_column_privilege(current_user, 'public.email_test_state', 'status', 'INSERT')
   AND has_column_privilege(current_user, 'public.email_test_state', 'status', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.email_test_state', 'DELETE')
   AND has_column_privilege(current_user, 'public.sessions', 'id', 'SELECT')
   AND NOT has_column_privilege(current_user, 'public.sessions', 'token_hash', 'SELECT')
   AND NOT has_table_privilege(current_user, 'public.sessions', 'UPDATE')
   AND has_column_privilege(current_user, 'public.sessions', 'revoked_at', 'UPDATE')
   AND has_function_privilege(current_user, 'public.session_id_for_token(bytea)', 'EXECUTE')
   AND has_function_privilege(current_user, 'public.revoke_session_by_token(bytea,timestamp with time zone)', 'EXECUTE')
   AND has_function_privilege(current_user, 'public.revoke_session_for_rotation_by_token(bigint,bytea,timestamp with time zone)', 'EXECUTE')
   AND has_column_privilege(current_user, 'public.users', 'authentik_sync_state', 'INSERT')
   AND NOT has_table_privilege(current_user, 'public.users', 'INSERT')
FROM owners`

// Ready attests the B1-09 control schema, runtime state, immutable startup
// ceilings, SMTP registration gate, and the connected role's narrow grants.
func Ready(ctx context.Context, database readinessDatabase, ceilings Ceilings, smtpConfigured bool) error {
	if ctx == nil || database == nil || !ceilings.Valid() {
		return fmt.Errorf("control readiness boundary is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("control readiness canceled: %w", err)
	}
	var valid bool
	if err := database.QueryRow(ctx, controlCatalogReadySQL).Scan(&valid); err != nil {
		return fmt.Errorf("query control catalog readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("control catalog readiness failed")
	}
	var row db.LoadRuntimeControlSettingsRow
	if err := database.QueryRow(ctx, controlStateReadySQL).Scan(
		&row.RegistrationMode, &row.MaintenanceEnabled,
		&row.MaintenanceMessage, &row.PublishRateLimit,
		&row.NewAccountPublishRateLimit, &row.PublishWindowSeconds,
		&row.NewAccountPeriodSeconds, &row.SessionIdleSeconds,
		&row.AuthRevalidateSeconds, &row.AdministrationRevision,
	); err != nil {
		return fmt.Errorf("query control state readiness: %w", err)
	}
	settings, err := validateRow(row, ceilings)
	if err != nil {
		return fmt.Errorf("control state readiness failed: %w", err)
	}
	if settings.Registration != RegistrationClosed && !smtpConfigured {
		return fmt.Errorf("control registration readiness failed")
	}
	if err := database.QueryRow(ctx, controlPrivilegeReadySQL).Scan(&valid); err != nil {
		return fmt.Errorf("query control privilege readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("control privilege readiness failed")
	}
	return nil
}
