package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAudioEngineOptions(t *testing.T) {
	ids := AudioEngineIDs()
	labels := AudioEngineLabels()
	if len(ids) != len(labels) || len(ids) < 2 {
		t.Fatalf("bad audio engine options: %v / %v", ids, labels)
	}
	if ids[0] != AudioEngineWhisper || ids[1] != AudioEngineGoogle {
		t.Fatalf("unexpected engine order: %v", ids)
	}
	for i, id := range ids {
		if got := NormalizeAudioEngine(id); got != id {
			t.Fatalf("id %q normalized to %q", id, got)
		}
		if got := NormalizeAudioEngine(labels[i]); got != id {
			t.Fatalf("label %q normalized to %q, want %q", labels[i], got, id)
		}
	}
	if got := NormalizeAudioEngine(""); got != AudioEngineWhisper {
		t.Fatalf("empty engine = %q", got)
	}
	if got := NormalizeAudioEngine("legacy"); got != AudioEngineOllama {
		t.Fatalf("legacy engine = %q", got)
	}
	if got := AudioEngineLabel(AudioEngineGoogle); got == "" || got == AudioEngineGoogle {
		t.Fatalf("unexpected google label %q", got)
	}
}

func TestSpeechLanguageOptions(t *testing.T) {
	codes := SpeechLanguageCodes()
	labels := SpeechLanguageLabels()
	if len(codes) != len(labels) || len(codes) < 2 {
		t.Fatalf("bad language options: %v / %v", codes, labels)
	}
	if got := NormalizeSpeechLanguage("ru-RU"); got != "ru-RU" {
		t.Fatalf("ru-RU = %q", got)
	}
	if got := NormalizeSpeechLanguage("Russian"); got != "ru-RU" {
		t.Fatalf("Russian = %q", got)
	}
	if got := NormalizeSpeechLanguage("ru"); got != "ru-RU" {
		t.Fatalf("ru = %q", got)
	}
	if got := NormalizeSpeechLanguage(""); got != DefaultSpeechLanguage {
		t.Fatalf("empty language = %q", got)
	}
}

func TestWhisperModelCatalog(t *testing.T) {
	names := WhisperModelNames()
	if len(names) < 3 {
		t.Fatalf("expected several models, got %v", names)
	}
	if got := NormalizeWhisperModel("ggml-small.bin"); got != "small" {
		t.Fatalf("ggml-small.bin = %q", got)
	}
	if got := NormalizeWhisperModel("SMALL"); got != "small" {
		t.Fatalf("SMALL = %q", got)
	}
	if got := NormalizeWhisperModel("not-a-model"); got != DefaultWhisperModel {
		t.Fatalf("unknown model = %q", got)
	}
	models := WhisperModels()
	if len(models) != len(names) {
		t.Fatalf("catalog length mismatch")
	}
	for i, m := range models {
		if m.Name != names[i] || m.Size <= 0 || m.URL == "" || m.FileName == "" {
			t.Fatalf("bad model entry: %#v", m)
		}
	}
}

func setTestCacheDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", dir)
	case "darwin":
		t.Setenv("HOME", dir)
	default:
		t.Setenv("XDG_CACHE_HOME", dir)
	}
}

func TestWhisperModelDownloadedStatus(t *testing.T) {
	setTestCacheDir(t)

	path, err := WhisperModelPath("base")
	if err != nil {
		t.Fatalf("model path: %v", err)
	}
	if filepath.Dir(path) == "" {
		t.Fatal("empty model dir")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !WhisperModelInstalled("base") {
		t.Fatal("WhisperModelInstalled did not see the file")
	}
	for _, m := range WhisperModels() {
		if m.Name == "base" {
			if !m.Downloaded || m.Path != path {
				t.Fatalf("base not detected as downloaded: %#v", m)
			}
			return
		}
	}
	t.Fatal("base model not found in catalog")
}

func TestTranscribeWhisperMissingRuntime(t *testing.T) {
	setTestCacheDir(t)
	if findExecutable("whisper-cli") != "" {
		t.Skip("system whisper-cli is installed")
	}
	t.Setenv("PATH", "")
	_, _, err := TranscribeWhisper(t.Context(), "base", "test.wav", []byte("RIFF"))
	if err == nil || !strings.Contains(err.Error(), "runtime is not installed") {
		t.Fatalf("got %v, want missing runtime error", err)
	}
}

func TestTranscribeWhisperMissingModel(t *testing.T) {
	setTestCacheDir(t)
	t.Setenv("PATH", "")
	dataDir, err := WhisperDataDir()
	if err != nil {
		t.Fatal(err)
	}
	binName := "whisper-cli"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	bin := filepath.Join(dataDir, "runtime", "fake", binName)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err = TranscribeWhisper(t.Context(), "base", "test.wav", []byte("RIFF"))
	if err == nil || !strings.Contains(err.Error(), "not downloaded") {
		t.Fatalf("got %v, want missing model error", err)
	}
}

func TestGoogleTranscriptionRequiresKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	_, _, err := TranscribeGoogle(t.Context(), "", "ru-RU", "test.wav", []byte("RIFF"))
	if err == nil {
		t.Fatal("expected an API key error")
	}
	if !strings.Contains(err.Error(), "console.cloud.google.com") {
		t.Fatalf("API key error should link to Google Cloud Console: %v", err)
	}
}

func TestAudioHelpIncludesInstallLinks(t *testing.T) {
	if !strings.Contains(FFmpegInstallInstructions(), "https://") {
		t.Fatalf("ffmpeg instructions missing link: %s", FFmpegInstallInstructions())
	}
	if !strings.Contains(WhisperRuntimeInstallInstructions(), "whisper.cpp") {
		t.Fatalf("whisper instructions missing link: %s", WhisperRuntimeInstallInstructions())
	}
	if !strings.Contains(GoogleAPIKeyInstructions(), "console.cloud.google.com") {
		t.Fatalf("Google instructions missing link: %s", GoogleAPIKeyInstructions())
	}
	if !strings.Contains(GoogleSpeechEnableInstructions(), "console.cloud.google.com") {
		t.Fatalf("Google enable instructions missing link: %s", GoogleSpeechEnableInstructions())
	}
}
