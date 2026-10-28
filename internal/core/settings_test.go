package core

import (
	"strconv"
	"testing"
)

func TestAppSettingsDefaults(t *testing.T) {
	db := openTestDB(t)

	got := LoadAppSettings(db)
	want := DefaultAppSettings()
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if want.MaxChars != 400000 || want.Workers != 2 {
		t.Fatalf("unexpected new defaults: maxchars=%d workers=%d", want.MaxChars, want.Workers)
	}
	if want.AudioEngine != AudioEngineWhisper || want.WhisperModel != DefaultWhisperModel || want.SpeechLanguage != DefaultSpeechLanguage {
		t.Fatalf("unexpected audio defaults: %#v", want)
	}
}

func TestAppSettingsRoundTrip(t *testing.T) {
	db := openTestDB(t)

	custom := DefaultAppSettings()
	custom.Src = "/data/notes"
	custom.Out = "/data/out"
	custom.Host = "http://example:11434"
	custom.Model = "vision-model"
	custom.TranscribeModel = "whisper-large"
	custom.AudioEngine = AudioEngineGoogle
	custom.WhisperModel = "small"
	custom.GoogleAPIKey = "test-key"
	custom.SpeechLanguage = "en-US"
	custom.MaxChars = 12345
	custom.Workers = 7
	if err := custom.Save(db); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := LoadAppSettings(db)
	if got != custom {
		t.Fatalf("got %#v, want %#v", got, custom)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 11 {
		t.Fatalf("got %d saved settings, want 11", rows)
	}
}

func TestAppSettingsRemovesDefaults(t *testing.T) {
	db := openTestDB(t)

	custom := DefaultAppSettings()
	custom.MaxChars = 1000
	custom.Workers = 4
	if err := custom.Save(db); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := DefaultAppSettings().Save(db); err != nil {
		t.Fatalf("save defaults: %v", err)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("defaults should be removed, got %d rows", rows)
	}
	if got := LoadAppSettings(db); got != DefaultAppSettings() {
		t.Fatalf("got %#v, want defaults", got)
	}
}

func TestAppSettingsInvalidIntsIgnored(t *testing.T) {
	db := openTestDB(t)
	for name, value := range map[string]string{"maxchars": "nope", "workers": "0"} {
		if _, err := db.Exec(`INSERT OR REPLACE INTO settings(name, value) VALUES(?,?)`, name, value); err != nil {
			t.Fatal(err)
		}
	}
	got := LoadAppSettings(db)
	def := DefaultAppSettings()
	if got.MaxChars != def.MaxChars || got.Workers != def.Workers {
		t.Fatalf("invalid values were applied: maxchars=%d workers=%d", got.MaxChars, got.Workers)
	}
	if strconv.Itoa(got.MaxChars) != "400000" {
		t.Fatalf("unexpected maxchars %d", got.MaxChars)
	}
}

func TestNewBuiltinSinglePromptsSeeded(t *testing.T) {
	db := openTestDB(t)
	for _, name := range []string{DataExtractionProcess, DefaultSummaryProcess} {
		body := LoadPrompt(db, name)
		if body == "" {
			t.Fatalf("prompt %q was not seeded", name)
		}
		var typ string
		if err := db.QueryRow(`SELECT type FROM prompts WHERE name=?`, name).Scan(&typ); err != nil {
			t.Fatalf("prompt %q: %v", name, err)
		}
		if typ != "single" {
			t.Fatalf("prompt %q type = %q, want single", name, typ)
		}
	}
}

func TestLimitRunes(t *testing.T) {
	long := ""
	for i := 0; i < 600; i++ {
		long += "я"
	}
	got := limitRunes(long, SummaryMaxChars)
	if n := len([]rune(got)); n != SummaryMaxChars {
		t.Fatalf("got %d runes, want %d", n, SummaryMaxChars)
	}
	if got == long {
		t.Fatal("long summary was not truncated")
	}
	if short := limitRunes("коротко", SummaryMaxChars); short != "коротко" {
		t.Fatalf("short summary changed: %q", short)
	}
}

func TestNoteRecordIncludesProcessOutput(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)
	if err := SaveProcessOutput(db, id, DefaultSummaryProcess, "very short"); err != nil {
		t.Fatal(err)
	}
	if err := SaveProcessOutput(db, id, DataExtractionProcess, "extracted text"); err != nil {
		t.Fatal(err)
	}

	fields, err := NoteRecord(db, id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range fields {
		got[f.Name] = f.Value
	}
	if got["process: "+DefaultSummaryProcess] != "very short" {
		t.Fatalf("summary output missing: %#v", got)
	}
	if got["process: "+DataExtractionProcess] != "extracted text" {
		t.Fatalf("extraction output missing: %#v", got)
	}
}
