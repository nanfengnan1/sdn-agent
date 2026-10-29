#!/bin/sh
# Linux + BuildKit. Build only: no deployment, checkout mutation or push.
set -eu
agent_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VPP_IMAGE=${VPP_IMAGE:-alpine/vpp:sdn-base}
FRR_IMAGE=${FRR_IMAGE:-alpine/frr:sdn-base}
AGENT_IMAGE=${AGENT_IMAGE:-alpine/sdn-agent:vpp-dev}
SNORT_IMAGE=${SNORT_IMAGE:-alpine/snort:sdn-base}
SNORT_REF=${SNORT_REF:-3.12.2.0}
SOURCE_REVISION=${SOURCE_REVISION:-$(git -C "$agent_dir" rev-parse HEAD 2>/dev/null || printf unknown)}
if ! git -C "$agent_dir" diff --quiet HEAD -- 2>/dev/null; then
  SOURCE_REVISION="$SOURCE_REVISION-dirty"
fi
BUILD_JOBS=${BUILD_JOBS:-12}
ALPINE_VERSION=${ALPINE_VERSION:-3.22}
BUILD_BASES=${BUILD_BASES:-0}
BUILD_SNORT=${BUILD_SNORT:-$BUILD_BASES}
VPP_REPOSITORY=${VPP_REPOSITORY:-https://github.com/nanfengnan1/vpp.git}
FRR_REPOSITORY=${FRR_REPOSITORY:-https://github.com/nanfengnan1/frr.git}
case "$BUILD_BASES" in 0|1) ;; *) printf '%s\n' 'BUILD_BASES must be 0 or 1' >&2; exit 1;; esac
case "$BUILD_JOBS" in ''|*[!0-9]*) printf '%s\n' 'BUILD_JOBS must be a positive integer' >&2; exit 1;; esac
test "$BUILD_JOBS" -gt 0 || { printf '%s\n' 'BUILD_JOBS must be a positive integer' >&2; exit 1; }

case "$BUILD_SNORT" in 0|1) ;; *) printf '%s\n' 'BUILD_SNORT must be 0 or 1' >&2; exit 1;; esac
build_snort() {
if [ "$BUILD_SNORT" = 1 ]; then
  docker build --progress=plain --build-arg "SNORT_REF=$SNORT_REF" --build-arg "BUILD_JOBS=$BUILD_JOBS" \
    --build-arg "ALPINE_VERSION=$ALPINE_VERSION" \
    -f "$agent_dir/docker/snort/Dockerfile" -t "$SNORT_IMAGE" "$agent_dir/docker"
fi
}

if [ "$BUILD_BASES" = 1 ]; then
  # Validate both inputs before starting either expensive build.
  if [ -n "${VPP_SOURCE:-}" ]; then
    test -d "$VPP_SOURCE" || { printf '%s\n' 'VPP_SOURCE is not a directory' >&2; exit 1; }
    test -d "$VPP_SOURCE/.git" || { printf '%s\n' 'VPP_SOURCE must be a full Git checkout with a .git directory (linked worktrees are not supported)' >&2; exit 1; }
    VPP_MODE=local
    VPP_REF=${VPP_REF:-local}
  else
    VPP_REF=${VPP_REF:-sdn/v26.06}
    VPP_MODE=git
  fi
  if [ -n "${FRR_SOURCE:-}" ]; then
    test -d "$FRR_SOURCE" || { printf '%s\n' 'FRR_SOURCE is not a directory' >&2; exit 1; }
    test -d "$FRR_SOURCE/.git" || { printf '%s\n' 'FRR_SOURCE must be a full Git checkout with a .git directory (linked worktrees are not supported)' >&2; exit 1; }
    FRR_MODE=local
    FRR_REF=${FRR_REF:-local}
  else
    FRR_MODE=git
    FRR_REF=${FRR_REF:-frr-10.7.0}
  fi
  build_snort
  set --
  if [ "$FRR_MODE" = local ]; then
    set -- --build-context "frr_source=$FRR_SOURCE" --build-context "frr_git=$FRR_SOURCE/.git"
  fi
  docker build --progress=plain "$@" \
    --build-arg "FRR_SOURCE_MODE=$FRR_MODE" --build-arg "FRR_REPOSITORY=$FRR_REPOSITORY" \
    --build-arg "FRR_REF=$FRR_REF" --build-arg "BUILD_JOBS=$BUILD_JOBS" \
    --build-arg "ALPINE_VERSION=$ALPINE_VERSION" --build-arg "SOURCE_CACHE_BUST=${SOURCE_CACHE_BUST:-0}" \
    -f "$agent_dir/docker/vpp/Dockerfile.frr" -t "$FRR_IMAGE" "$agent_dir/docker"
  set --
  if [ "$VPP_MODE" = local ]; then
    set -- --build-context "vpp_source=$VPP_SOURCE" --build-context "vpp_git=$VPP_SOURCE/.git"
  fi
  docker build --progress=plain "$@" \
    --build-arg "SNORT_IMAGE=$SNORT_IMAGE" --build-arg "VPP_SOURCE_MODE=$VPP_MODE" --build-arg "VPP_REPOSITORY=$VPP_REPOSITORY" \
    --build-arg "VPP_REF=$VPP_REF" --build-arg "BUILD_JOBS=$BUILD_JOBS" \
    --build-arg "ALPINE_VERSION=$ALPINE_VERSION" --build-arg "SOURCE_CACHE_BUST=${SOURCE_CACHE_BUST:-0}" \
    -f "$agent_dir/docker/vpp/Dockerfile.base" -t "$VPP_IMAGE" "$agent_dir/docker"
else
  build_snort
fi
docker build --progress=plain \
  --build-arg "SOURCE_REVISION=$SOURCE_REVISION" --build-arg "VPP_IMAGE=$VPP_IMAGE" --build-arg "FRR_IMAGE=$FRR_IMAGE" --build-arg "SNORT_IMAGE=$SNORT_IMAGE" \
  -f "$agent_dir/docker/Dockerfile.vpp" -t "$AGENT_IMAGE" "$(dirname "$agent_dir")"
