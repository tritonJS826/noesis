package core

// DefaultModel can be overridden at build time via
// -ldflags "-X noesis/internal/core.DefaultModel=...".
var DefaultModel = "gemma4:31b-cloud"

// Config holds everything the processing phases need.
type Config struct {
	Host         string
	Model        string
	MaxChars     int
	Workers      int
	PromptName   string // prompt to use (empty = default)
	TemplateName string // export template to use (empty = default)
}

// DefaultConfig returns the configuration used by the UIs on startup.
func DefaultConfig() Config {
	return Config{
		Host:     "http://localhost:11434",
		Model:    DefaultModel,
		MaxChars: 4000,
		Workers:  1,
	}
}
