package site

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type readinessDatabase interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

var expectedConstraintRelations = []string{
	"public.areas",
	"public.forum_groups",
	"public.moderation_actions", "public.moderation_actions", "public.moderation_actions",
	"public.moderation_actions", "public.moderation_actions", "public.moderation_actions",
	"public.site_settings", "public.site_settings", "public.site_settings", "public.site_settings",
	"public.site_settings", "public.site_settings", "public.site_settings", "public.site_settings",
	"public.site_settings", "public.site_settings",
	"public.users",
}

var expectedConstraintNames = []string{
	"areas_administration_revision_positive",
	"forum_groups_administration_revision_positive",
	"moderation_actions_action_type_closed",
	"moderation_actions_reason_length",
	"moderation_actions_reason_required",
	"moderation_actions_target_consistent",
	"moderation_actions_target_site_true",
	"moderation_actions_target_type_closed",
	"site_settings_description_shape",
	"site_settings_name_shape",
	"site_settings_pkey",
	"site_settings_revision_positive",
	"site_settings_rules_html_size",
	"site_settings_rules_source_size",
	"site_settings_rules_tuple_current",
	"site_settings_singleton_true",
	"site_settings_theme_closed",
	"site_settings_updated_at_finite",
	"users_administration_revision_positive",
}

var expectedConstraintDefinitions = []string{
	"CHECK ((administration_revision > 0))",
	"CHECK ((administration_revision > 0))",
	"CHECK ((action_type = ANY (ARRAY['bootstrap_administrator'::text, 'change_role'::text, 'grant_group_membership'::text, 'revoke_group_membership'::text, 'create_group'::text, 'rename_group'::text, 'create_area'::text, 'update_area'::text, 'change_area_visibility'::text, 'change_area_posting_mode'::text, 'grant_area_group'::text, 'revoke_area_group'::text, 'lock_topic'::text, 'unlock_topic'::text, 'hide_topic'::text, 'restore_topic'::text, 'pin_topic'::text, 'unpin_topic'::text, 'move_topic'::text, 'archive_topic'::text, 'hide_post'::text, 'restore_post'::text, 'redact_post'::text, 'warn_user'::text, 'mute_user'::text, 'suspend_user'::text, 'reinstate_user'::text, 'assign_report'::text, 'note_report'::text, 'resolve_report'::text, 'dismiss_report'::text, 'update_site_settings'::text])))",
	"CHECK (((reason IS NULL) OR (((octet_length(reason) >= 1) AND (octet_length(reason) <= 2000)) AND (reason !~ '[[:cntrl:]]'::text) AND (reason = btrim(reason, ' '::text)))))",
	"CHECK (((action_type <> ALL (ARRAY['change_role'::text, 'grant_group_membership'::text, 'revoke_group_membership'::text, 'create_group'::text, 'rename_group'::text, 'create_area'::text, 'update_area'::text, 'grant_area_group'::text, 'revoke_area_group'::text, 'move_topic'::text, 'hide_topic'::text, 'archive_topic'::text, 'hide_post'::text, 'redact_post'::text, 'warn_user'::text, 'mute_user'::text, 'suspend_user'::text, 'reinstate_user'::text, 'resolve_report'::text, 'dismiss_report'::text, 'update_site_settings'::text])) OR (reason IS NOT NULL)))",
	"CHECK (((num_nonnulls(target_user_id, target_group_id, target_area_id, target_topic_id, target_post_id, target_report_id, target_site) = 1) AND ((target_type <> 'user'::text) OR (target_user_id IS NOT NULL)) AND ((target_type <> 'group'::text) OR (target_group_id IS NOT NULL)) AND ((target_type <> 'area'::text) OR (target_area_id IS NOT NULL)) AND ((target_type <> 'topic'::text) OR (target_topic_id IS NOT NULL)) AND ((target_type <> 'post'::text) OR (target_post_id IS NOT NULL)) AND ((target_type <> 'report'::text) OR (target_report_id IS NOT NULL)) AND ((target_type <> 'site'::text) OR (target_site IS NOT NULL))))",
	"CHECK (((target_site IS NULL) OR target_site))",
	"CHECK ((target_type = ANY (ARRAY['user'::text, 'group'::text, 'area'::text, 'topic'::text, 'post'::text, 'report'::text, 'site'::text])))",
	"CHECK (((char_length(site_description) <= 280) AND (site_description !~ '[[:cntrl:]]'::text)))",
	"CHECK ((((char_length(site_name) >= 1) AND (char_length(site_name) <= 80)) AND (site_name !~ '[[:cntrl:]]'::text)))",
	"PRIMARY KEY (singleton)",
	"CHECK ((administration_revision > 0))",
	"CHECK ((octet_length(rules_html) <= 262144))",
	"CHECK ((octet_length(rules_markdown) <= 65536))",
	"CHECK (((rules_renderer_version = 'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2'::text) AND (((rules_markdown = ''::text) AND (rules_html = ''::text)) OR ((rules_markdown <> ''::text) AND (rules_html <> ''::text)))))",
	"CHECK (singleton)",
	"CHECK ((brand_theme = ANY (ARRAY['blue'::text, 'cyan'::text, 'emerald'::text, 'amber'::text, 'rose'::text])))",
	"CHECK (isfinite(updated_at))",
	"CHECK ((administration_revision > 0))",
}

