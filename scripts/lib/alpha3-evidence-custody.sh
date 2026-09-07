#!/usr/bin/env bash

# Capture exactly one committed source archive and prepare an isolated Go build
# environment. Callers remain responsible for checking the repository identity
# again after the measured process exits.
alpha3_remove_scratch() {
  if [ "$#" -ne 1 ]; then
    printf '%s\n' 'alpha3_remove_scratch requires one exact scratch path' >&2
    return 2
  fi
  case "$1" in
    /tmp/gotth-bb-alpha3-*.??????) ;;
    *) printf 'refusing unexpected Alpha.3 scratch path %s\n' "$1" >&2; return 2 ;;
  esac
  /usr/bin/chmod -R u+w -- "$1" 2>/dev/null || true
  /usr/bin/rm -rf -- "$1"
}

alpha3_capture_committed_source() {
  if [ "$#" -ne 1 ] || [ ! -d "$1" ]; then
    printf '%s\n' 'alpha3_capture_committed_source requires an existing private scratch directory' >&2
    return 2
  fi

  local scratch=$1
  ALPHA3_SOURCE_HEAD=$(/usr/bin/git rev-parse HEAD)
  ALPHA3_SOURCE_TREE=$(/usr/bin/git rev-parse 'HEAD^{tree}')
  if [ -n "$(/usr/bin/git status --porcelain=v1)" ]; then
    printf '%s\n' 'Alpha.3 evidence requires a clean committed source tree' >&2
    return 2
  fi

  ALPHA3_SOURCE_ARCHIVE="$scratch/source.tar"
  ALPHA3_SOURCE_ROOT="$scratch/source"
  ALPHA3_GO_BOOTSTRAP_BINARY=/usr/bin/go
  for directory in "$ALPHA3_SOURCE_ROOT" "$scratch/home" "$scratch/go-path" "$scratch/go-mod-cache" "$scratch/go-build-cache" "$scratch/go-tmp"; do
    /usr/bin/mkdir -m 0700 "$directory"
  done
  /usr/bin/git archive --format=tar --output="$ALPHA3_SOURCE_ARCHIVE" "$ALPHA3_SOURCE_HEAD"
  ALPHA3_SOURCE_ARCHIVE_SHA256=$(/usr/bin/sha256sum "$ALPHA3_SOURCE_ARCHIVE" | /usr/bin/cut -d' ' -f1)
  /usr/bin/tar -xf "$ALPHA3_SOURCE_ARCHIVE" -C "$ALPHA3_SOURCE_ROOT"
  ALPHA3_GO_BOOTSTRAP_SHA256=$(/usr/bin/sha256sum "$ALPHA3_GO_BOOTSTRAP_BINARY" | /usr/bin/cut -d' ' -f1)
  ALPHA3_GO_VERSION=$(alpha3_clean_go "$scratch" env GOVERSION)
  if [ "$ALPHA3_GO_VERSION" != go1.26.6 ]; then
    printf 'selected Go toolchain is %s, want go1.26.6\n' "$ALPHA3_GO_VERSION" >&2
    return 2
  fi
  ALPHA3_GO_ROOT=$(alpha3_clean_go "$scratch" env GOROOT)
  ALPHA3_GO_COMPILER_BINARY="$ALPHA3_GO_ROOT/bin/go"
  if [ ! -x "$ALPHA3_GO_COMPILER_BINARY" ]; then
    printf 'selected Go compiler is not executable: %s\n' "$ALPHA3_GO_COMPILER_BINARY" >&2
    return 2
  fi
  ALPHA3_GO_COMPILER_SHA256=$(/usr/bin/sha256sum "$ALPHA3_GO_COMPILER_BINARY" | /usr/bin/cut -d' ' -f1)
}

alpha3_clean_process() {
  if [ "$#" -lt 2 ]; then
    printf '%s\n' 'alpha3_clean_process requires scratch plus a command' >&2
    return 2
  fi
  local scratch=$1
  shift
  /usr/bin/env -i \
    HOME="$scratch/home" PATH=/usr/bin:/bin LANG=C.UTF-8 LC_ALL=C.UTF-8 TZ=UTC "$@"
}

alpha3_clean_go() {
  if [ "$#" -lt 2 ]; then
    printf '%s\n' 'alpha3_clean_go requires scratch plus a Go command' >&2
    return 2
  fi
  local scratch=$1
  shift
  alpha3_clean_process "$scratch" \
    GOENV=off GOWORK=off GOFLAGS= GOTOOLCHAIN=go1.26.6 GO111MODULE=on \
    GOOS=linux GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0 GOEXPERIMENT= GODEBUG= \
    GOPATH="$scratch/go-path" GOMODCACHE="$scratch/go-mod-cache" \
    GOCACHE="$scratch/go-build-cache" GOTMPDIR="$scratch/go-tmp" \
    GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
    GOPRIVATE= GONOSUMDB= GONOPROXY=none GOINSECURE= GOVCS='*:off' \
    GOAUTH=off GOTELEMETRY=off "$ALPHA3_GO_BOOTSTRAP_BINARY" "$@"
}

alpha3_compile_rerender_test() {
  if [ "$#" -ne 2 ]; then
    printf '%s\n' 'alpha3_compile_rerender_test requires scratch and output path' >&2
    return 2
  fi
  local scratch=$1
  local output=$2
  (
    cd "$ALPHA3_SOURCE_ROOT"
    alpha3_clean_go "$scratch" test -mod=readonly -tags=integration -c -o "$output" ./internal/rerender
  )
}
