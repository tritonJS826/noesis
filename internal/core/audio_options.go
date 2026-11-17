package core

import (
	"context"
	"strings"
)

// AudioOptions selects how audio files are transcribed.
type AudioOptions struct {
	Engine         string // whisper / ollama
	WhisperModel   string
	WhisperBackend string // auto / cpu / vulkan
	WhisperDevice  int    // Vulkan device index
	OllamaHost     string
	OllamaModel    string
}

func (c Config) audioOptions() AudioOptions {
	return AudioOptions{
		Engine:         c.AudioEngine,
		WhisperModel:   c.WhisperModel,
		WhisperBackend: c.WhisperBackend,
		WhisperDevice:  c.WhisperDevice,
		OllamaHost:     c.Host,
		OllamaModel:    c.TranscribeModel,
	}
}

// Transcribe runs the selected audio backend.
func (o AudioOptions) Transcribe(ctx context.Context, name string, data []byte) (string, string, error) {
	switch NormalizeAudioEngine(o.Engine) {
	case AudioEngineWhisper:
		return TranscribeWhisperWithOptions(ctx, o.WhisperModel, o.WhisperBackend, o.WhisperDevice, name, data)
	default:
		return TranscribeAudio(ctx, o.OllamaHost, o.OllamaModel, name, data)
	}
}

// WhisperBackendIDs returns canonical backend IDs in display order.
func WhisperBackendIDs() []string {
	return []string{WhisperBackendAuto, WhisperBackendCPU, WhisperBackendVulkan}
}

// WhisperBackendLabels returns labels matching WhisperBackendIDs.
func WhisperBackendLabels() []string {
	return []string{"Auto (Vulkan when available)", "CPU only", "Vulkan (GPU)"}
}

// WhisperBackendLabel returns the human-readable label for a backend ID.
func WhisperBackendLabel(v string) string {
	v = NormalizeWhisperBackend(v)
	for i, id := range WhisperBackendIDs() {
		if id == v {
			return WhisperBackendLabels()[i]
		}
	}
	return v
}

// NormalizeWhisperBackend accepts IDs or labels and falls back to auto.
func NormalizeWhisperBackend(v string) string {
	raw := strings.TrimSpace(v)
	for i, label := range WhisperBackendLabels() {
		if strings.EqualFold(raw, label) {
			return WhisperBackendIDs()[i]
		}
	}
	v = strings.ToLower(raw)
	switch v {
	case WhisperBackendCPU, "cpu-only", "no-gpu", "nongpu":
		return WhisperBackendCPU
	case WhisperBackendVulkan, "gpu", "vk":
		return WhisperBackendVulkan
	default:
		return WhisperBackendAuto
	}
}

type audioEngineOption struct {
	ID    string
	Label string
}

var audioEngineOptions = []audioEngineOption{
	{AudioEngineWhisper, "Local Whisper (whisper.cpp)"},
}

// AudioEngineLabels returns the labels shown in the settings selector.
func AudioEngineLabels() []string {
	out := make([]string, len(audioEngineOptions))
	for i, o := range audioEngineOptions {
		out[i] = o.Label
	}
	return out
}

// AudioEngineIDs returns the canonical IDs in the same order as labels.
func AudioEngineIDs() []string {
	out := make([]string, len(audioEngineOptions))
	for i, o := range audioEngineOptions {
		out[i] = o.ID
	}
	return out
}

// AudioEngineLabel returns the human-readable label for an engine ID.
func AudioEngineLabel(engine string) string {
	engine = NormalizeAudioEngine(engine)
	for _, o := range audioEngineOptions {
		if o.ID == engine {
			return o.Label
		}
	}
	return engine
}

// NormalizeAudioEngine accepts an engine ID or label and returns the canonical
// ID. Unknown values fall back to Local Whisper.
func NormalizeAudioEngine(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return AudioEngineWhisper
	}
	for _, o := range audioEngineOptions {
		if strings.EqualFold(v, o.ID) || strings.EqualFold(v, o.Label) {
			return o.ID
		}
	}
	switch strings.ToLower(v) {
	case AudioEngineOllama, "openai", "openai-compatible", "legacy":
		return AudioEngineOllama
	}
	return AudioEngineWhisper
}
