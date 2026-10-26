#!/usr/bin/env bash
# Build the CLI, TUI and GUI release binaries for one OS/architecture.
#
# Usage:
#   bash CICD/build-release.sh [GOOS] [GOARCH] [PLATFORM]
#
# Examples:
#   bash CICD/build-release.sh linux amd64 linux
#   bash CICD/build-release.sh windows amd64 windows
#   bash CICD/build-release.sh darwin arm64 macos
#
# The GUI (Fyne) needs cgo and a native compiler. The GitHub Actions runners
# provide that; locally install the platform dependencies described in
# CICD/README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GOOS_ARG="${1:-${GOOS:-$(go env GOOS)}}"
GOARCH_ARG="${2:-${GOARCH:-$(go env GOARCH)}}"
PLATFORM="${3:-}"

if [ -z "$PLATFORM" ]; then
  case "$GOOS_ARG" in
    darwin) PLATFORM="macos" ;;
    *) PLATFORM="$GOOS_ARG" ;;
  esac
fi

MODEL="${MODEL:-gemma4:31b-cloud}"
OUT="dist/release"
EXT=""
if [ "$GOOS_ARG" = "windows" ]; then
  EXT=".exe"
  export CC="${CC:-gcc}"
fi
if [ "$GOOS_ARG" = "darwin" ] && [ -z "${MACOSX_DEPLOYMENT_TARGET:-}" ]; then
  export MACOSX_DEPLOYMENT_TARGET=12.0
fi

LDFLAGS="-s -w -X noesis/internal/core.DefaultModel=$MODEL"
GUI_LDFLAGS="$LDFLAGS"
if [ "$GOOS_ARG" = "windows" ]; then
  GUI_LDFLAGS="$GUI_LDFLAGS -H windowsgui"
fi

mkdir -p "$OUT"

build() {
  local app="$1"
  local cgo="$2"
  local ldflags="$3"
  local target="$OUT/noesis-${app}-${PLATFORM}-${GOARCH_ARG}${EXT}"

  echo "→ $(basename "$target")"
  CGO_ENABLED="$cgo" GOOS="$GOOS_ARG" GOARCH="$GOARCH_ARG" \
    go build -trimpath -ldflags "$ldflags" -o "$target" "./cmd/noesis-${app}"
}

echo "noesis release build"
echo "  platform: ${PLATFORM}-${GOARCH_ARG} (GOOS=${GOOS_ARG}, GOARCH=${GOARCH_ARG})"
echo "  model:    ${MODEL}"
echo "  output:   ${OUT}/"
echo

build cli 0 "$LDFLAGS"
build tui 0 "$LDFLAGS"
build gui 1 "$GUI_LDFLAGS"

echo
echo "Done:"
ls -lh "$OUT"
