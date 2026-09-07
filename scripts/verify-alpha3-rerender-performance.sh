#!/usr/bin/env bash
set -euo pipefail

readonly expected_image='postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d'
readonly container_name="${GOTTH_BB_POSTGRES_CONTAINER:-gotth-bb-alpha3-totality-pg}"

if [ -z "${GOTTH_BB_TEST_DATABASE_URL:-}" ]; then
  printf '%s\n' 'GOTTH_BB_TEST_DATABASE_URL is required' >&2
  exit 2
fi
readonly source_head_before="$(git rev-parse HEAD)"
readonly source_tree_before="$(git rev-parse 'HEAD^{tree}')"
readonly source_archive_before="$(git archive --format=tar HEAD | sha256sum | cut -d' ' -f1)"
if [ -n "$(git status --porcelain=v1)" ]; then
  printf '%s\n' 'performance evidence requires a clean committed source tree' >&2
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
case "$GOTTH_BB_TEST_DATABASE_URL" in
  postgres://*|postgresql://*) ;;
  *) printf '%s\n' 'database URL must use the postgres or postgresql scheme' >&2; exit 2 ;;
esac
database_authority=${GOTTH_BB_TEST_DATABASE_URL#*://}
if [ "$database_authority" = "$GOTTH_BB_TEST_DATABASE_URL" ] || [ "$database_authority" = "${database_authority#*/}" ]; then
  printf '%s\n' 'database URL must contain an authority and database path' >&2
  exit 2
fi
database_authority=${database_authority%%/*}
database_endpoint=${database_authority##*@}
if [ "$database_endpoint" != "$published_endpoint" ]; then
  printf 'database URL endpoint does not match inspected container endpoint %s\n' "$published_endpoint" >&2
  exit 2
fi

evidence_scratch=$(mktemp -d "${TMPDIR:-/tmp}/gotth-bb-alpha3-performance.XXXXXX")
trap 'rm -rf -- "$evidence_scratch"' EXIT HUP INT TERM
readonly test_binary="$evidence_scratch/rerender-performance.test"
readonly test_log="$evidence_scratch/result.log"
readonly transcript="$evidence_scratch/evidence.txt"

GOMAXPROCS=4 go test -mod=readonly -tags=integration -c -o "$test_binary" ./internal/rerender
GOTTH_BB_RUN_PERFORMANCE=1 GOTTH_BB_TEST_DATABASE_URL="$GOTTH_BB_TEST_DATABASE_URL" GOMAXPROCS=4 "$test_binary" \
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

source_head_after=$(git rev-parse HEAD)
source_tree_after=$(git rev-parse 'HEAD^{tree}')
source_archive_after=$(git archive --format=tar HEAD | sha256sum | cut -d' ' -f1)
if [ -n "$(git status --porcelain=v1)" ] || [ "$source_head_after" != "$source_head_before" ] || [ "$source_tree_after" != "$source_tree_before" ] || [ "$source_archive_after" != "$source_archive_before" ]; then
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
  printf 'environment=%s\n' "$(uname -srvmo)"
  printf 'go_version=%s\n' "$(go env GOVERSION)"
  printf 'gomaxprocs=4\n'
  printf 'postgres_container=%s\n' "$container_name"
  printf 'postgres_container_running=%s\n' "$container_running"
  printf 'postgres_published_endpoint=%s\n' "$published_endpoint"
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
grep -F 'sql_server_identity version_num=170010 ' "$transcript" >/dev/null
