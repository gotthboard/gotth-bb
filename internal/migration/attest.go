package migration

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const attestHistoryTableSQL = `WITH target AS (
    SELECT class.oid
    FROM pg_catalog.pg_class AS class
    JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = class.relnamespace
    WHERE namespace.nspname = 'public'
      AND class.relname = 'gotth_schema_migrations'
      AND class.relkind = 'r'
      AND class.relpersistence = 'p'
      AND NOT class.relrowsecurity
      AND NOT class.relforcerowsecurity
)
SELECT
    (SELECT count(*) = 1 FROM target)
    AND (SELECT count(*) = 4
         FROM pg_catalog.pg_attribute AS attribute
         JOIN target ON target.oid = attribute.attrelid
         WHERE attribute.attnum > 0 AND NOT attribute.attisdropped)
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute AS attribute JOIN target ON target.oid = attribute.attrelid
        WHERE attribute.attnum = 1 AND attribute.attname = 'version'
          AND attribute.atttypid = 'pg_catalog.int8'::pg_catalog.regtype AND attribute.attnotnull
          AND attribute.attidentity = '' AND attribute.attgenerated = '')
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute AS attribute JOIN target ON target.oid = attribute.attrelid
        WHERE attribute.attnum = 2 AND attribute.attname = 'name'
          AND attribute.atttypid = 'pg_catalog.text'::pg_catalog.regtype AND attribute.attnotnull
          AND attribute.attidentity = '' AND attribute.attgenerated = '')
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute AS attribute JOIN target ON target.oid = attribute.attrelid
        WHERE attribute.attnum = 3 AND attribute.attname = 'sha256'
          AND attribute.atttypid = 'pg_catalog.bytea'::pg_catalog.regtype AND attribute.attnotnull
          AND attribute.attidentity = '' AND attribute.attgenerated = '')
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute AS attribute JOIN target ON target.oid = attribute.attrelid
        WHERE attribute.attnum = 4 AND attribute.attname = 'applied_at'
          AND attribute.atttypid = 'pg_catalog.timestamptz'::pg_catalog.regtype AND attribute.attnotnull
          AND attribute.attidentity = '' AND attribute.attgenerated = '')
    AND (SELECT count(*) = 1
         FROM pg_catalog.pg_attrdef AS default_value
         JOIN target ON target.oid = default_value.adrelid)
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_attrdef AS default_value JOIN target ON target.oid = default_value.adrelid
        WHERE default_value.adnum = 4
          AND pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid, false) = 'clock_timestamp()')
    AND (SELECT count(*) = 3
         FROM pg_catalog.pg_constraint AS constraint_value
         JOIN target ON target.oid = constraint_value.conrelid)
    AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_constraint AS constraint_value
        JOIN target ON target.oid = constraint_value.conrelid
        JOIN pg_catalog.pg_index AS index_value ON index_value.indexrelid = constraint_value.conindid
        WHERE constraint_value.conname = 'gotth_schema_migrations_pkey' AND constraint_value.contype = 'p'
          AND constraint_value.convalidated AND index_value.indisprimary AND index_value.indisunique
          AND index_value.indisvalid AND index_value.indisready
          AND pg_catalog.pg_get_constraintdef(constraint_value.oid, false) = 'PRIMARY KEY (version)')
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint AS constraint_value JOIN target ON target.oid = constraint_value.conrelid
        WHERE constraint_value.conname = 'gotth_schema_migrations_version_positive' AND constraint_value.contype = 'c'
          AND constraint_value.convalidated
          AND pg_catalog.pg_get_constraintdef(constraint_value.oid, false) = 'CHECK ((version > 0))')
    AND EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint AS constraint_value JOIN target ON target.oid = constraint_value.conrelid
        WHERE constraint_value.conname = 'gotth_schema_migrations_sha256_length' AND constraint_value.contype = 'c'
          AND constraint_value.convalidated
          AND pg_catalog.pg_get_constraintdef(constraint_value.oid, false) = 'CHECK ((octet_length(sha256) = 32))')
    AND NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_inherits AS inheritance JOIN target ON target.oid = inheritance.inhrelid)
    AND NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger AS trigger_value JOIN target ON target.oid = trigger_value.tgrelid
        WHERE NOT trigger_value.tgisinternal)
    AND NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_rewrite AS rewrite_value JOIN target ON target.oid = rewrite_value.ev_class)`

type migrationRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// attestHistoryTable proves the existing ledger matches the exact PostgreSQL
// 17 catalog shape created by this release before any history is trusted.
//
// Complexity: local time and space are tight Theta(1). Total time is the
// delegated fixed-catalog query cost C: O(C), Omega(1), with no tighter Theta
// bound because PostgreSQL catalog access is external.
func attestHistoryTable(ctx context.Context, querier migrationRowQuerier) error {
	if ctx == nil {
		return fmt.Errorf("migration attestation context is required")
	}
	if querier == nil {
		return fmt.Errorf("migration attestation connection is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migration attestation canceled: %w", err)
	}
	var valid bool
	if err := querier.QueryRow(ctx, attestHistoryTableSQL).Scan(&valid); err != nil {
		return fmt.Errorf("attest migration history table: %w", err)
	}
	if !valid {
		return fmt.Errorf("migration history table does not match the release contract")
	}
	return nil
}
