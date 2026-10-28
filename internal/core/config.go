package core

// DefaultModel can be overridden at build time via
// -ldflags "-X noesis/internal/core.DefaultModel=...".
var DefaultModel = "gemma4:31b-cloud"

// DefaultTranscribeModel is the legacy Ollama audio-transcription model.
var DefaultTranscribeModel = "whisper"

// Audio backend identifiers.
const (
	AudioEngineWhisper = "whisper"
	AudioEngineGoogle  = "google"
	AudioEngineOllama  = "ollama"
)

// DefaultWhisperModel is the whisper.cpp model selected on first launch.
const DefaultWhisperModel = "base"

// DefaultSpeechLanguage is the language used by API backends that require one.
// Whisper ignores this and auto-detects.
const DefaultSpeechLanguage = "ru-RU"

// Config holds everything the processing phases need.
type Config struct {
	Host            string
	Model           string
	TranscribeModel string // legacy Ollama audio transcription model
	AudioEngine     string // whisper / google / ollama
	WhisperModel    string
	BinaryStorage   string // links (default) / db
	GoogleAPIKey    string
	SpeechLanguage  string
	MaxChars        int
	Workers         int
	PromptName      string // prompt to use (empty = default)
	TemplateName    string // export template to use (empty = default)
}

// DefaultConfig returns the configuration used by the UIs on startup.
func DefaultConfig() Config {
	return Config{
		Host:            "http://localhost:11434",
		Model:           DefaultModel,
		TranscribeModel: DefaultTranscribeModel,
		AudioEngine:     AudioEngineWhisper,
		WhisperModel:    DefaultWhisperModel,
		BinaryStorage:   BinaryStorageLinks,
		SpeechLanguage:  DefaultSpeechLanguage,
		MaxChars:        400000,
		Workers:         2,
	}
}
