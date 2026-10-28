# Build distribution binaries for Windows from PowerShell.
# Requires Go and a MinGW-w64 compatible C compiler (e.g. MSYS2) for the Fyne GUI.
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$model = if ($env:MODEL) { $env:MODEL } else { "gemma4:31b-cloud" }
$transcribeModel = if ($env:TRANSCRIBE_MODEL) { $env:TRANSCRIBE_MODEL } else { "whisper" }
$arch = if ($env:GOARCH) { $env:GOARCH } else { (go env GOARCH) }
$out = "dist/windows-$arch"
$ldflags = "-s -w -X noesis/internal/core.DefaultModel=$model -X noesis/internal/core.DefaultTranscribeModel=$transcribeModel"

New-Item -ItemType Directory -Force -Path $out | Out-Null

Write-Host "Building Windows/$arch binaries in $out"

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = $arch
go build -trimpath -ldflags $ldflags -o "$out/noesis-cli.exe" ./cmd/noesis-cli
go build -trimpath -ldflags $ldflags -o "$out/noesis-tui.exe" ./cmd/noesis-tui

$env:CGO_ENABLED = "1"
go build -trimpath -ldflags "$ldflags -H=windowsgui" -o "$out/noesis-gui.exe" ./cmd/noesis-gui

Write-Host "Done:"
Get-ChildItem $out | Format-Table Name, Length
