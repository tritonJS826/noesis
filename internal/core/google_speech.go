package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// TranscribeGoogle sends audio to the Google Cloud Speech-to-Text v1 REST API.
// It requires an API key and ffmpeg (for conversion to 16 kHz mono WAV).
// The synchronous endpoint accepts audio up to about one minute.
func TranscribeGoogle(ctx context.Context, apiKey, language, name string, data []byte) (string, string, error) {
	if len(data) == 0 {
		return "", "google:speech", fmt.Errorf("audio %q is empty", name)
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}
	if key == "" {
		return "", "google:speech", fmt.Errorf("Google API key is not set. %s", GoogleAPIKeyInstructions())
	}
	language = NormalizeSpeechLanguage(language)
	if language == "auto" {
		return "", "google:speech", fmt.Errorf(`Google transcription needs a specific language. Choose one in Settings; "Auto-detect" works only with Local Whisper`)
	}

	audio, err := audioForGoogle(ctx, name, data)
	if err != nil {
		return "", "google:speech", err
	}
	// 16 kHz mono 16-bit WAV = 32,000 bytes per second.
	if seconds := float64(len(audio)) / 32000.0; seconds > 58 {
		return "", "google:speech", fmt.Errorf("Google synchronous transcription supports about one minute of audio; use Local Whisper for this %.0f-second file", seconds)
	}

	payload := map[string]any{
		"config": map[string]any{
			"encoding":                   "LINEAR16",
			"sampleRateHertz":            16000,
			"languageCode":               language,
			"enableAutomaticPunctuation": true,
		},
		"audio": map[string]string{
			"content": base64.StdEncoding.EncodeToString(audio),
		},
	}
	body, _ := json.Marshal(payload)
	endpoint := "https://speech.googleapis.com/v1/speech:recognize?key=" + url.QueryEscape(key)

	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "google:speech", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "google:speech", fmt.Errorf("Google transcription: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			msg += ". " + GoogleSpeechEnableInstructions()
		}
		return "", "google:speech", fmt.Errorf("Google transcription HTTP %d: %s", resp.StatusCode, msg)
	}

	var out struct {
		Results []struct {
			Alternatives []struct {
				Transcript string `json:"transcript"`
			} `json:"alternatives"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "google:speech", fmt.Errorf("failed to read Google transcription response")
	}
	var parts []string
	for _, res := range out.Results {
		if len(res.Alternatives) > 0 {
			if t := strings.TrimSpace(res.Alternatives[0].Transcript); t != "" {
				parts = append(parts, t)
			}
		}
	}
	text := cleanExtracted(strings.Join(parts, "\n"))
	if text == "" {
		return "", "google:speech", fmt.Errorf("Google returned an empty transcript")
	}
	return text, "google:speech", nil
}

func audioForGoogle(ctx context.Context, name string, data []byte) ([]byte, error) {
	ffmpeg := findExecutable("ffmpeg")
	if ffmpeg == "" {
		return nil, fmt.Errorf("ffmpeg is required for Google transcription. %s", FFmpegInstallInstructions())
	}
	out, err := runCommand(ctx, 5*time.Minute, ffmpeg, []string{
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-vn", "-ac", "1", "-ar", "16000",
		"-f", "wav", "pipe:1",
	}, data)
	if err != nil {
		return nil, fmt.Errorf("convert audio for Google: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ffmpeg produced an empty WAV for %q", name)
	}
	return out, nil
}
