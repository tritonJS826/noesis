package core

// DefaultModel can be overridden at build time via
// -ldflags "-X noesis/internal/core.DefaultModel=...".
var DefaultModel = "gemma4:31b-cloud"

// DefaultTranscribeModel is the Ollama audio-transcription model used when the
// user has not configured one. It can be overridden at build time via
// -ldflags "-X noesis/internal/core.DefaultTranscribeModel=...".
var DefaultTranscribeModel = "whisper"

// Config holds everything the processing phases need.
type Config struct {
	Host            string
	Model           string
	TranscribeModel string // Ollama audio transcription model
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
		MaxChars:        400000,
		Workers:         2,
	}
}
