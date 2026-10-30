package core

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
)

// EncodeFormatSelection serialises an import-format selection for the settings
// table: nil (every format) becomes "*", an empty non-nil set (none) becomes
// "none", and anything else becomes a comma-separated sorted extension list.
// An empty string means "not chosen".
func EncodeFormatSelection(fs FormatSet) string {
	if fs == nil {
		return "*"
	}
	if len(fs) == 0 {
		return "none"
	}
	exts := make([]string, 0, len(fs))
	for e, on := range fs {
		if on {
			exts = append(exts, e)
		}
	}
	sort.Strings(exts)
	return strings.Join(exts, ",")
}

// DecodeFormatSelection parses a stored selection. ok is false when raw is
// empty (the caller should fall back to DefaultImportFormats). A returned nil
// set means "every format".
func DecodeFormatSelection(raw string) (fs FormatSet, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	switch raw {
	case "*":
		return nil, true
	case "none":
		return FormatSet{}, true
	}
	fs = FormatSet{}
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
		if e != "" {
			fs[e] = true
		}
	}
	return fs, true
}

// SavedImportFormats returns the import-format selection stored for the
// database, falling back to DefaultImportFormats when none was saved.
func SavedImportFormats(db *sql.DB) FormatSet {
	if fs, ok := DecodeFormatSelection(LoadAppSettings(db).Formats); ok {
		return fs
	}
	return defaultFormatSet()
}

// AppSettings is the subset of UI settings persisted in the database. The
// database path itself is not stored here: it must be known before the
// database can be opened.
type AppSettings struct {
	Src             string
	Out             string
	Host            string
	Model           string
	VisionModel     string
	TranscribeModel string
	AudioEngine     string
	WhisperModel    string
	WhisperBackend  string
	WhisperDevice   int
	BinaryStorage   string
	GoogleAPIKey    string
	SpeechLanguage  string
	MaxChars        int
	Workers         int
	// Formats holds the import-format selection as stored in the settings
	// table. Empty means "not chosen — use DefaultImportFormats"; see
	// EncodeFormatSelection / DecodeFormatSelection.
	Formats string
}

// DefaultAppSettings returns the initial settings used for new databases.
func DefaultAppSettings() AppSettings {
	c := DefaultConfig()
	return AppSettings{
		Src:             ".",
		Out:             "out",
		Host:            c.Host,
		Model:           c.Model,
		VisionModel:     c.VisionModel,
		TranscribeModel: c.TranscribeModel,
		AudioEngine:     c.AudioEngine,
		WhisperModel:    c.WhisperModel,
		WhisperBackend:  c.WhisperBackend,
		WhisperDevice:   c.WhisperDevice,
		BinaryStorage:   c.BinaryStorage,
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
	if v, ok := get("vision_model"); ok {
		s.VisionModel = v
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
	if v, ok := get("whisper_backend"); ok {
		s.WhisperBackend = NormalizeWhisperBackend(v)
	}
	if v, ok := get("whisper_device"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			s.WhisperDevice = n
		}
	}
	if v, ok := get("binary_storage"); ok {
		s.BinaryStorage = NormalizeBinaryStorage(v)
	}
	if v, ok := get("google_api_key"); ok {
		s.GoogleAPIKey = v
	}
	if v, ok := get("speech_language"); ok {
		s.SpeechLanguage = NormalizeSpeechLanguage(v)
	}
	if v, ok := get("import_formats"); ok {
		s.Formats = v
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
		{"vision_model", s.VisionModel, def.VisionModel},
		{"transcribe_model", s.TranscribeModel, def.TranscribeModel},
		{"audio_engine", s.AudioEngine, def.AudioEngine},
		{"whisper_model", s.WhisperModel, def.WhisperModel},
		{"whisper_backend", NormalizeWhisperBackend(s.WhisperBackend), NormalizeWhisperBackend(def.WhisperBackend)},
		{"whisper_device", strconv.Itoa(s.WhisperDevice), strconv.Itoa(def.WhisperDevice)},
		{"binary_storage", NormalizeBinaryStorage(s.BinaryStorage), def.BinaryStorage},
		{"google_api_key", s.GoogleAPIKey, def.GoogleAPIKey},
		{"speech_language", s.SpeechLanguage, def.SpeechLanguage},
		{"import_formats", s.Formats, def.Formats},
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
