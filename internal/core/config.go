package core

import "strings"

// DefaultModel can be overridden at build time via
// -ldflags "-X noesis/internal/core.DefaultModel=...".
var DefaultModel = "gemma4:31b-cloud"

// DefaultTranscribeModel is the legacy Ollama audio-transcription model.
var DefaultTranscribeModel = "whisper"

// DefaultVisionModel is the model used to turn images into markdown. An empty
// value means "use the main model" (see Config.visionModel).
var DefaultVisionModel = ""

// DefaultEmbeddingModel is the ollama model used to build the semantic (RAG)
// vector index. Pull it with "ollama pull mxbai-embed-large".
var DefaultEmbeddingModel = "mxbai-embed-large"

// NormalizeEmbeddingModel returns a usable embedding model name, falling back
// to the default when the value is empty.
func NormalizeEmbeddingModel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultEmbeddingModel
	}
	return s
}

// Audio backend identifiers.
const (
	AudioEngineWhisper = "whisper"
	AudioEngineGoogle  = "google"
	AudioEngineOllama  = "ollama"
)

// DefaultWhisperModel is the whisper.cpp model selected on first launch.
const DefaultWhisperModel = "base"

// Whisper GPU backends. "auto" uses a Vulkan runtime when one is available and
// otherwise falls back to the CPU build; "cpu" always passes -ng; "vulkan"
// requires a Vulkan-enabled runtime.
const (
	WhisperBackendAuto   = "auto"
	WhisperBackendCPU    = "cpu"
	WhisperBackendVulkan = "vulkan"
)

// DefaultWhisperBackend is the GPU policy selected on first launch.
const DefaultWhisperBackend = WhisperBackendAuto

// DefaultWhisperDevice is the Vulkan device index (0 = default adapter).
const DefaultWhisperDevice = 0

// DefaultSpeechLanguage is the language used by API backends that require one.
// Whisper ignores this and auto-detects.
const DefaultSpeechLanguage = "ru-RU"

// Config holds everything the processing phases need.
type Config struct {
	Host            string
	Model           string
	VisionModel     string // model used to describe images (empty = main model)
	EmbeddingModel  string // model used to embed notes for RAG (empty = default)
	TranscribeModel string // legacy Ollama audio transcription model
	AudioEngine     string // whisper / google / ollama
	WhisperModel    string
	WhisperBackend  string // auto / cpu / vulkan
	WhisperDevice   int    // Vulkan device index
	BinaryStorage   string // links (default) / db
	GoogleAPIKey    string
	SpeechLanguage  string
	MaxChars        int
	Workers         int
	PromptName      string // prompt to use (empty = default)
	TemplateName    string // export template to use (empty = default)

	// Re-extraction filters (used by PhaseExtract). ExtractName matches a
	// case-insensitive substring of the file path / name; ExtractFormats
	// restricts the run to the given extensions (nil = every extractable
	// format).
	ExtractName    string
	ExtractFormats FormatSet
}

// visionModel returns the model used to describe images. It falls back to the
// main model when no dedicated vision model is configured.
func (c Config) visionModel() string {
	if strings.TrimSpace(c.VisionModel) != "" {
		return c.VisionModel
	}
	return c.Model
}

// DefaultConfig returns the configuration used by the UIs on startup.
func DefaultConfig() Config {
	return Config{
		Host:            "http://localhost:11434",
		Model:           DefaultModel,
		EmbeddingModel:  DefaultEmbeddingModel,
		TranscribeModel: DefaultTranscribeModel,
		AudioEngine:     AudioEngineWhisper,
		WhisperModel:    DefaultWhisperModel,
		WhisperBackend:  DefaultWhisperBackend,
		WhisperDevice:   DefaultWhisperDevice,
		BinaryStorage:   BinaryStorageLinks,
		SpeechLanguage:  DefaultSpeechLanguage,
		MaxChars:        400000,
		Workers:         2,
	}
}