var expectedColumnRelations = []string{
	"public.site_settings", "public.site_settings", "public.site_settings", "public.site_settings",
	"public.site_settings", "public.site_settings", "public.site_settings", "public.site_settings", "public.site_settings",
	"public.users", "public.forum_groups", "public.areas", "public.moderation_actions",
}

var expectedColumnNames = []string{
	"singleton", "site_name", "site_description", "brand_theme", "rules_markdown",
	"rules_html", "rules_renderer_version", "administration_revision", "updated_at",
	"administration_revision", "administration_revision", "administration_revision", "target_site",
}

var expectedColumnTypes = []string{
	"boolean", "text", "text", "text", "text", "text", "text", "bigint", "timestamp with time zone",
	"bigint", "bigint", "bigint", "boolean",
}

var expectedColumnNotNull = []bool{true, true, true, true, true, true, true, true, true, true, true, true, false}
var expectedColumnDefaults = []string{"true", "", "", "", "", "", "", "1", "clock_timestamp()", "1", "1", "1", ""}

const catalogReadySQL = `WITH expected_constraints AS (
    SELECT relation_name, name, definition
    FROM ROWS FROM (
        pg_catalog.unnest($1::text[]),
        pg_catalog.unnest($2::text[]),
        pg_catalog.unnest($3::text[])
    ) AS expected(relation_name, name, definition)
), expected_columns AS (
    SELECT relation_name, name, type_name, not_null, default_expression
    FROM ROWS FROM (
        pg_catalog.unnest($4::text[]),
        pg_catalog.unnest($5::text[]),
        pg_catalog.unnest($6::text[]),
        pg_catalog.unnest($7::boolean[]),
        pg_catalog.unnest($8::text[])
    ) AS expected(relation_name, name, type_name, not_null, default_expression)
)
SELECT
    (SELECT count(*) FROM expected_constraints AS expected
     JOIN pg_catalog.pg_constraint AS actual
       ON actual.conrelid = expected.relation_name::regclass
      AND actual.conname = expected.name
      AND actual.convalidated
      AND pg_catalog.pg_get_constraintdef(actual.oid, false) = expected.definition) = cardinality($2::text[])
    AND (SELECT count(*) FROM expected_columns AS expected
         JOIN pg_catalog.pg_attribute AS actual
           ON actual.attrelid = expected.relation_name::regclass
          AND actual.attname = expected.name
          AND actual.attnum > 0
          AND NOT actual.attisdropped
         LEFT JOIN pg_catalog.pg_attrdef AS default_value
           ON default_value.adrelid = actual.attrelid
          AND default_value.adnum = actual.attnum
         WHERE pg_catalog.format_type(actual.atttypid, actual.atttypmod) = expected.type_name
           AND actual.attnotnull = expected.not_null
           AND COALESCE(pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid), '') = expected.default_expression
           AND actual.attidentity = ''
           AND actual.attgenerated = '') = cardinality($5::text[])
    AND (SELECT count(*) FROM pg_catalog.pg_attribute
         WHERE attrelid = 'public.site_settings'::regclass
           AND attnum > 0 AND NOT attisdropped) = 9`

const stateReadySQL = `SELECT site_name, site_description, brand_theme,
       rules_markdown, rules_html, rules_renderer_version,
       administration_revision, updated_at
FROM public.site_settings
WHERE singleton
  AND (SELECT count(*) FROM public.site_settings) = 1`

