#!/usr/bin/bash -p
set +x
set -euo pipefail

if [ -z "${GOTTH_BB_EVIDENCE_LAUNCHER_PID:-}" ] || [ -z "${GOTTH_BB_EVIDENCE_LAUNCHER_PATH:-}" ]; then
  printf '%s\n' 'unsupported direct invocation; use scripts/alpha3-evidence-launcher rerender' >&2
  exit 2
fi
case "$GOTTH_BB_EVIDENCE_LAUNCHER_PID" in
  ''|*[!0-9]*) printf '%s\n' 'invalid Alpha.3 evidence launcher PID' >&2; exit 2 ;;
esac
if [ "$PPID" != "$GOTTH_BB_EVIDENCE_LAUNCHER_PID" ]; then
  printf '%s\n' 'Alpha.3 evidence launcher is not the direct parent' >&2
  exit 2
fi
readonly evidence_launcher_path="$(/usr/bin/readlink -f -- "/proc/$PPID/exe")"
readonly expected_launcher_path="$(/usr/bin/readlink -f -- "${BASH_SOURCE[0]%/*}/alpha3-evidence-launcher")"
readonly attested_launcher_path="$(/usr/bin/readlink -f -- "$GOTTH_BB_EVIDENCE_LAUNCHER_PATH")"
if [ "$evidence_launcher_path" != "$expected_launcher_path" ] || [ "$attested_launcher_path" != "$expected_launcher_path" ] || [ "$(/usr/bin/basename -- "$evidence_launcher_path")" != alpha3-evidence-launcher ]; then
  printf '%s\n' 'Alpha.3 evidence launcher executable does not match its attestation' >&2
  exit 2
fi
if /usr/bin/readelf -l -- "$evidence_launcher_path" | /usr/bin/grep -q 'INTERP'; then
  printf '%s\n' 'Alpha.3 evidence launcher must be statically linked' >&2
  exit 2
fi
readonly evidence_launcher_sha256="$(/usr/bin/sha256sum "/proc/$PPID/exe" | /usr/bin/cut -d' ' -f1)"
readonly expected_launcher_sha256=b5b869a7ad2bbe1bd6969c8428621dc8644a84b89193d2397ec9f7342b47d859
if [ "$evidence_launcher_sha256" != "$expected_launcher_sha256" ]; then
  printf '%s\n' 'Alpha.3 evidence launcher digest does not match the admitted binary' >&2
  exit 2
fi
if ! shopt -q -o privileged || [[ $- == *x* ]]; then
  printf '%s\n' 'Alpha.3 evidence Bash must be privileged with xtrace disabled' >&2
  exit 2
fi
for injection_variable in BASH_ENV ENV CDPATH LD_PRELOAD LD_LIBRARY_PATH; do
  if [ -n "${!injection_variable-}" ]; then
    printf 'refusing ambient process injection variable %s\n' "$injection_variable" >&2
    exit 2
  fi
done
if /usr/bin/env | /usr/bin/grep -q '^BASH_FUNC_'; then
  printf '%s\n' 'refusing imported shell functions' >&2
  exit 2
fi
PATH=/usr/bin:/bin
export PATH
readonly PATH

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/alpha3-evidence-custody.sh"

readonly expected_image='postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d'
readonly container_name="${GOTTH_BB_POSTGRES_CONTAINER:-gotth-bb-alpha3-totality-pg}"

if [ -z "${GOTTH_BB_TEST_DATABASE_URL:-}" ]; then
  printf '%s\n' 'GOTTH_BB_TEST_DATABASE_URL is required' >&2
  exit 2
fi
readonly evidence_output="${GOTTH_BB_EVIDENCE_OUTPUT:-}"
case "$evidence_output" in
  /*) ;;
  *) printf '%s\n' 'GOTTH_BB_EVIDENCE_OUTPUT must name a new absolute evidence file' >&2; exit 2 ;;
esac
if [ -e "$evidence_output" ] || [ -L "$evidence_output" ] || [ ! -d "$(dirname -- "$evidence_output")" ]; then
  printf '%s\n' 'GOTTH_BB_EVIDENCE_OUTPUT must name a new file in an existing directory' >&2
  exit 2
fi

evidence_scratch=$(mktemp -d /tmp/gotth-bb-alpha3-performance.XXXXXX)
trap 'alpha3_remove_scratch "$evidence_scratch"' EXIT HUP INT TERM
alpha3_capture_committed_source "$evidence_scratch"
readonly source_head_before=$ALPHA3_SOURCE_HEAD
readonly source_tree_before=$ALPHA3_SOURCE_TREE
readonly source_archive_before=$ALPHA3_SOURCE_ARCHIVE_SHA256

image_ref=$(sudo -n docker inspect --format '{{.Config.Image}}' "$container_name")
image_id=$(sudo -n docker inspect --format '{{.Image}}' "$container_name")
container_running=$(sudo -n docker inspect --format '{{.State.Running}}' "$container_name")
published_endpoint=$(sudo -n docker port "$container_name" 5432/tcp)
if [ "$image_ref" != "$expected_image" ] || [ "$image_id" != "sha256:${expected_image#*@sha256:}" ] || [ "$container_running" != true ]; then
  printf 'unexpected PostgreSQL image: ref=%s id=%s\n' "$image_ref" "$image_id" >&2
  exit 2
fi
if [[ ! "$published_endpoint" =~ ^127\.0\.0\.1:[0-9]+$ ]]; then
  printf 'PostgreSQL container must publish exactly one loopback endpoint, got %s\n' "$published_endpoint" >&2
  exit 2
fi
readonly expected_database_host=${published_endpoint%:*}
readonly expected_database_port=${published_endpoint#*:}
container_system_identifier=$(sudo -n docker exec "$container_name" sh -ceu 'exec psql -XAt --dbname "$POSTGRES_DB" --username "$POSTGRES_USER" -c "SELECT system_identifier FROM pg_control_system()"')
if [[ ! "$container_system_identifier" =~ ^[0-9]+$ ]]; then
  printf 'inspected container returned invalid PostgreSQL system identifier %s\n' "$container_system_identifier" >&2
  exit 2
fi
readonly test_binary="$evidence_scratch/rerender-performance.test"
readonly test_log="$evidence_scratch/result.log"
readonly transcript="$evidence_scratch/evidence.txt"

alpha3_compile_rerender_test "$evidence_scratch" "$test_binary"
alpha3_clean_process "$evidence_scratch" GOTTH_BB_RUN_PERFORMANCE=1 GOTTH_BB_TEST_DATABASE_URL="$GOTTH_BB_TEST_DATABASE_URL" \
  GOTTH_BB_EXPECTED_DATABASE_HOST="$expected_database_host" GOTTH_BB_EXPECTED_DATABASE_PORT="$expected_database_port" \
  GOTTH_BB_EXPECTED_DATABASE_SYSTEM_IDENTIFIER="$container_system_identifier" GOMAXPROCS=4 "$test_binary" \
  -test.run '^TestMaximumCompatibilityBatchPerformanceOnPostgreSQL17$' \
  -test.count=1 -test.v >"$test_log" 2>&1 &
test_pid=$!
peak_rss_kib=0
while kill -0 "$test_pid" 2>/dev/null; do
  current_rss_kib=$(grep '^VmRSS:' "/proc/$test_pid/status" 2>/dev/null | tr -cd '0-9' || true)
  case "$current_rss_kib" in
    ''|*[!0-9]*) ;;
    *)
      if [ "$current_rss_kib" -gt "$peak_rss_kib" ]; then
        peak_rss_kib=$current_rss_kib
      fi
      ;;
  esac
  sleep 0.05
done
set +e
wait "$test_pid"
test_status=$?
set -e

source_head_after=$(/usr/bin/git rev-parse HEAD)
source_tree_after=$(/usr/bin/git rev-parse 'HEAD^{tree}')
source_archive_after=$(/usr/bin/git archive --format=tar HEAD | /usr/bin/sha256sum | /usr/bin/cut -d' ' -f1)
if [ -n "$(/usr/bin/git status --porcelain=v1)" ] || [ "$source_head_after" != "$source_head_before" ] || [ "$source_tree_after" != "$source_tree_before" ] || [ "$source_archive_after" != "$source_archive_before" ]; then
  printf '%s\n' 'source identity changed during performance evidence run' >&2
  exit 2
fi

{
  cat "$test_log"
  printf 'source_head_before=%s\n' "$source_head_before"
  printf 'source_tree_before=%s\n' "$source_tree_before"
  printf 'source_archive_sha256_before=%s\n' "$source_archive_before"
  printf 'source_clean_before=true\n'
  printf 'source_head_after=%s\n' "$source_head_after"
  printf 'source_tree_after=%s\n' "$source_tree_after"
  printf 'source_archive_sha256_after=%s\n' "$source_archive_after"
  printf 'source_clean_after=true\n'
  printf 'source_execution_root=%s\n' "$ALPHA3_SOURCE_ROOT"
  printf 'source_execution_kind=extracted_captured_git_archive\n'
  printf 'evidence_launcher_path=%s\n' "$evidence_launcher_path"
  printf 'evidence_launcher_sha256=%s\n' "$evidence_launcher_sha256"
  printf 'evidence_launcher_linkage=static-linux-amd64\n'
  printf 'evidence_shell=/usr/bin/bash;--noprofile;--norc;-p;environment-allowlist\n'
  printf 'environment=%s\n' "$(uname -srvmo)"
  printf 'go_bootstrap_binary=%s\n' "$ALPHA3_GO_BOOTSTRAP_BINARY"
  printf 'go_bootstrap_sha256=%s\n' "$ALPHA3_GO_BOOTSTRAP_SHA256"
  printf 'go_compiler_binary=%s\n' "$ALPHA3_GO_COMPILER_BINARY"
  printf 'go_compiler_sha256=%s\n' "$ALPHA3_GO_COMPILER_SHA256"
  printf 'go_version=%s\n' "$ALPHA3_GO_VERSION"
  printf 'go_environment=env-i;GOENV=off;GOWORK=off;GOFLAGS=;GOTOOLCHAIN=go1.26.6;CGO_ENABLED=0;GOOS=linux;GOARCH=amd64;GOAMD64=v1;isolated-caches\n'
  printf 'gomaxprocs=4\n'
  printf 'postgres_container=%s\n' "$container_name"
  printf 'postgres_container_running=%s\n' "$container_running"
  printf 'postgres_published_endpoint=%s\n' "$published_endpoint"
  printf 'postgres_system_identifier=%s\n' "$container_system_identifier"
  printf 'postgres_image_ref=%s\n' "$image_ref"
  printf 'postgres_image_id=%s\n' "$image_id"
  printf 'postgres_version=%s\n' "$(sudo -n docker exec "$container_name" postgres --version)"
  printf 'sampled_peak_rss_kib=%s\n' "$peak_rss_kib"
} >"$transcript"
cat "$transcript"
umask 022
set -o noclobber
if ! cat "$transcript" >"$evidence_output"; then
  printf 'cannot preserve evidence at %s\n' "$evidence_output" >&2
  exit 2
fi
set +o noclobber

if [ "$test_status" -ne 0 ]; then
  exit "$test_status"
fi
grep -F 'preserved=100 exact_html=100 converted=100' "$transcript" >/dev/null
grep -F 'sql_server_identity version_num=170010 ' "$transcript" | grep -F " system_identifier=$container_system_identifier" >/dev/null
