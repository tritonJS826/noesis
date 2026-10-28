package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// WhisperModelInfo describes one downloadable whisper.cpp model.
type WhisperModelInfo struct {
	Name       string
	FileName   string
	URL        string
	Size       int64
	Downloaded bool
	Path       string
}

var whisperModelCatalog = []WhisperModelInfo{
	{Name: "tiny", FileName: "ggml-tiny.bin", URL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin", Size: 77691713},
	{Name: "base", FileName: "ggml-base.bin", URL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin", Size: 147951465},
	{Name: "small", FileName: "ggml-small.bin", URL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin", Size: 487601967},
	{Name: "medium", FileName: "ggml-medium.bin", URL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin", Size: 1533763059},
	{Name: "large-v3-turbo", FileName: "ggml-large-v3-turbo.bin", URL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin", Size: 1624555275},
}

// WhisperModels returns the model catalog with local download status.
func WhisperModels() []WhisperModelInfo {
	dir, _ := WhisperModelsDir()
	out := make([]WhisperModelInfo, len(whisperModelCatalog))
	copy(out, whisperModelCatalog)
	for i := range out {
		if dir != "" {
			out[i].Path = filepath.Join(dir, out[i].FileName)
			if st, err := os.Stat(out[i].Path); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
				out[i].Downloaded = true
			}
		}
	}
	return out
}

// WhisperModelNames returns canonical model names in catalog order.
func WhisperModelNames() []string {
	out := make([]string, len(whisperModelCatalog))
	for i, m := range whisperModelCatalog {
		out[i] = m.Name
	}
	return out
}

// NormalizeWhisperModel accepts "base", "ggml-base.bin", etc.
func NormalizeWhisperModel(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.TrimSuffix(name, ".bin")
	name = strings.TrimPrefix(name, "ggml-")
	if name == "" {
		return DefaultWhisperModel
	}
	for _, m := range whisperModelCatalog {
		if m.Name == name {
			return name
		}
	}
	return DefaultWhisperModel
}

func whisperModel(name string) (WhisperModelInfo, bool) {
	name = NormalizeWhisperModel(name)
	for _, m := range whisperModelCatalog {
		if m.Name == name {
			return m, true
		}
	}
	return WhisperModelInfo{}, false
}

// WhisperDataDir returns the directory holding downloaded models and the
// whisper.cpp runtime.
func WhisperDataDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = "."
	}
	dir := filepath.Join(base, "noesis", "whisper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// WhisperModelsDir returns the directory holding model files.
func WhisperModelsDir() (string, error) {
	dir, err := WhisperDataDir()
	if err != nil {
		return "", err
	}
	models := filepath.Join(dir, "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		return "", err
	}
	return models, nil
}

// WhisperModelPath returns the local path expected for a model.
func WhisperModelPath(name string) (string, error) {
	m, ok := whisperModel(name)
	if !ok {
		return "", fmt.Errorf("unknown whisper model %q", name)
	}
	dir, err := WhisperModelsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, m.FileName), nil
}

// WhisperModelInstalled reports whether a model file is present locally.
func WhisperModelInstalled(name string) bool {
	path, err := WhisperModelPath(name)
	if err != nil {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// DownloadWhisperModel downloads one model unless it already exists.
func DownloadWhisperModel(ctx context.Context, name string, progress func(done, total int64)) error {
	m, ok := whisperModel(name)
	if !ok {
		return fmt.Errorf("unknown whisper model %q", name)
	}
	dest, err := WhisperModelPath(m.Name)
	if err != nil {
		return err
	}
	if st, err := os.Stat(dest); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		return nil
	}
	return downloadFile(ctx, m.URL, dest, progress)
}

// DeleteWhisperModel removes a downloaded model.
func DeleteWhisperModel(name string) error {
	path, err := WhisperModelPath(name)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ─── whisper.cpp runtime ────────────────────────────────────────────────────

const whisperRuntimeRelease = "b5454"

// WhisperRuntimePath returns the whisper-cli binary path, or "" when no
// runtime is available. A system-wide whisper-cli is preferred.
func WhisperRuntimePath() string {
	if p := findExecutable("whisper-cli"); p != "" {
		return p
	}
	dir, err := WhisperDataDir()
	if err != nil {
		return ""
	}
	if p := findWhisperBinary(filepath.Join(dir, "runtime")); p != "" {
		return p
	}
	return ""
}

// WhisperRuntimeInstalled reports whether whisper-cli can be used.
func WhisperRuntimeInstalled() bool { return WhisperRuntimePath() != "" }

func findWhisperBinary(root string) string {
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == "whisper-cli" || name == "whisper-cli.exe" {
			found = path
			return io.EOF
		}
		return nil
	})
	return found
}

// WhisperRuntimeAutoInstallSupported reports whether noesis can download a
// prebuilt whisper.cpp runtime on this platform.
func WhisperRuntimeAutoInstallSupported() bool {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64":
		return true
	default:
		return false
	}
}

type runtimeAsset struct {
	URL  string
	Kind string // "tar.gz" or "zip"
}

func whisperRuntimeAsset() (runtimeAsset, error) {
	base := "https://github.com/ggml-org/whisper.cpp/releases/download/" + whisperRuntimeRelease + "/"
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return runtimeAsset{base + "whisper-bin-ubuntu-x64.tar.gz", "tar.gz"}, nil
	case "linux/arm64":
		return runtimeAsset{base + "whisper-bin-ubuntu-arm64.tar.gz", "tar.gz"}, nil
	case "windows/amd64":
		return runtimeAsset{base + "whisper-bin-x64.zip", "zip"}, nil
	case "windows/arm64":
		return runtimeAsset{base + "whisper-bin-win-cpu-arm64.zip", "zip"}, nil
	default:
		return runtimeAsset{}, fmt.Errorf("automatic whisper.cpp download is not available for %s/%s. %s", runtime.GOOS, runtime.GOARCH, WhisperRuntimeInstallInstructions())
	}
}

// EnsureWhisperRuntime downloads and extracts whisper.cpp if no runtime is
// installed yet.
func EnsureWhisperRuntime(ctx context.Context, progress func(done, total int64)) error {
	if WhisperRuntimeInstalled() {
		return nil
	}
	asset, err := whisperRuntimeAsset()
	if err != nil {
		return err
	}
	dir, err := WhisperDataDir()
	if err != nil {
		return err
	}
	archive := filepath.Join(dir, "runtime-download."+asset.Kind)
	if err := downloadFile(ctx, asset.URL, archive, progress); err != nil {
		return fmt.Errorf("download whisper.cpp runtime: %w. %s", err, WhisperRuntimeInstallInstructions())
	}
	defer os.Remove(archive)
	if err := installWhisperRuntime(archive, asset.Kind, dir); err != nil {
		return fmt.Errorf("install whisper.cpp runtime: %w. %s", err, WhisperRuntimeInstallInstructions())
	}
	return nil
}

func installWhisperRuntime(archive, kind, dataDir string) error {
	tmp, err := os.MkdirTemp(dataDir, "runtime-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	switch kind {
	case "tar.gz":
		err = extractTarGz(archive, tmp)
	case "zip":
		err = extractZip(archive, tmp)
	default:
		err = fmt.Errorf("unknown archive kind %q", kind)
	}
	if err != nil {
		return err
	}
	bin := findWhisperBinary(tmp)
	if bin == "" {
		return fmt.Errorf("downloaded whisper.cpp archive does not contain whisper-cli")
	}
	_ = os.Chmod(bin, 0o755)

	runtimeDir := filepath.Join(dataDir, "runtime")
	if err := os.RemoveAll(runtimeDir); err != nil {
		return err
	}
	return os.Rename(tmp, runtimeDir)
}

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeArchivePath(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeArchiveFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if _, err := safeArchivePath(filepath.Dir(target), hdr.Linkname); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget, err := safeArchivePath(dest, hdr.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
}

func extractZip(src, dest string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target, err := safeArchivePath(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeArchiveFile(target, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func safeArchivePath(root, name string) (string, error) {
	name = filepath.FromSlash(name)
	clean := filepath.Clean(name)
	if clean == "." {
		return root, nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in archive: %q", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in archive: %q", name)
	}
	return target, nil
}

func writeArchiveFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// ─── transcription ──────────────────────────────────────────────────────────

// TranscribeWhisper runs whisper.cpp locally with a downloaded model.
func TranscribeWhisper(ctx context.Context, modelName, name string, data []byte) (string, string, error) {
	if len(data) == 0 {
		return "", "", fmt.Errorf("audio %q is empty", name)
	}
	bin := WhisperRuntimePath()
	if bin == "" {
		return "", "whisper", fmt.Errorf("local Whisper runtime is not installed. %s", WhisperRuntimeInstallInstructions())
	}
	modelName = NormalizeWhisperModel(modelName)
	modelPath, err := WhisperModelPath(modelName)
	if err != nil {
		return "", "whisper", err
	}
	if st, err := os.Stat(modelPath); err != nil || st.Size() == 0 {
		return "", "whisper:" + modelName, fmt.Errorf("whisper model %q is not downloaded; open Settings → Whisper models and download it", modelName)
	}

	audio, ext, err := audioForWhisper(ctx, name, data)
	if err != nil {
		return "", "whisper:" + modelName, err
	}
	tmp, err := os.MkdirTemp("", "noesis-whisper-*")
	if err != nil {
		return "", "whisper:" + modelName, err
	}
	defer os.RemoveAll(tmp)
	audioPath := filepath.Join(tmp, "audio."+ext)
	if err := os.WriteFile(audioPath, audio, 0o644); err != nil {
		return "", "whisper:" + modelName, err
	}

	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	}
	if threads > 8 {
		threads = 8
	}
	args := []string{
		"-m", modelPath,
		"-f", audioPath,
		"-l", "auto",
		"-np", "-nt",
		"-t", strconv.Itoa(threads),
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = whisperEnv(bin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", "whisper:" + modelName, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", "whisper:" + modelName, fmt.Errorf("whisper: %w: %s", err, msg)
	}
	text := cleanExtracted(stdout.String())
	if text == "" {
		return "", "whisper:" + modelName, fmt.Errorf("whisper returned an empty transcript")
	}
	return text, "whisper:" + modelName, nil
}

func audioForWhisper(ctx context.Context, name string, data []byte) ([]byte, string, error) {
	if ffmpeg := findExecutable("ffmpeg"); ffmpeg != "" {
		out, err := runCommand(ctx, 5*time.Minute, ffmpeg, []string{
			"-hide_banner", "-loglevel", "error",
			"-i", "pipe:0",
			"-vn", "-ac", "1", "-ar", "16000",
			"-f", "wav", "pipe:1",
		}, data)
		if err == nil && len(out) > 0 {
			return out, "wav", nil
		}
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch ext {
	case "wav", "mp3", "ogg", "flac":
		return data, ext, nil
	}
	return nil, "", fmt.Errorf("ffmpeg is required to convert .%s audio for Whisper. %s", ext, FFmpegInstallInstructions())
}

func whisperEnv(bin string) []string {
	env := os.Environ()
	if !isCachedWhisperBinary(bin) {
		return env
	}
	dir := filepath.Dir(bin)
	switch runtime.GOOS {
	case "linux":
		return append(env, "LD_LIBRARY_PATH="+prependEnvPath("LD_LIBRARY_PATH", dir))
	case "darwin":
		return append(env, "DYLD_LIBRARY_PATH="+prependEnvPath("DYLD_LIBRARY_PATH", dir))
	default:
		return env
	}
}

func isCachedWhisperBinary(bin string) bool {
	dataDir, err := WhisperDataDir()
	if err != nil {
		return false
	}
	root := filepath.Join(dataDir, "runtime")
	rel, err := filepath.Rel(root, filepath.Dir(bin))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func prependEnvPath(name, dir string) string {
	if old := os.Getenv(name); old != "" {
		return dir + string(os.PathListSeparator) + old
	}
	return dir
}

// ─── download helpers ───────────────────────────────────────────────────────

func downloadFile(ctx context.Context, url, dest string, progress func(done, total int64)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	_ = os.Remove(part)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		return fmt.Errorf("download HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 128*1024)
	lastReport := int64(-1)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(part)
				return err
			}
			done += int64(n)
			if progress != nil && (done-lastReport >= 1<<20 || (total > 0 && done == total)) {
				progress(done, total)
				lastReport = done
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			os.Remove(part)
			return readErr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return err
	}
	if total > 0 && done != total {
		os.Remove(part)
		return fmt.Errorf("download incomplete: got %d of %d bytes", done, total)
	}
	if progress != nil {
		progress(done, total)
	}
	return os.Rename(part, dest)
}