const privilegeReadySQL = `WITH owners AS (
    SELECT bool_or(owner.rolname = current_user) AS current_user_owns_an04_relation
    FROM pg_catalog.pg_class AS relation
    JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    JOIN pg_catalog.pg_roles AS owner ON owner.oid = relation.relowner
    WHERE namespace.nspname = 'public'
      AND relation.relname IN ('site_settings', 'forum_groups', 'forum_group_members')
)
SELECT NOT owners.current_user_owns_an04_relation
   AND has_table_privilege(current_user, 'public.site_settings', 'SELECT')
   AND has_column_privilege(current_user, 'public.site_settings', 'site_name', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'site_description', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'brand_theme', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'rules_markdown', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'rules_html', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'rules_renderer_version', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'administration_revision', 'UPDATE')
   AND has_column_privilege(current_user, 'public.site_settings', 'updated_at', 'UPDATE')
   AND NOT has_column_privilege(current_user, 'public.site_settings', 'singleton', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.site_settings', 'INSERT')
   AND NOT has_table_privilege(current_user, 'public.site_settings', 'DELETE')
   AND has_table_privilege(current_user, 'public.forum_groups', 'SELECT')
   AND has_column_privilege(current_user, 'public.forum_groups', 'name', 'INSERT')
   AND has_column_privilege(current_user, 'public.forum_groups', 'created_by', 'INSERT')
   AND has_column_privilege(current_user, 'public.forum_groups', 'created_at', 'INSERT')
   AND has_column_privilege(current_user, 'public.forum_groups', 'updated_at', 'INSERT')
   AND has_column_privilege(current_user, 'public.forum_groups', 'name', 'UPDATE')
   AND has_column_privilege(current_user, 'public.forum_groups', 'updated_at', 'UPDATE')
   AND has_column_privilege(current_user, 'public.forum_groups', 'administration_revision', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.forum_groups', 'DELETE')
   AND has_sequence_privilege(current_user, 'public.forum_groups_id_seq', 'USAGE')
   AND has_sequence_privilege(current_user, 'public.forum_groups_id_seq', 'SELECT')
   AND NOT has_sequence_privilege(current_user, 'public.forum_groups_id_seq', 'UPDATE')
   AND has_table_privilege(current_user, 'public.forum_group_members', 'SELECT')
   AND has_column_privilege(current_user, 'public.forum_group_members', 'group_id', 'INSERT')
   AND has_column_privilege(current_user, 'public.forum_group_members', 'user_id', 'INSERT')
   AND has_column_privilege(current_user, 'public.forum_group_members', 'granted_by', 'INSERT')
   AND has_column_privilege(current_user, 'public.forum_group_members', 'created_at', 'INSERT')
   AND has_table_privilege(current_user, 'public.forum_group_members', 'DELETE')
   AND NOT has_table_privilege(current_user, 'public.forum_group_members', 'UPDATE')
   AND NOT has_table_privilege(current_user, 'public.users', 'DELETE')
   AND NOT has_table_privilege(current_user, 'public.areas', 'DELETE')
   AND NOT has_table_privilege(current_user, 'public.moderation_actions', 'DELETE')
   AND NOT has_table_privilege(current_user, 'public.area_groups', 'UPDATE')
FROM owners`

// Ready attests migration 000010's exact catalog, singleton contents, current
// renderer output, and the connected runtime role's narrow privilege delta.
func Ready(ctx context.Context, database readinessDatabase) error {
	if ctx == nil || database == nil {
		return fmt.Errorf("site settings readiness boundary is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("site settings readiness canceled: %w", err)
	}
	var valid bool
	if err := database.QueryRow(ctx, catalogReadySQL,
		expectedConstraintRelations, expectedConstraintNames, expectedConstraintDefinitions,
		expectedColumnRelations, expectedColumnNames, expectedColumnTypes,
		expectedColumnNotNull, expectedColumnDefaults,
	).Scan(&valid); err != nil {
		return fmt.Errorf("query site settings catalog readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("site settings catalog readiness failed")
	}
	var name, description, theme, rulesMarkdown, rulesHTML, rendererVersion string
	var revision int64
	var updatedAt pgtype.Timestamptz
	if err := database.QueryRow(ctx, stateReadySQL).Scan(
		&name, &description, &theme, &rulesMarkdown, &rulesHTML, &rendererVersion, &revision, &updatedAt,
	); err != nil {
		return fmt.Errorf("query site settings state readiness: %w", err)
	}
	if revision <= 0 || !updatedAt.Valid || updatedAt.InfinityModifier != pgtype.Finite ||
		!validShell(ShellPresentation{Name: name, Description: description, Theme: theme}) {
		return fmt.Errorf("site settings state readiness failed")
	}
	if _, err := validateRulesTuple(rulesMarkdown, rulesHTML, rendererVersion); err != nil {
		return fmt.Errorf("site settings renderer readiness failed")
	}
	if err := database.QueryRow(ctx, privilegeReadySQL).Scan(&valid); err != nil {
		return fmt.Errorf("query site settings privilege readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("site settings privilege readiness failed")
	}
	return nil
}
