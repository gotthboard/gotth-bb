package abuse

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type readinessDatabase interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const publicationCatalogReadySQL = `WITH expected_constraints(name, definition) AS (
    VALUES
      ('users_created_at_finite'::text, $1::text),
      ('users_publication_window_consistent'::text, $2::text)
), expected_columns(name, type_name, not_null, default_expression) AS (
    VALUES
      ('publication_window_started_at'::text, 'timestamp with time zone'::text, false, ''::text),
      ('publication_count'::text, 'integer'::text, true, '0'::text)
)
SELECT
    (SELECT count(*) FROM expected_constraints AS expected
     JOIN pg_catalog.pg_constraint AS actual
       ON actual.conrelid = 'public.users'::regclass
      AND actual.conname = expected.name
      AND actual.contype = 'c'
      AND actual.convalidated
      AND pg_catalog.pg_get_constraintdef(actual.oid, false) = expected.definition) = 2
    AND (SELECT count(*) FROM expected_columns AS expected
         JOIN pg_catalog.pg_attribute AS actual
           ON actual.attrelid = 'public.users'::regclass
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
           AND actual.attgenerated = '') = 2`

const publicationPrivilegeReadySQL = `SELECT
    NOT pg_catalog.pg_has_role(current_user, owner.oid, 'USAGE')
    AND has_table_privilege(current_user, 'public.users', 'SELECT')
    AND NOT has_table_privilege(current_user, 'public.users', 'UPDATE')
    AND has_column_privilege(current_user, 'public.users', 'publication_window_started_at', 'UPDATE')
    AND has_column_privilege(current_user, 'public.users', 'publication_count', 'UPDATE')
    AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_attribute AS column_state
        WHERE column_state.attrelid = 'public.users'::regclass
          AND column_state.attnum > 0
          AND NOT column_state.attisdropped
          AND column_state.attname NOT IN ('publication_window_started_at', 'publication_count')
          AND has_column_privilege(current_user, 'public.users', column_state.attname, 'UPDATE')
    )
FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_roles AS owner ON owner.oid = relation.relowner
WHERE relation.oid = 'public.users'::regclass`

const (
	createdAtFiniteDefinition  = "CHECK (isfinite(created_at))"
	publicationTupleDefinition = "CHECK ((((publication_window_started_at IS NULL) AND (publication_count = 0)) OR ((publication_window_started_at IS NOT NULL) AND isfinite(publication_window_started_at) AND (publication_window_started_at >= created_at) AND ((publication_count >= 1) AND (publication_count <= 100000)))))"
)

// PublicationReady attests migration 000011's exact columns, constraints, and
// the connected runtime role's two-column mutation authority.
func PublicationReady(ctx context.Context, database readinessDatabase) error {
	if ctx == nil || database == nil {
		return fmt.Errorf("publication readiness boundary is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publication readiness canceled: %w", err)
	}
	var valid bool
	if err := database.QueryRow(ctx, publicationCatalogReadySQL, createdAtFiniteDefinition, publicationTupleDefinition).Scan(&valid); err != nil {
		return fmt.Errorf("query publication catalog readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("publication catalog readiness failed")
	}
	if err := database.QueryRow(ctx, publicationPrivilegeReadySQL).Scan(&valid); err != nil {
		return fmt.Errorf("query publication privilege readiness: %w", err)
	}
	if !valid {
		return fmt.Errorf("publication privilege readiness failed")
	}
	return nil
}
