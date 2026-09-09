#!/bin/sh
set -eu

fail() {
	echo "standalone preflight: $*" >&2
	exit 1
}

[ "$(id -u)" -eq 0 ] || fail "run as root"
[ "$#" -eq 1 ] || fail "expected one absolute deployment.env path"
deployment_env=$1
case "$deployment_env" in /*) ;; *) fail "deployment.env path is not absolute" ;; esac
[ -f "$deployment_env" ] && [ ! -L "$deployment_env" ] || fail "deployment.env is not a regular non-symlink file"
[ "$(stat -c %u:%g:%a "$deployment_env")" = "0:0:600" ] || fail "deployment.env must be root:root mode 0600"

set -a
# deployment.env is sourced only after proving it is root-owned and private.
# It is operator code, not an untrusted dotenv upload.
. "$deployment_env"
set +a

required_variables='GOTTH_BB_IMAGE GOTTH_BB_ENV_FILE GOTTH_BB_PUBLIC_BASE_URL GOTTH_BB_AUTH_PUBLIC_BASE_URL GOTTH_BB_OIDC_REDIRECT_URI GOTTH_BB_BOARD_HOST GOTTH_BB_AUTH_HOST GOTTH_BB_CADDY_BIND GOTTH_BB_CADDY_CLIENT_ADDRESS GOTTH_BB_CADDY_DATA_DIR GOTTH_BB_CADDY_CONFIG_DIR GOTTH_BB_POSTGRES_DATA_DIR GOTTH_BB_AUTHENTIK_POSTGRES_DATA_DIR GOTTH_BB_AUTHENTIK_DATA_DIR GOTTH_BB_AUTHENTIK_TEMPLATES_DIR GOTTH_BB_AUTHENTIK_CERTS_DIR GOTTH_BB_ABUSE_RULES_FILE GOTTH_BB_POSTGRES_MIGRATE_PASSWORD_FILE GOTTH_BB_POSTGRES_RUNTIME_PASSWORD_FILE GOTTH_BB_DATABASE_URL_FILE GOTTH_BB_OIDC_CLIENT_SECRET_FILE GOTTH_BB_ACTIVITY_CURSOR_KEYRING_FILE GOTTH_BB_AUTHENTIK_POSTGRES_PASSWORD_FILE GOTTH_BB_AUTHENTIK_SECRET_KEY_FILE'
for name in $required_variables; do
	eval "value=\${$name-}"
	[ -n "$value" ] || fail "$name is required"
done

[ "$GOTTH_BB_OIDC_REDIRECT_URI" = "$GOTTH_BB_PUBLIC_BASE_URL/auth/callback" ] || fail "OIDC redirect URI differs from the Board callback"
[ "$GOTTH_BB_PUBLIC_BASE_URL" != "$GOTTH_BB_AUTH_PUBLIC_BASE_URL" ] || fail "Board and Authentik public origins must differ"

case "$GOTTH_BB_CADDY_BIND" in 0.0.0.0 | 127.0.0.1) ;; *) fail "Caddy bind must be 0.0.0.0 or 127.0.0.1" ;; esac
case "$GOTTH_BB_CADDY_BIND:$GOTTH_BB_PUBLIC_BASE_URL:$GOTTH_BB_AUTH_PUBLIC_BASE_URL" in
	0.0.0.0:https://*:https://*)
		[ "$GOTTH_BB_CADDY_CLIENT_ADDRESS" = '{remote_host}' ] || fail "direct Caddy must derive client identity from its peer"
		[ "$GOTTH_BB_BOARD_HOST" = "${GOTTH_BB_PUBLIC_BASE_URL#https://}" ] || fail "direct Board Caddy address differs from its public origin"
		[ "$GOTTH_BB_AUTH_HOST" = "${GOTTH_BB_AUTH_PUBLIC_BASE_URL#https://}" ] || fail "direct Authentik Caddy address differs from its public origin"
		;;
	127.0.0.1:https://*:https://*)
		[ "$GOTTH_BB_CADDY_CLIENT_ADDRESS" = '{http.request.header.X-Forwarded-For}' ] || fail "edge-fed Caddy must consume the edge canonical client identity"
		case "$GOTTH_BB_BOARD_HOST" in http://"${GOTTH_BB_PUBLIC_BASE_URL#https://}":[0-9]*) ;; *) fail "edge-fed Board Caddy address differs from its public origin" ;; esac
		case "$GOTTH_BB_AUTH_HOST" in http://"${GOTTH_BB_AUTH_PUBLIC_BASE_URL#https://}":[0-9]*) ;; *) fail "edge-fed Authentik Caddy address differs from its public origin" ;; esac
		;;
	127.0.0.1:http://127.0.0.1:*:http://127.0.0.1:*)
		[ "$GOTTH_BB_CADDY_CLIENT_ADDRESS" = '{remote_host}' ] || fail "loopback-test Caddy must derive client identity from its peer"
		[ "$GOTTH_BB_BOARD_HOST" = "$GOTTH_BB_PUBLIC_BASE_URL" ] || fail "test Board Caddy address differs from its public origin"
		[ "$GOTTH_BB_AUTH_HOST" = "$GOTTH_BB_AUTH_PUBLIC_BASE_URL" ] || fail "test Authentik Caddy address differs from its public origin"
		;;
	*) fail "Caddy bind and public origins are not an admitted direct, edge-fed, or loopback-test tuple" ;;
esac

GOTTH_BB_APP_HTTP_PORT=${GOTTH_BB_APP_HTTP_PORT:-18082}
GOTTH_BB_AUTH_HTTP_PORT=${GOTTH_BB_AUTH_HTTP_PORT:-19000}
GOTTH_BB_POSTGRES_PORT=${GOTTH_BB_POSTGRES_PORT:-55435}
GOTTH_BB_CADDY_ADMIN=${GOTTH_BB_CADDY_ADMIN:-127.0.0.1:2019}
case "$GOTTH_BB_CADDY_ADMIN" in 127.0.0.1:*) caddy_admin_port=${GOTTH_BB_CADDY_ADMIN##*:} ;; *) fail "Caddy admin binding is not loopback" ;; esac
for binding in "$GOTTH_BB_APP_HTTP_PORT" "$GOTTH_BB_AUTH_HTTP_PORT" "$GOTTH_BB_POSTGRES_PORT" "$caddy_admin_port"; do
	case "$binding" in '' | *[!0-9]*) fail "loopback service port is invalid" ;; esac
	[ "$binding" -ge 1024 ] && [ "$binding" -le 65535 ] || fail "loopback service port is outside 1024..65535"
done
[ "$GOTTH_BB_APP_HTTP_PORT" != "$GOTTH_BB_AUTH_HTTP_PORT" ] &&
	[ "$GOTTH_BB_APP_HTTP_PORT" != "$GOTTH_BB_POSTGRES_PORT" ] &&
	[ "$GOTTH_BB_AUTH_HTTP_PORT" != "$GOTTH_BB_POSTGRES_PORT" ] &&
	[ "$caddy_admin_port" != "$GOTTH_BB_APP_HTTP_PORT" ] &&
	[ "$caddy_admin_port" != "$GOTTH_BB_AUTH_HTTP_PORT" ] &&
	[ "$caddy_admin_port" != "$GOTTH_BB_POSTGRES_PORT" ] || fail "loopback service ports overlap"

case "$GOTTH_BB_IMAGE" in
	gotth-bb:*-[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
	*) fail "Board image must carry a release and commit identity" ;;
esac

check_path() {
	path=$1
	expected=$2
	type=$3
	case "$path" in /*) ;; *) fail "$path is not absolute" ;; esac
	[ ! -L "$path" ] || fail "$path must not be a symlink"
	case "$type" in
		directory) [ -d "$path" ] || fail "$path is not a directory" ;;
		file) [ -f "$path" ] || fail "$path is not a regular file" ;;
		*) fail "internal path type error" ;;
	esac
	actual=$(stat -c %u:%g:%a "$path")
	[ "$actual" = "$expected" ] || fail "$path ownership/mode is $actual, want $expected"
}

check_path "$GOTTH_BB_ENV_FILE" 0:0:600 file
check_path "$GOTTH_BB_ABUSE_RULES_FILE" 0:65532:440 file
check_path "$GOTTH_BB_CADDY_DATA_DIR" 65532:65532:700 directory
check_path "$GOTTH_BB_CADDY_CONFIG_DIR" 65532:65532:700 directory
check_path "$GOTTH_BB_POSTGRES_DATA_DIR" 999:999:700 directory
check_path "$GOTTH_BB_AUTHENTIK_POSTGRES_DATA_DIR" 70:70:700 directory
check_path "$GOTTH_BB_AUTHENTIK_DATA_DIR" 1000:1000:770 directory
check_path "$GOTTH_BB_AUTHENTIK_TEMPLATES_DIR" 1000:1000:700 directory
check_path "$GOTTH_BB_AUTHENTIK_CERTS_DIR" 1000:1000:750 directory

check_secret() {
	path=$1
	group=$2
	check_path "$path" "0:$group:440" file
	bytes=$(wc -c <"$path" | tr -d ' ')
	[ "$bytes" -gt 0 ] || fail "$path is empty"
	framed=$(tr -d '\000\r\n' <"$path" | wc -c | tr -d ' ')
	[ "$bytes" = "$framed" ] || fail "$path contains NUL, CR, or LF framing"
}

check_secret "$GOTTH_BB_POSTGRES_MIGRATE_PASSWORD_FILE" 999
check_secret "$GOTTH_BB_POSTGRES_RUNTIME_PASSWORD_FILE" 999
check_secret "$GOTTH_BB_AUTHENTIK_POSTGRES_PASSWORD_FILE" 999
check_secret "$GOTTH_BB_AUTHENTIK_SECRET_KEY_FILE" 1000
check_secret "$GOTTH_BB_DATABASE_URL_FILE" 65532
check_secret "$GOTTH_BB_OIDC_CLIENT_SECRET_FILE" 65532
check_secret "$GOTTH_BB_ACTIVITY_CURSOR_KEYRING_FILE" 65532

paths="
$GOTTH_BB_CADDY_DATA_DIR
$GOTTH_BB_CADDY_CONFIG_DIR
$GOTTH_BB_POSTGRES_DATA_DIR
$GOTTH_BB_AUTHENTIK_POSTGRES_DATA_DIR
$GOTTH_BB_AUTHENTIK_DATA_DIR
$GOTTH_BB_AUTHENTIK_TEMPLATES_DIR
$GOTTH_BB_AUTHENTIK_CERTS_DIR
$GOTTH_BB_ENV_FILE
$GOTTH_BB_ABUSE_RULES_FILE
$GOTTH_BB_POSTGRES_MIGRATE_PASSWORD_FILE
$GOTTH_BB_POSTGRES_RUNTIME_PASSWORD_FILE
$GOTTH_BB_DATABASE_URL_FILE
$GOTTH_BB_OIDC_CLIENT_SECRET_FILE
$GOTTH_BB_ACTIVITY_CURSOR_KEYRING_FILE
$GOTTH_BB_AUTHENTIK_POSTGRES_PASSWORD_FILE
$GOTTH_BB_AUTHENTIK_SECRET_KEY_FILE
"
canonical_paths=''
old_ifs=$IFS
IFS='
'
for path in $paths; do
	[ -n "$path" ] || continue
	canonical=$(readlink -f "$path")
	case "
$canonical_paths
" in *"
$canonical
"*) fail "durable and secret paths overlap at $canonical" ;; esac
	canonical_paths="${canonical_paths}${canonical}
"
done
IFS=$old_ifs

expected_public=$GOTTH_BB_PUBLIC_BASE_URL
expected_issuer=$GOTTH_BB_AUTH_PUBLIC_BASE_URL/application/o/gotth-bb/
expected_registration=$GOTTH_BB_AUTH_PUBLIC_BASE_URL/if/flow/gotth-bb-enrollment/
unset APP_ENV PUBLIC_BASE_URL BASE_PATH OIDC_ISSUER_URL OIDC_CLIENT_ID REGISTRATION_URL
set -a
. "$GOTTH_BB_ENV_FILE"
set +a
[ "${PUBLIC_BASE_URL-}" = "$expected_public" ] || fail "app PUBLIC_BASE_URL differs from deployment"
[ "${BASE_PATH-}" = "" ] || fail "standalone deployment requires an empty BASE_PATH"
[ "${OIDC_ISSUER_URL-}" = "$expected_issuer" ] || fail "app OIDC issuer differs from dedicated Authentik"
[ "${OIDC_CLIENT_ID-}" = "gotth-bb" ] || fail "app OIDC client ID differs"
[ "${REGISTRATION_URL-}" = "$expected_registration" ] || fail "app registration URL differs from dedicated Authentik"
case "${APP_ENV-}:$expected_public" in
	production:https://*) ;;
	test:http://127.0.0.1:* | test:http://localhost:*) ;;
	*) fail "APP_ENV and public URL are not an admitted production or loopback-test pair" ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
docker compose --env-file "$deployment_env" --project-directory "$script_dir" -f "$script_dir/compose.yml" config --quiet
echo STANDALONE_PREFLIGHT_OK
