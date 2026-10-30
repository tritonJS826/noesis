package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizeWhisperBackend(t *testing.T) {
	cases := map[string]string{
		"":                             WhisperBackendAuto,
		"auto":                         WhisperBackendAuto,
		"Auto (Vulkan when available)": WhisperBackendAuto,
		"cpu":                          WhisperBackendCPU,
		"CPU only":                     WhisperBackendCPU,
		"vulkan":                       WhisperBackendVulkan,
		"Vulkan (GPU)":                 WhisperBackendVulkan,
		"gpu":                          WhisperBackendVulkan,
		"unknown":                      WhisperBackendAuto,
	}
	for in, want := range cases {
		if got := NormalizeWhisperBackend(in); got != want {
			t.Fatalf("NormalizeWhisperBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWhisperBackendArgs(t *testing.T) {
	vulkan := WhisperRuntimeInfo{Backend: WhisperBackendVulkan, Vulkan: true}
	cpu := WhisperRuntimeInfo{Backend: WhisperBackendCPU}
	if got := whisperBackendArgs(WhisperBackendCPU, vulkan, 3); len(got) != 1 || got[0] != "-ng" {
		t.Fatalf("cpu args = %v", got)
	}
	if got := whisperBackendArgs(WhisperBackendVulkan, vulkan, 2); len(got) != 2 || got[0] != "-dev" || got[1] != "2" {
		t.Fatalf("vulkan args = %v", got)
	}
	if got := whisperBackendArgs(WhisperBackendAuto, vulkan, 1); len(got) != 2 || got[0] != "-dev" || got[1] != "1" {
		t.Fatalf("auto+vulkan args = %v", got)
	}
	if got := whisperBackendArgs(WhisperBackendAuto, cpu, 1); len(got) != 0 {
		t.Fatalf("auto+cpu args = %v", got)
	}
}

func TestWhisperRuntimeSelectionPrefersVulkan(t *testing.T) {
	setTestCacheDir(t)
	t.Setenv("PATH", "")
	dataDir, err := WhisperDataDir()
	if err != nil {
		t.Fatal(err)
	}
	bin := "whisper-cli"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	mk := func(dir, name string) string {
		t.Helper()
		p := filepath.Join(dataDir, dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cpuBin := mk(whisperRuntimeDirName, bin)
	vkBin := mk(whisperVulkanRuntimeDirName, bin)
	if err := os.WriteFile(filepath.Join(filepath.Dir(vkBin), "libggml-vulkan.so"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := WhisperRuntimePathFor(WhisperBackendCPU); got != cpuBin {
		t.Fatalf("cpu runtime = %q, want %q", got, cpuBin)
	}
	if got := WhisperRuntimePathFor(WhisperBackendVulkan); got != vkBin {
		t.Fatalf("vulkan runtime = %q, want %q", got, vkBin)
	}
	if got := WhisperRuntimePathFor(WhisperBackendAuto); got != vkBin {
		t.Fatalf("auto runtime = %q, want %q", got, vkBin)
	}
	info := WhisperRuntimeInfoFor(WhisperBackendAuto)
	if !info.Vulkan || info.Backend != WhisperBackendVulkan {
		t.Fatalf("info = %#v", info)
	}
}

func TestWhisperRuntimeInstallSupportedFor(t *testing.T) {
	if !WhisperRuntimeInstallSupportedFor(WhisperBackendAuto) {
		t.Fatal("auto should be installable when any backend is")
	}
	if WhisperRuntimeInstallSupportedFor(WhisperBackendVulkan) != WhisperVulkanInstallSupported() {
		t.Fatal("vulkan support mismatch")
	}
	if WhisperRuntimeInstallSupportedFor(WhisperBackendCPU) != WhisperRuntimeAutoInstallSupported() {
		t.Fatal("cpu support mismatch")
	}
}

func TestFirstExistingFile(t *testing.T) {
	dir := t.TempDir()
	name := "libvulkan.so.1"
	if got := firstExistingFile([]string{dir}, name); got != "" {
		t.Fatalf("unexpected hit %q", got)
	}
	want := filepath.Join(dir, name)
	if err := os.WriteFile(want, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstExistingFile([]string{dir}, name); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWhisperRuntimeNeedsInstallFindsVulkanUpgrade(t *testing.T) {
	if !WhisperVulkanInstallSupported() {
		t.Skip("platform has no Vulkan asset")
	}
	setTestCacheDir(t)
	// With no runtime at all, the installer has work to do.
	if !WhisperRuntimeNeedsInstall(WhisperBackendAuto) {
		t.Fatal("auto should need an install when nothing is present")
	}
}

// TestTranscribeWhisperAutoFallsBackToCPU verifies that an "auto" run using a
// Vulkan build that fails to initialise the GPU is retried silently with -ng.
func TestTranscribeWhisperAutoFallsBackToCPU(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake runtime is POSIX-only")
	}
	setTestCacheDir(t)
	t.Setenv("PATH", "")
	// No ffmpeg needed: the fake runtime ignores the audio.
	dataDir, err := WhisperDataDir()
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dataDir, whisperVulkanRuntimeDirName)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"-ng\" ]; then\n" +
		"    echo \"fallback transcript\"\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"echo \"vulkan device init failed\" 1>&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "whisper-cli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mark the directory as a Vulkan runtime so "auto" selects it.
	if err := os.WriteFile(filepath.Join(binDir, "libggml-vulkan.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	modelPath, err := WhisperModelPath("base")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, _, err := TranscribeWhisperWithOptions(t.Context(), "base", WhisperBackendAuto, 0, "test.wav", []byte("RIFF"))
	if err != nil {
		t.Fatalf("auto fallback failed: %v", err)
	}
	if text != "fallback transcript" {
		t.Fatalf("text = %q, want fallback transcript", text)
	}
}
