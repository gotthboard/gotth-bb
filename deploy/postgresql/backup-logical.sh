#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: backup-logical.sh CONTAINER /absolute/output.dump' >&2
	exit 2
}

fail() {
	printf 'backup-logical: %s\n' "$1" >&2
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

parent=$(dirname -- "$archive") || fail 'cannot resolve archive parent'
parent_real=$(realpath -e -- "$parent") || fail 'archive parent must exist'
[ -d "$parent_real" ] || fail 'archive parent is not a directory'
[ ! -L "$parent" ] || fail 'archive parent must not be a symlink'
[ "$parent" = "$parent_real" ] || fail 'archive parent must be an absolute real path'
base=$(basename -- "$archive") || fail 'cannot resolve archive name'
[ "$base" != '.' ] && [ "$base" != '..' ] || fail 'invalid archive name'
if LC_ALL=C printf '%s' "$base" | grep -q '[[:cntrl:]]'; then
	fail 'archive name contains a control character'
fi
archive=$parent_real/$base
sidecar=$archive.sha256
if [ -e "$archive" ] || [ -L "$archive" ] || [ -e "$sidecar" ] || [ -L "$sidecar" ]; then
	fail 'archive or sidecar already exists'
fi

umask 077
temporary=$(mktemp "$parent_real/.$base.dump.XXXXXX") || fail 'cannot create temporary archive'
temporary_sidecar=$(mktemp "$parent_real/.$base.sha256.XXXXXX") || {
	rm -f -- "$temporary"
	fail 'cannot create temporary sidecar'
}
committed=0
child_pid=
cleanup() {
	if [ "$committed" -eq 0 ]; then
		rm -f -- "$temporary" "$temporary_sidecar"
	fi
}
cancel() {
	status=$1
	if [ -n "$child_pid" ]; then
		kill -TERM "$child_pid" 2>/dev/null || true
		wait "$child_pid" 2>/dev/null || true
		child_pid=
	fi
	exit "$status"
}
trap 'cancel 130' INT
trap 'cancel 143' TERM
trap 'cancel 129' HUP
trap cleanup EXIT

docker exec --user postgres "$container" sh -ceu '
  test -n "${POSTGRES_USER:-}" && test -n "${POSTGRES_DB:-}"
  exec pg_dump --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
    --format=custom --no-privileges --serializable-deferrable \
    --lock-wait-timeout=5s
' >"$temporary" &
child_pid=$!
if ! wait "$child_pid"; then
	child_pid=
	fail 'database dump failed'
fi
child_pid=
[ -s "$temporary" ] || fail 'database dump was empty'

docker exec --interactive --user postgres "$container" sh -ceu '
  exec pg_restore --list
' <"$temporary" >/dev/null &
child_pid=$!
if ! wait "$child_pid"; then
	child_pid=
	fail 'archive validation failed'
fi
child_pid=

sync "$temporary" || fail 'archive sync failed'
digest=$(sha256sum -- "$temporary" | awk '{print $1}') || fail 'archive digest failed'
case "$digest" in
	*[!0-9a-f]*|'') fail 'archive digest was invalid' ;;
esac
[ "${#digest}" -eq 64 ] || fail 'archive digest was invalid'
printf '%s\n' "$digest" >"$temporary_sidecar" || fail 'sidecar write failed'
sync "$temporary_sidecar" || fail 'sidecar sync failed'

mv --no-clobber --no-target-directory -- "$temporary" "$archive" || fail 'archive admission failed'
[ ! -e "$temporary" ] || fail 'archive target changed during admission'
mv --no-clobber --no-target-directory -- "$temporary_sidecar" "$sidecar" || fail 'sidecar admission failed'
[ ! -e "$temporary_sidecar" ] || fail 'sidecar target changed during admission'
committed=1
trap - EXIT HUP INT TERM

bytes=$(wc -c <"$archive" | tr -d '[:space:]') || fail 'archive size failed'
printf 'backup-logical: archive=%s bytes=%s sha256=%s\n' "$base" "$bytes" "$digest"
