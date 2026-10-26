# CICD

Release automation for noesis. The actual GitHub Actions workflow lives in
`.github/workflows/release.yml` because GitHub only discovers workflows in
that directory. This folder contains the build script it calls and the
release documentation.

## What gets built

Nine assets, published to a GitHub Release:

| Platform | Architecture | Assets |
| --- | --- | --- |
| Linux | x86_64 | `noesis-cli-linux-amd64`, `noesis-tui-linux-amd64`, `noesis-gui-linux-amd64` |
| Windows | x86_64 | `noesis-cli-windows-amd64.exe`, `noesis-tui-windows-amd64.exe`, `noesis-gui-windows-amd64.exe` |
| macOS | Apple Silicon (arm64) | `noesis-cli-macos-arm64`, `noesis-tui-macos-arm64`, `noesis-gui-macos-arm64` |

The landing page uses `releases/latest/download/<asset>` URLs, so the names
above must stay in sync with `landings/content.json`.

Release binaries bake in the local model `qwen2.5:7b` through the linker flag
`noesis/internal/core.DefaultModel`. That keeps the default download free and
local; users can override the model in the TUI/GUI settings or with `-model`.

## Trigger a release

### From a tag

```bash
git tag v0.1.0
git push origin v0.1.0
```

The `Release` workflow runs automatically and publishes the release with the
same tag.

### From GitHub Actions

1. Open **Actions → Release → Run workflow**.
2. Enter a tag such as `v0.1.0`.
3. Run it. If the tag does not exist yet, the release step creates it at the
   selected commit.

The release job downloads all matrix artifacts and attaches them to the
release. `generate_release_notes` is enabled, so GitHub fills the body from
commits since the previous release.

## Local build

The script builds the three binaries for one target:

```bash
bash CICD/build-release.sh linux amd64 linux
bash CICD/build-release.sh windows amd64 windows
bash CICD/build-release.sh darwin arm64 macos
```

Output goes to `dist/release/`.

Requirements:

- **Linux:** Go, gcc, and the X11/OpenGL/Wayland headers for Fyne:
  ```sh
  sudo apt-get install libgl1-mesa-dev libwayland-dev libxkbcommon-dev wayland-protocols xorg-dev
  ```
- **Windows:** Go and a MinGW-w64 `gcc` (GitHub's `windows-latest` image
  includes one). The GUI is linked with `-H windowsgui` so it starts without a
  console window.
- **macOS:** Go and Xcode command-line tools. The CI target is Apple Silicon
  (arm64) and uses a macOS 12+ deployment target. Intel Mac builds are not
  produced yet; add an Intel runner and a matching asset if they are needed.

Set `MODEL` to bake a different default:

```bash
MODEL=qwen2.5:7b bash CICD/build-release.sh linux amd64 linux
```

## Notes

- macOS and Windows binaries are not code-signed. macOS may require
  `xattr -d com.apple.quarantine <file>` after download; Windows SmartScreen
  may show a warning. Code signing can be added later without changing asset
  names.
- The GUI is a Fyne application, so it must be built on its target OS with
  cgo. The matrix uses native runners: Ubuntu, Windows, and Apple Silicon
  macOS.
- The workflow keeps the nine names stable. If an asset is renamed, update
  `landings/content.json` in the same commit.
