#!/usr/bin/env bash
# Build distribution binaries for the current operating system.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$(uname -s)" in
  Linux*)
    exec "$ROOT/scripts/build-linux.sh"
    ;;
  Darwin*)
    exec "$ROOT/scripts/build-macos.sh"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    exec "$ROOT/scripts/build-windows.sh"
    ;;
  *)
    echo "unsupported OS: $(uname -s)" >&2
    echo "use one of: scripts/build-linux.sh, scripts/build-macos.sh, scripts/build-windows.sh" >&2
    exit 1
    ;;
esac
