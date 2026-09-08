#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: restore-logical.sh CONTAINER /absolute/input.dump' >&2
	exit 2
}

fail() {
	printf 'restore-logical: %s\n' "$1" >&2
	exit 1
}

[ "$#" -eq 2 ] || usage
container=$1
archive=$2

case "$container" in
	''|*[!A-Za-z0-9_.-]*|[!A-Za-z0-9]*) fail 'invalid container name' ;;
esac
case "$archive" in
	/*) ;;
	*) fail 'archive path must be absolute' ;;
esac
[ -f "$archive" ] && [ ! -L "$archive" ] || fail 'archive must be a regular non-symlink file'
sidecar=$archive.sha256
[ -f "$sidecar" ] && [ ! -L "$sidecar" ] || fail 'sidecar must be a regular non-symlink file'

[ "$(wc -c <"$sidecar" | tr -d '[:space:]')" -eq 65 ] || fail 'sidecar format is invalid'
[ "$(wc -l <"$sidecar" | tr -d '[:space:]')" -eq 1 ] || fail 'sidecar format is invalid'
expected=$(cat -- "$sidecar") || fail 'cannot read sidecar'
case "$expected" in
	*[!0-9a-f]*|'') fail 'sidecar format is invalid' ;;
esac
[ "${#expected}" -eq 64 ] || fail 'sidecar format is invalid'
actual=$(sha256sum -- "$archive" | awk '{print $1}') || fail 'archive digest failed'
[ "$actual" = "$expected" ] || fail 'archive digest mismatch'

if ! docker exec --interactive --user postgres "$container" sh -ceu '
  exec pg_restore --list
' <"$archive" >/dev/null; then
	fail 'archive validation failed'
fi

version=$(docker exec --user postgres "$container" postgres --version) || fail 'PostgreSQL version check failed'
case "$version" in
	'postgres (PostgreSQL) 17.'*) ;;
	*) fail 'target is not PostgreSQL major 17' ;;
esac

if ! docker exec --user postgres "$container" sh -ceu '
  test -n "${POSTGRES_USER:-}" && test -n "${POSTGRES_DB:-}"
'; then
	fail 'target database identity is unavailable'
fi

relations=$(docker exec --user postgres "$container" sh -ceu '
  exec psql --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
    --no-psqlrc --tuples-only --no-align --set=ON_ERROR_STOP=1 --command="
      SELECT count(*)
      FROM pg_catalog.pg_class AS relation
      JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
      WHERE namespace.nspname NOT IN (\$\$pg_catalog\$\$, \$\$information_schema\$\$)
        AND namespace.nspname !~ \$\$^pg_toast\$\$
        AND relation.relkind IN (\$\$r\$\$, \$\$p\$\$, \$\$v\$\$, \$\$m\$\$, \$\$S\$\$, \$\$f\$\$)
        AND NOT EXISTS (
          SELECT 1 FROM pg_catalog.pg_depend AS dependency
          WHERE dependency.classid = \$\$pg_class\$\$::regclass
            AND dependency.objid = relation.oid
            AND dependency.deptype = \$\$e\$\$
        );"
') || fail 'clean-target inspection failed'
[ "$relations" = 0 ] || fail 'target database is not clean'

if ! docker exec --interactive --user postgres "$container" sh -ceu '
  exec pg_restore --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
    --exit-on-error --single-transaction --no-privileges
' <"$archive"; then
	fail 'database restore failed; inspect target before retry'
fi

printf 'restore-logical: archive=%s sha256=%s target=%s result=committed\n' "$(basename -- "$archive")" "$actual" "$container"
