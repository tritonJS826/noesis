#!/usr/bin/env bash
# Build distribution binaries for Linux.
# Run this on Linux (Fyne needs cgo and the X11/OpenGL/Wayland development headers):
#   sudo apt-get install libgl1-mesa-dev libwayland-dev libxkbcommon-dev wayland-protocols xorg-dev
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

MODEL="${MODEL:-gemma4:31b-cloud}"
TRANSCRIBE_MODEL="${TRANSCRIBE_MODEL:-whisper}"
ARCH="${GOARCH:-$(go env GOARCH)}"
OUT="dist/linux-$ARCH"
LDFLAGS="-s -w -X noesis/internal/core.DefaultModel=$MODEL -X noesis/internal/core.DefaultTranscribeModel=$TRANSCRIBE_MODEL"

mkdir -p "$OUT"

echo "Building Linux/$ARCH binaries in $OUT"

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-cli" ./cmd/noesis-cli

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-tui" ./cmd/noesis-tui

# Fyne requires cgo. Cross-architecture builds need a matching cross compiler.
CGO_ENABLED=1 GOOS=linux GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-gui" ./cmd/noesis-gui

echo "Done:"
ls -lh "$OUT"
