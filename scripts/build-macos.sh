#!/usr/bin/env bash
# Build distribution binaries for macOS.
# Run this on macOS: the Fyne GUI needs cgo and the macOS SDK.
# For a proper .app bundle use: fyne package -os darwin -name noesis-gui
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

MODEL="${MODEL:-gemma4:31b-cloud}"
TRANSCRIBE_MODEL="${TRANSCRIBE_MODEL:-whisper}"
ARCH="${GOARCH:-$(go env GOARCH)}"
OUT="dist/darwin-$ARCH"
LDFLAGS="-s -w -X noesis/internal/core.DefaultModel=$MODEL -X noesis/internal/core.DefaultTranscribeModel=$TRANSCRIBE_MODEL"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "warning: noesis-gui for macOS should be built on macOS; the GUI step may fail." >&2
fi

mkdir -p "$OUT"

echo "Building macOS/$ARCH binaries in $OUT"

CGO_ENABLED=0 GOOS=darwin GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-cli" ./cmd/noesis-cli

CGO_ENABLED=0 GOOS=darwin GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-tui" ./cmd/noesis-tui

CGO_ENABLED=1 GOOS=darwin GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-gui" ./cmd/noesis-gui

echo "Done:"
ls -lh "$OUT"
