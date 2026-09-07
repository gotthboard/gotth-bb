#!/usr/bin/bash -p
set +x
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/alpha3-evidence-custody.sh"
readonly repository_root="$(cd -- "$script_dir/.." && pwd -P)"

readonly test_scratch="$(mktemp -d /tmp/gotth-bb-alpha3-custody-test.XXXXXX)"
trap 'alpha3_remove_scratch "$test_scratch"' EXIT HUP INT TERM
readonly startup_scratch="$test_scratch/startup"
/usr/bin/mkdir -m 0700 "$startup_scratch"
readonly bash_env_payload="$startup_scratch/bash-env.sh"
readonly bash_env_marker="$startup_scratch/bash-env-ran"
readonly dirname_marker="$startup_scratch/dirname-ran"
readonly sudo_marker="$startup_scratch/sudo-ran"
readonly docker_marker="$startup_scratch/docker-ran"
/usr/bin/printf 'unset BASH_ENV\n/usr/bin/printf "%%s\\n" BASH_ENV_PAYLOAD_EXECUTED\n: > %q\n' "$bash_env_marker" >"$bash_env_payload"

# Prove the unsupported direct-Bash shape really does execute BASH_ENV before
# the runner can reject it. This is why only the static launcher is canonical.
set +e
BASH_ENV="$bash_env_payload" /usr/bin/bash "$script_dir/verify-alpha3-rerender-performance.sh" \
  >"$startup_scratch/unsafe-direct.log" 2>&1
unsafe_status=$?
set -e
test "$unsafe_status" -ne 0
test -f "$bash_env_marker"
/usr/bin/grep -F 'BASH_ENV_PAYLOAD_EXECUTED' "$startup_scratch/unsafe-direct.log" >/dev/null
/usr/bin/grep -F 'unsupported direct invocation' "$startup_scratch/unsafe-direct.log" >/dev/null
/usr/bin/rm -f -- "$bash_env_marker"

readonly secret_sentinel='alpha3-secret-must-not-appear'
for mode in rerender population; do
  inner_log="$startup_scratch/canonical-$mode.log"
  set +e
  /usr/bin/env \
    BASH_ENV="$bash_env_payload" ENV="$bash_env_payload" CDPATH="$startup_scratch" \
    SHELLOPTS=xtrace BASHOPTS=extdebug LD_PRELOAD=/nonexistent/alpha3-preload.so \
    LD_AUDIT=/nonexistent/alpha3-audit.so LD_DEBUG=all \
    LD_LIBRARY_PATH=/nonexistent/alpha3-libraries GLIBC_TUNABLES=glibc.malloc.perturb=85 \
    'BASH_FUNC_dirname%%=() { /usr/bin/touch '"$dirname_marker"'; /usr/bin/printf "%s\n" /tmp; }' \
    'BASH_FUNC_sudo%%=() { /usr/bin/touch '"$sudo_marker"'; /usr/bin/printf "%s\n" spoofed; }' \
    'BASH_FUNC_docker%%=() { /usr/bin/touch '"$docker_marker"'; /usr/bin/printf "%s\n" spoofed; }' \
    GOTTH_BB_TEST_DATABASE_URL="postgres://$secret_sentinel@example.invalid/test?sslmode=disable" \
    GOTTH_BB_EVIDENCE_OUTPUT="$startup_scratch/$mode-evidence.txt" \
    GOTTH_BB_POSTGRES_CONTAINER=alpha3-custody-container-does-not-exist \
    "$script_dir/alpha3-evidence-launcher" "$mode" >"$inner_log" 2>&1
  canonical_status=$?
  set -e
  test "$canonical_status" -ne 0
  test ! -e "$bash_env_marker"
  test ! -e "$dirname_marker"
  test ! -e "$sudo_marker"
  test ! -e "$docker_marker"
  test ! -e "$startup_scratch/$mode-evidence.txt"
  ! /usr/bin/grep -F 'BASH_ENV_PAYLOAD_EXECUTED' "$inner_log" >/dev/null
  ! /usr/bin/grep -F "$secret_sentinel" "$inner_log" >/dev/null
  ! /usr/bin/grep -F 'spoofed' "$inner_log" >/dev/null
  /usr/bin/grep -F "alpha3_custody_attested mode=$mode" "$inner_log" >/dev/null
done

make_attack_repository() {
  if [ "$#" -ne 1 ]; then
    printf '%s\n' 'make_attack_repository requires one destination' >&2
    return 2
  fi
  /usr/bin/git -c advice.detachedHead=false clone -q --no-hardlinks "$repository_root" "$1"
}

