package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// TranscribeAudio sends an audio file to an OpenAI-compatible transcription
// endpoint and returns the transcript plus a tool identifier.
//
// This is a legacy backend for custom servers: Ollama itself does not ship
// native audio transcription. New code should use TranscribeWhisper or
// TranscribeGoogle via AudioOptions. When ffmpeg is installed, audio is
// converted to 16 kHz mono WAV first, which is the format most local speech
// models expect.
func TranscribeAudio(ctx context.Context, host, model, name string, data []byte) (string, string, error) {
	if strings.TrimSpace(host) == "" {
		host = DefaultConfig().Host
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultTranscribeModel
	}
	if len(data) == 0 {
		return "", "", fmt.Errorf("audio %q is empty", name)
	}

	audio := data
	tool := "ollama:" + model
	converted := false
	if ffmpeg := findExecutable("ffmpeg"); ffmpeg != "" {
		out, err := runCommand(ctx, 5*time.Minute, ffmpeg, []string{
			"-hide_banner", "-loglevel", "error",
			"-i", "pipe:0",
			"-vn", "-ac", "1", "-ar", "16000",
			"-f", "wav", "pipe:1",
		}, data)
		if err == nil && len(out) > 0 {
			audio = out
			tool += "+ffmpeg"
			converted = true
		}
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	filename := filepath.Base(name)
	if filename == "" || filename == "." {
		filename = "audio.wav"
	}
	if converted {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".wav"
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", tool, err
	}
	if _, err := part.Write(audio); err != nil {
		return "", tool, err
	}
	if err := mw.WriteField("model", model); err != nil {
		return "", tool, err
	}
	if err := mw.WriteField("response_format", "json"); err != nil {
		return "", tool, err
	}
	if err := mw.Close(); err != nil {
		return "", tool, err
	}

	base := strings.TrimRight(host, "/")
	endpoint := base + "/v1/audio/transcriptions"
	if strings.HasSuffix(base, "/v1") {
		endpoint = base + "/audio/transcriptions"
	}

	rctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST", endpoint, &body)
	if err != nil {
		return "", tool, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", tool, fmt.Errorf("ollama transcription at %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		return "", tool, fmt.Errorf("ollama transcription HTTP %d: %s", resp.StatusCode, msg)
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err == nil {
		if text := cleanExtracted(out.Text); text != "" {
			return text, tool, nil
		}
	}
	// Some compatible servers return plain text rather than JSON.
	if text := cleanExtracted(string(raw)); text != "" && !strings.HasPrefix(text, "{") {
		return text, tool, nil
	}
	return "", tool, fmt.Errorf("ollama returned an empty transcript (model %q may not support audio)", model)
}
