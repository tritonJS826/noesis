#!/usr/bin/env bash
# Build distribution binaries for Windows.
# Run this from Git Bash/MSYS2 on Windows, or cross-compile from Linux with
# mingw-w64 installed (set CC=x86_64-w64-mingw32-gcc for amd64).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

MODEL="${MODEL:-gemma4:31b-cloud}"
ARCH="${GOARCH:-$(go env GOARCH)}"
OUT="dist/windows-$ARCH"
LDFLAGS="-s -w -X noesis/internal/core.DefaultModel=$MODEL"

mkdir -p "$OUT"

echo "Building Windows/$ARCH binaries in $OUT"

CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-cli.exe" ./cmd/noesis-cli

CGO_ENABLED=0 GOOS=windows GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/noesis-tui.exe" ./cmd/noesis-tui

# Fyne requires cgo. -H=windowsgui hides the console window for the GUI app.
CGO_ENABLED=1 GOOS=windows GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$OUT/noesis-gui.exe" ./cmd/noesis-gui

echo "Done:"
ls -lh "$OUT"
