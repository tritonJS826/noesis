package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// This file manages the ollama models a phase needs. Ollama can pull a model on
// demand, so the app downloads missing models itself instead of asking the user
// to run "ollama pull" by hand. Interactive UIs get a chance to approve the
// download first (see ModelApprover).

// ModelInstalled reports whether model is present in an installed-model list,
// ignoring the ollama ":tag" suffix (so "nomic-embed-text" matches
// "nomic-embed-text:latest").
func ModelInstalled(installed []string, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	base := modelBaseName(model)
	for _, m := range installed {
		if strings.EqualFold(m, model) || strings.EqualFold(modelBaseName(m), base) {
			return true
		}
	}
	return false
}

// EnsureOllamaModel makes sure model is available on the ollama host. It pulls
// the model when it is missing, streaming progress through r. When r implements
// ModelApprover the user is asked first; otherwise the model is pulled
// automatically. An empty model name is a no-op.
func EnsureOllamaModel(ctx context.Context, r Reporter, host, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	installed, err := FetchModels(host)
	if err != nil {
		return fmt.Errorf("ollama unavailable at %s: %w", host, err)
	}
	if ModelInstalled(installed, model) {
		return nil
	}

	if a, ok := r.(ModelApprover); ok && !a.ApproveModelPull(model) {
		return fmt.Errorf("model %q is not installed — download skipped (run: ollama pull %s)", model, model)
	}

	r.SetPhase("pulling model "+model, 0, 0)
	r.Logf("model %q not installed — downloading it now (this can take a while)…", model)
	err = PullModel(ctx, host, model, func(status string, completed, total int64) {
		if total > 0 {
			r.SetPhase("pulling model "+model, int(completed), int(total))
		}
		switch {
		case total > 0:
			r.SetCurrent(fmt.Sprintf("%s — %d/%d bytes", status, completed, total))
		case status != "":
			r.SetCurrent(status)
		}
	})
	if err != nil {
		return fmt.Errorf("failed to download model %q: %w", model, err)
	}
	r.Logf("model %q is ready", model)
	return nil
}

// ensureExtractionModels ensures the ollama models needed to turn the given
// files into text: the vision model when a raster image is present, and the
// legacy ollama transcription model when ollama audio is selected.
func ensureExtractionModels(ctx context.Context, r Reporter, c Config, mimeTypes []string) error {
	needVision := false
	needAudio := false
	for _, m := range mimeTypes {
		if isRasterImage(m) {
			needVision = true
		}
		if NormalizeAudioEngine(c.AudioEngine) == AudioEngineOllama && strings.HasPrefix(m, "audio/") {
			needAudio = true
		}
	}
	if needVision {
		if err := EnsureOllamaModel(ctx, r, c.Host, c.visionModel()); err != nil {
			return err
		}
	}
	if needAudio {
		if err := EnsureOllamaModel(ctx, r, c.Host, c.TranscribeModel); err != nil {
			return err
		}
	}
	return nil
}

// PullModel downloads model from the ollama library, invoking onProgress for
// every streamed status line. It returns when the stream reports "success".
func PullModel(ctx context.Context, host, model string, onProgress func(status string, completed, total int64)) error {
	payload, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(host, "/")+"/api/pull", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("%s", ev.Error)
		}
		if onProgress != nil {
			onProgress(ev.Status, ev.Completed, ev.Total)
		}
		if ev.Status == "success" {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}
