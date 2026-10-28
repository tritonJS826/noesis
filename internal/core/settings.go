package core

import (
	"database/sql"
	"strconv"
	"strings"
)

// AppSettings is the subset of UI settings persisted in the database. The
// database path itself is not stored here: it must be known before the
// database can be opened.
type AppSettings struct {
	Src             string
	Out             string
	Host            string
	Model           string
	TranscribeModel string
	AudioEngine     string
	WhisperModel    string
	GoogleAPIKey    string
	SpeechLanguage  string
	MaxChars        int
	Workers         int
}

// DefaultAppSettings returns the initial settings used for new databases.
func DefaultAppSettings() AppSettings {
	c := DefaultConfig()
	return AppSettings{
		Src:             ".",
		Out:             "out",
		Host:            c.Host,
		Model:           c.Model,
		TranscribeModel: c.TranscribeModel,
		AudioEngine:     c.AudioEngine,
		WhisperModel:    c.WhisperModel,
		GoogleAPIKey:    c.GoogleAPIKey,
		SpeechLanguage:  c.SpeechLanguage,
		MaxChars:        c.MaxChars,
		Workers:         c.Workers,
	}
}

// LoadAppSettings reads persisted settings, falling back to the defaults for
// every value that has not been saved yet.
func LoadAppSettings(db *sql.DB) AppSettings {
	s := DefaultAppSettings()
	if db == nil {
		return s
	}
	get := func(name string) (string, bool) {
		var v string
		if db.QueryRow(`SELECT value FROM settings WHERE name=?`, name).Scan(&v) != nil {
			return "", false
		}
		return v, true
	}
	if v, ok := get("src"); ok {
		s.Src = v
	}
	if v, ok := get("out"); ok {
		s.Out = v
	}
	if v, ok := get("host"); ok {
		s.Host = v
	}
	if v, ok := get("model"); ok {
		s.Model = v
	}
	if v, ok := get("transcribe_model"); ok {
		s.TranscribeModel = v
	}
	if v, ok := get("audio_engine"); ok {
		s.AudioEngine = NormalizeAudioEngine(v)
	}
	if v, ok := get("whisper_model"); ok {
		s.WhisperModel = NormalizeWhisperModel(v)
	}
	if v, ok := get("google_api_key"); ok {
		s.GoogleAPIKey = v
	}
	if v, ok := get("speech_language"); ok {
		s.SpeechLanguage = NormalizeSpeechLanguage(v)
	}
	if v, ok := get("maxchars"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			s.MaxChars = n
		}
	}
	if v, ok := get("workers"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			if n > 100 {
				n = 100
			}
			s.Workers = n
		}
	}
	return s
}

// Save persists every setting that differs from its default and removes the
// stored value once the user returns a setting to the default. This keeps the
// settings table small and makes "reset to defaults" work naturally.
func (s AppSettings) Save(db *sql.DB) error {
	if db == nil {
		return nil
	}
	def := DefaultAppSettings()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	set := func(name, value, defaultValue string) error {
		if value == defaultValue {
			_, err := tx.Exec(`DELETE FROM settings WHERE name=?`, name)
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO settings(name, value) VALUES(?,?)`, name, value)
		return err
	}
	items := [][3]string{
		{"src", s.Src, def.Src},
		{"out", s.Out, def.Out},
		{"host", s.Host, def.Host},
		{"model", s.Model, def.Model},
		{"transcribe_model", s.TranscribeModel, def.TranscribeModel},
		{"audio_engine", s.AudioEngine, def.AudioEngine},
		{"whisper_model", s.WhisperModel, def.WhisperModel},
		{"google_api_key", s.GoogleAPIKey, def.GoogleAPIKey},
		{"speech_language", s.SpeechLanguage, def.SpeechLanguage},
		{"maxchars", strconv.Itoa(s.MaxChars), strconv.Itoa(def.MaxChars)},
		{"workers", strconv.Itoa(s.Workers), strconv.Itoa(def.Workers)},
	}
	for _, it := range items {
		if err := set(it[0], it[1], it[2]); err != nil {
			return err
		}
	}
	return tx.Commit()
}
