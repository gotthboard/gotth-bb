#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/alpha3-evidence-custody.sh"

readonly test_scratch="$(mktemp -d /tmp/gotth-bb-alpha3-custody-test.XXXXXX)"
trap 'alpha3_remove_scratch "$test_scratch"' EXIT HUP INT TERM
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
