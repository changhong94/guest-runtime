#!/usr/bin/env bash
#
# Build the e2b guest agent (envd) into $BINDIR. envd is injected into
# sandbox-runtime.bundle at /opt/sandbox-runtime/bin/envd (done by
# guest-runtime's `make sandbox-runtime`), where sandbox-init
# auto-bind-mounts it into the guest. It is treated as a TARGET-ARCH binary
# (the guest's arch), built CGO-free so GOARCH alone handles cross-compilation.
#
# Source comes from the e2b-dev/runtime release tarball (pinned by tag in the URL),
# fetched + extracted via the shared tarball/src cache like the other deps.
#
# Inputs (env):
#   ENVD_TARBALL          URL or local path; supports "url#filename" form.
#                         Default: e2b-dev/runtime 2026.22 github archive.
#   ENVD_TARBALL_SHA256   Optional expected SHA256. Empty → skip verify.
#   BUILD_DIR             Per-arch build directory (e.g. build/x86_64).
#   BINDIR                Per-arch bin directory (e.g. bin/x86_64).
#   ENVD_SRC              Source tree to extract into — arch-neutral and shared
#                         (Go: GOARCH picks the target). Default: build/src/e2b-infra
#                         (i.e. $BUILD_DIR/../src/e2b-infra).
#   TARBALL_CACHE         Optional shared tarball cache (default $BUILD_DIR/tarball).
#   GO_ARCH               Go GOARCH for the target (amd64 | arm64).
#   KUASAR_BUILD_JOBS      Positive task CPU budget, independent of ENVD_GOFLAGS.
#
# envd's go.mod pins a newer Go toolchain (e.g. `go 1.26.3`); with GOTOOLCHAIN=auto
# the Go command fetches it on demand. That toolchain download requires the checksum
# database — keep GOSUMDB enabled (Go refuses to download+run a toolchain when
# GOSUMDB=off). Toolchain + module fetches use the ambient Go environment.
#
# Always validate patched sources and invoke Go, even when the output exists.
# Go's content-addressed build cache handles unchanged inputs; file existence
# alone cannot prove that an envd binary contains the current runtime patches.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/common.sh"

: "${ENVD_TARBALL:=https://codeload.github.com/e2b-dev/runtime/tar.gz/refs/tags/2026.22#e2b-runtime-2026.22.tar.gz}"
: "${ENVD_TARBALL_SHA256:=}"
: "${BUILD_DIR:=$(pwd)/build}"
: "${BINDIR:=$(pwd)/bin}"
: "${ENVD_SRC:=$BUILD_DIR/../src/e2b-infra}"
: "${GO_ARCH:=$(go env GOARCH 2>/dev/null || echo amd64)}"
build_args=()
if [ "${KUASAR_BUILD_JOBS+x}" ]; then
    [[ "$KUASAR_BUILD_JOBS" =~ ^[1-9][0-9]*$ ]] || die "KUASAR_BUILD_JOBS must be a positive integer"
    build_args+=(-p "$KUASAR_BUILD_JOBS")
fi

out_bin="$BINDIR/envd"

require_cmd go tar patch

tarball="$(resolve_tarball "$ENVD_TARBALL" "$ENVD_TARBALL_SHA256")"
extract_tarball "$tarball" "$ENVD_SRC" >/dev/null

envd_dir="$ENVD_SRC/packages/envd"
[ -f "$envd_dir/main.go" ] || die "$envd_dir/main.go not found (upstream layout changed?)"

# Keep command policy identical while avoiding the sh/ionice/nice exec chain.
# The helper is built and packaged by guest-runtime alongside envd.
if ! grep -Fq '"/opt/sandbox-runtime/bin/exec-wrapper"' "$envd_dir/internal/services/process/handler/handler.go"; then
    patch -d "$envd_dir" -p1 < "$script_dir/envd-patches/0001-runtime-exec-policy-wrapper.patch"
fi

if ! grep -Fq 'X-Kuasar-Init-Timing' "$envd_dir/internal/api/init.go"; then
    patch -d "$envd_dir" -p1 < "$script_dir/envd-patches/0002-init-stage-timing.patch"
fi

if ! grep -Fq 'listenLocalInit' "$envd_dir/main.go"; then
    patch -d "$envd_dir" -p1 < "$script_dir/envd-patches/0003-private-local-init.patch"
fi

if ! grep -Fq 'Non-Firecracker guests have no MMDS endpoint' "$envd_dir/internal/api/init.go"; then
    patch -d "$envd_dir" -p1 < "$script_dir/envd-patches/0004-skip-non-fc-mmds-poll.patch"
fi

log "go build envd (GOARCH=$GO_ARCH, CGO disabled)"
mkdir -p "$BINDIR"
staged_bin="$(mktemp "$BINDIR/.envd.XXXXXX")"
trap 'rm -f "$staged_bin"' EXIT
(
    cd "$envd_dir"
    GOWORK=off GOOS=linux GOARCH="$GO_ARCH" CGO_ENABLED=0 \
        GOFLAGS="${ENVD_GOFLAGS:--mod=mod}" \
        go build "${build_args[@]}" -trimpath -ldflags "-s -w" -o "$staged_bin" .
)

chmod 0755 "$staged_bin"
mv -f "$staged_bin" "$out_bin"
trap - EXIT

log "built $out_bin"
file "$out_bin" 2>&1 | head -1 || true