require_pre_bash_rejection() {
  if [ "$#" -ne 3 ]; then
    printf '%s\n' 'require_pre_bash_rejection requires repository, case name, and expected diagnostic' >&2
    return 2
  fi
  local attack_repository=$1
  local case_name=$2
  local expected_diagnostic=$3
  local attack_log="$startup_scratch/attack-$case_name.log"
  local attack_evidence="$startup_scratch/attack-$case_name-evidence.txt"

  /usr/bin/rm -f -- "$bash_env_marker"
  set +e
  /usr/bin/env BASH_ENV="$bash_env_payload" ENV="$bash_env_payload" \
    GOTTH_BB_TEST_DATABASE_URL="postgres://$secret_sentinel@example.invalid/test?sslmode=disable" \
    GOTTH_BB_EVIDENCE_OUTPUT="$attack_evidence" \
    GOTTH_BB_POSTGRES_CONTAINER=alpha3-custody-container-does-not-exist \
    "$attack_repository/scripts/alpha3-evidence-launcher" rerender >"$attack_log" 2>&1
  local attack_status=$?
  set -e

  test "$attack_status" -ne 0
  /usr/bin/grep -F "$expected_diagnostic" "$attack_log" >/dev/null
  test ! -e "$bash_env_marker"
  test ! -e "$attack_evidence"
  ! /usr/bin/grep -F 'BASH_ENV_PAYLOAD_EXECUTED' "$attack_log" >/dev/null
  ! /usr/bin/grep -F 'alpha3_custody_attested' "$attack_log" >/dev/null
  ! /usr/bin/grep -F "$secret_sentinel" "$attack_log" >/dev/null
}

# Every live Bash input is byte-attested before Bash starts. Git index flags
# cannot turn a modified runner or shared library into accepted evidence.
readonly assume_repository="$test_scratch/assume-repository"
make_attack_repository "$assume_repository"
(
  cd "$assume_repository"
  /usr/bin/printf '%s\n' '# hidden runner mutation' >>scripts/verify-alpha3-rerender-performance.sh
  /usr/bin/git update-index --assume-unchanged scripts/verify-alpha3-rerender-performance.sh
  test -z "$(/usr/bin/git status --porcelain=v1 --untracked-files=all)"
)
require_pre_bash_rejection "$assume_repository" assume-unchanged 'critical evidence file identity mismatch: scripts/verify-alpha3-rerender-performance.sh'

readonly assume_flag_repository="$test_scratch/assume-flag-repository"
make_attack_repository "$assume_flag_repository"
(
  cd "$assume_flag_repository"
  /usr/bin/git update-index --assume-unchanged scripts/verify-alpha3-rerender-performance.sh
)
require_pre_bash_rejection "$assume_flag_repository" assume-unchanged-exact 'tracked-file index flags may hide mutations:'

readonly skip_repository="$test_scratch/skip-repository"
make_attack_repository "$skip_repository"
(
  cd "$skip_repository"
  /usr/bin/git update-index --skip-worktree scripts/lib/alpha3-evidence-custody.sh
  /usr/bin/printf '%s\n' '# hidden library mutation' >>scripts/lib/alpha3-evidence-custody.sh
  test -z "$(/usr/bin/git status --porcelain=v1 --untracked-files=all)"
)
require_pre_bash_rejection "$skip_repository" skip-worktree 'critical evidence file identity mismatch: scripts/lib/alpha3-evidence-custody.sh'

readonly skip_flag_repository="$test_scratch/skip-flag-repository"
make_attack_repository "$skip_flag_repository"
(
  cd "$skip_flag_repository"
  /usr/bin/git update-index --skip-worktree scripts/lib/alpha3-evidence-custody.sh
)
require_pre_bash_rejection "$skip_flag_repository" skip-worktree-exact 'tracked-file index flags may hide mutations:'

# A repository-local fsmonitor is rejected before either Git status or Bash can
# execute it. The runner mutation independently remains caught by byte identity.
readonly fsmonitor_repository="$test_scratch/fsmonitor-repository"
make_attack_repository "$fsmonitor_repository"
readonly fsmonitor_hook="$fsmonitor_repository/.git/hostile-fsmonitor"
readonly fsmonitor_marker="$startup_scratch/fsmonitor-ran"
/usr/bin/printf '#!/usr/bin/bash\n: > %q\nexit 0\n' "$fsmonitor_marker" >"$fsmonitor_hook"
/usr/bin/chmod 0700 "$fsmonitor_hook"
(
  cd "$fsmonitor_repository"
  /usr/bin/git config core.fsmonitor "$fsmonitor_hook"
  /usr/bin/printf '%s\n' '# fsmonitor-hidden runner mutation' >>scripts/verify-alpha3-rerender-performance.sh
)
require_pre_bash_rejection "$fsmonitor_repository" hostile-fsmonitor-runner 'critical evidence file identity mismatch: scripts/verify-alpha3-rerender-performance.sh'
test ! -e "$fsmonitor_marker"

