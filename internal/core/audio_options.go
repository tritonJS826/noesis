package core

import (
	"context"
	"strings"
)

// AudioOptions selects how audio files are transcribed.
type AudioOptions struct {
	Engine         string // whisper / google / ollama
	WhisperModel   string
	GoogleAPIKey   string
	SpeechLanguage string
	OllamaHost     string
	OllamaModel    string
}

func (c Config) audioOptions() AudioOptions {
	return AudioOptions{
		Engine:         c.AudioEngine,
		WhisperModel:   c.WhisperModel,
		GoogleAPIKey:   c.GoogleAPIKey,
		SpeechLanguage: c.SpeechLanguage,
		OllamaHost:     c.Host,
		OllamaModel:    c.TranscribeModel,
	}
}

// Transcribe runs the selected audio backend.
func (o AudioOptions) Transcribe(ctx context.Context, name string, data []byte) (string, string, error) {
	switch NormalizeAudioEngine(o.Engine) {
	case AudioEngineGoogle:
		return TranscribeGoogle(ctx, o.GoogleAPIKey, o.SpeechLanguage, name, data)
	case AudioEngineWhisper:
		return TranscribeWhisper(ctx, o.WhisperModel, name, data)
	default:
		return TranscribeAudio(ctx, o.OllamaHost, o.OllamaModel, name, data)
	}
}

type audioEngineOption struct {
	ID    string
	Label string
}

var audioEngineOptions = []audioEngineOption{
	{AudioEngineWhisper, "Local Whisper (whisper.cpp)"},
	{AudioEngineGoogle, "Google Speech-to-Text (API key)"},
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

type speechLanguageOption struct {
	Code  string
	Label string
}

var speechLanguageOptions = []speechLanguageOption{
	{"auto", "Auto-detect (Whisper only)"},
	{"ru-RU", "Russian"},
	{"en-US", "English"},
	{"uk-UA", "Ukrainian"},
	{"de-DE", "German"},
	{"fr-FR", "French"},
	{"es-ES", "Spanish"},
	{"it-IT", "Italian"},
	{"pl-PL", "Polish"},
	{"pt-BR", "Portuguese"},
	{"tr-TR", "Turkish"},
	{"zh-CN", "Chinese"},
	{"ja-JP", "Japanese"},
	{"ko-KR", "Korean"},
}

// SpeechLanguageLabels returns the labels shown in the language selector.
func SpeechLanguageLabels() []string {
	out := make([]string, len(speechLanguageOptions))
	for i, o := range speechLanguageOptions {
		out[i] = o.Label
	}
	return out
}

// SpeechLanguageCodes returns the canonical codes in label order.
func SpeechLanguageCodes() []string {
	out := make([]string, len(speechLanguageOptions))
	for i, o := range speechLanguageOptions {
		out[i] = o.Code
	}
	return out
}

// SpeechLanguageLabel returns the human-readable label for a language code.
func SpeechLanguageLabel(code string) string {
	code = NormalizeSpeechLanguage(code)
	for _, o := range speechLanguageOptions {
		if o.Code == code {
			return o.Label
		}
	}
	return code
}

// NormalizeSpeechLanguage accepts a code or label and returns the canonical
// code. Unknown values fall back to the default language.
func NormalizeSpeechLanguage(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultSpeechLanguage
	}
	for _, o := range speechLanguageOptions {
		if strings.EqualFold(v, o.Code) || strings.EqualFold(v, o.Label) {
			return o.Code
		}
	}
	// Accept plain ISO codes such as "ru" or "en".
	switch strings.ToLower(v) {
	case "ru":
		return "ru-RU"
	case "en":
		return "en-US"
	case "uk":
		return "uk-UA"
	case "de":
		return "de-DE"
	case "fr":
		return "fr-FR"
	case "es":
		return "es-ES"
	case "it":
		return "it-IT"
	case "pl":
		return "pl-PL"
	case "pt":
		return "pt-BR"
	case "tr":
		return "tr-TR"
	case "zh":
		return "zh-CN"
	case "ja":
		return "ja-JP"
	case "ko":
		return "ko-KR"
	}
	return DefaultSpeechLanguage
}
