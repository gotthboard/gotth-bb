#!/usr/bin/env bash
set -euo pipefail

readonly expected_image='postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d'
readonly container_name="${GOTTH_BB_POSTGRES_CONTAINER:-gotth-bb-alpha3-totality-pg}"

if [ -z "${GOTTH_BB_TEST_DATABASE_URL:-}" ]; then
  printf '%s\n' 'GOTTH_BB_TEST_DATABASE_URL is required' >&2
  exit 2
fi
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
if [ "$image_ref" != "$expected_image" ] || [ "$image_id" != "sha256:${expected_image#*@sha256:}" ]; then
  printf 'unexpected PostgreSQL image: ref=%s id=%s\n' "$image_ref" "$image_id" >&2
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

{
  cat "$test_log"
  printf 'commit=%s\n' "$(git rev-parse HEAD)"
  printf 'tree=%s\n' "$(git rev-parse 'HEAD^{tree}')"
  printf 'source_archive_sha256=%s\n' "$(git archive --format=tar HEAD | sha256sum | cut -d' ' -f1)"
  printf 'environment=%s\n' "$(uname -srvmo)"
  printf 'go_version=%s\n' "$(go env GOVERSION)"
  printf 'gomaxprocs=4\n'
  printf 'postgres_container=%s\n' "$container_name"
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
