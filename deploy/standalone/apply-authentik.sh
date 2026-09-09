#!/bin/sh
set -eu

if [ -z "${GOTTH_BB_COMPOSE_ENV_FILE:-}" ]; then
	echo "GOTTH_BB_COMPOSE_ENV_FILE is required" >&2
	exit 1
fi
[ "$#" -le 1 ] || {
	echo "usage: apply-authentik.sh [COMPOSE_PROJECT_NAME]" >&2
	exit 2
}
project=${1:-gotth-bb-standalone}
case "$project" in
	'' | *[!a-z0-9_-]* | [!a-z0-9]*)
		echo "invalid Compose project name" >&2
		exit 2
		;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
output_file=$(mktemp "${TMPDIR:-/tmp}/gotth-bb-authentik-apply.XXXXXX")
trap 'rm -f "$output_file"' EXIT HUP INT TERM

if ! docker compose \
	--env-file "$GOTTH_BB_COMPOSE_ENV_FILE" \
	--project-name "$project" \
	--project-directory "$script_dir" \
	-f "$script_dir/compose.yml" \
	exec -T authentik-server \
	/bootstrap/entrypoint.sh shell -c \
	'exec(open("/bootstrap/apply.py", encoding="utf-8").read())' \
	>"$output_file" 2>&1; then
	tail -n 80 "$output_file" >&2
	exit 1
fi

if ! grep -Fxq AUTHENTIK_BOARD_BLUEPRINT_APPLIED "$output_file"; then
	echo "Authentik bootstrap completed without its success marker" >&2
	tail -n 80 "$output_file" >&2
	exit 1
fi

echo AUTHENTIK_BOARD_BLUEPRINT_APPLIED