readonly fsmonitor_config_repository="$test_scratch/fsmonitor-config-repository"
make_attack_repository "$fsmonitor_config_repository"
readonly fsmonitor_config_hook="$fsmonitor_config_repository/.git/hostile-fsmonitor"
/usr/bin/printf '#!/usr/bin/bash\n: > %q\nexit 0\n' "$fsmonitor_marker" >"$fsmonitor_config_hook"
/usr/bin/chmod 0700 "$fsmonitor_config_hook"
(
  cd "$fsmonitor_config_repository"
  /usr/bin/git config core.fsmonitor "$fsmonitor_config_hook"
)
require_pre_bash_rejection "$fsmonitor_config_repository" hostile-fsmonitor-config 'local Git configuration may alter source custody: core.fsmonitor'
test ! -e "$fsmonitor_marker"

# Per-repository attributes have higher archive precedence than committed
# attributes and therefore cannot participate in a reviewed source capture.
readonly attributes_repository="$test_scratch/attributes-repository"
make_attack_repository "$attributes_repository"
/usr/bin/printf '%s\n' '* export-subst' >"$attributes_repository/.git/info/attributes"
require_pre_bash_rejection "$attributes_repository" info-attributes 'Git info/attributes must be absent or an empty regular file'

if /usr/bin/readelf -l -- "$script_dir/alpha3-evidence-launcher" | /usr/bin/grep -q 'INTERP'; then
  printf '%s\n' 'committed Alpha.3 evidence launcher is dynamically linked' >&2
  exit 1
fi

# Rebuild the launcher only from the committed archive under the same empty,
# exact Go environment used for evidence compilation, then require exact bytes.
(
  cd "$repository_root"
  readonly launcher_capture="$test_scratch/launcher-capture"
  /usr/bin/mkdir -m 0700 "$launcher_capture"
  alpha3_capture_committed_source "$launcher_capture"
  readonly rebuilt_launcher="$launcher_capture/rebuilt-launcher"
  alpha3_compile_evidence_launcher "$launcher_capture" "$rebuilt_launcher"
  /usr/bin/cmp "$ALPHA3_SOURCE_ROOT/scripts/alpha3-evidence-launcher" "$rebuilt_launcher"
)

readonly repository="$test_scratch/repository"
/usr/bin/mkdir -p "$repository/internal/rerender"
(
  cd "$repository"
  /usr/bin/git init -q
  /usr/bin/printf '%s\n' 'module example.test/custody' 'go 1.26.0' >go.mod
  /usr/bin/printf '%s\n' 'package rerender' '' 'import (' '  "os"' '  "testing"' ')' '' 'func TestCommitted(t *testing.T) {' '  for _, key := range []string{"GOWORK", "GOENV", "GOFLAGS", "GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "GOTMPDIR", "GOTOOLCHAIN", "GOOS", "GOARCH", "CGO_ENABLED", "CC", "GODEBUG"} {' '    if value := os.Getenv(key); value != "" {' '      t.Fatalf("ambient %s survived with value %q", key, value)' '    }' '  }' '}' >internal/rerender/committed_test.go
  /usr/bin/printf '%s\n' 'internal/rerender/ignored_injection_test.go' >.gitignore
  /usr/bin/git add go.mod .gitignore internal/rerender/committed_test.go
  /usr/bin/git -c user.name=Custody -c user.email=custody@example.test commit -q -m base
  /usr/bin/printf '%s\n' 'internal/rerender/excluded_injection_test.go' >>.git/info/exclude
  /usr/bin/printf '%s\n' 'package rerender' 'this cannot compile' >internal/rerender/ignored_injection_test.go
  /usr/bin/printf '%s\n' 'package rerender' 'this also cannot compile' >internal/rerender/excluded_injection_test.go
  test -z "$(/usr/bin/git status --porcelain=v1)"

  export GOWORK=/hostile/workspace.work
  export GOENV=/hostile/go.env
  export GOFLAGS='-overlay=/hostile/overlay.json'
  export GOROOT=/hostile/goroot
  export GOPATH=/hostile/gopath
  export GOMODCACHE=/hostile/modcache
  export GOCACHE=/hostile/buildcache
  export GOTMPDIR=/hostile/tmp
  export GOTOOLCHAIN=path
  export GOOS=plan9
  export GOARCH=arm64
  export CGO_ENABLED=1
  export CC=/hostile/cc
  export GODEBUG=installgoroot=all
  readonly capture="$test_scratch/capture"
  /usr/bin/mkdir -m 0700 "$capture"
  alpha3_capture_committed_source "$capture"
  test ! -e "$ALPHA3_SOURCE_ROOT/internal/rerender/ignored_injection_test.go"
  test ! -e "$ALPHA3_SOURCE_ROOT/internal/rerender/excluded_injection_test.go"
  if alpha3_clean_go "$capture" test -mod=readonly ./internal/rerender >/dev/null 2>&1; then
    printf '%s\n' 'contaminated live package unexpectedly compiled' >&2
    exit 1
  fi
  readonly test_binary="$capture/custody.test"
  alpha3_compile_rerender_test "$capture" "$test_binary"
  alpha3_clean_process "$capture" "$test_binary" -test.run '^TestCommitted$' -test.count=1
)
