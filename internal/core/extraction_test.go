package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataExtractionDescribesImagesWithVisionModel(t *testing.T) {
	src := t.TempDir()
	// Not a real PNG: the vision path only forwards the bytes to the model.
	if err := os.WriteFile(filepath.Join(src, "cat.png"), []byte("PNGfake"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model  string   `json:"model"`
			Images []string `json:"images"`
		}
		_ = json.Unmarshal(body, &payload)
		gotModel = payload.Model
		if len(payload.Images) == 0 {
			t.Errorf("no images attached to the vision request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"response":"a small cat"}`)
	}))
	defer srv.Close()

	db := openTestDB(t)
	if err := Import(context.Background(), NoopReporter{}, db, src, ImportOptions{
		Source:  SourceFilesystem,
		Formats: FormatSet{"png": true},
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Host = srv.URL
	cfg.Model = "main-model"
	cfg.VisionModel = "vision-model"
	if err := ExtractAll(context.Background(), NoopReporter{}, db, cfg, ExtractFilter{}, true); err != nil {
		t.Fatalf("extract: %v", err)
	}

	var markdown, extractErr, tool string
	if err := db.QueryRow(`SELECT markdown, extract_error, extract_tool FROM notes WHERE file_name='cat.png'`).
		Scan(&markdown, &extractErr, &tool); err != nil {
		t.Fatal(err)
	}
	if markdown != "a small cat" || extractErr != "" || tool != "ollama:vision" {
		t.Fatalf("markdown=%q error=%q tool=%q", markdown, extractErr, tool)
	}
	if gotModel != "vision-model" {
		t.Fatalf("vision model = %q, want vision-model", gotModel)
	}
}

func TestVisionModelFallsBackToMainModel(t *testing.T) {
	c := Config{Model: "main", VisionModel: ""}
	if got := c.visionModel(); got != "main" {
		t.Fatalf("visionModel() = %q, want main", got)
	}
	c.VisionModel = "vision"
	if got := c.visionModel(); got != "vision" {
		t.Fatalf("visionModel() = %q, want vision", got)
	}
}

func TestExtractFilterMatch(t *testing.T) {
	cases := []struct {
		name   string
		filter ExtractFilter
		path   string
		file   string
		mime   string
		want   bool
	}{
		{"empty matches", ExtractFilter{}, "docs/a.pdf", "a.pdf", "application/pdf", true},
		{"name hit path", ExtractFilter{Name: "docs"}, "docs/a.pdf", "a.pdf", "application/pdf", true},
		{"name hit file", ExtractFilter{Name: "A.PDF"}, "docs/a.pdf", "a.pdf", "application/pdf", true},
		{"name miss", ExtractFilter{Name: "zzz"}, "docs/a.pdf", "a.pdf", "application/pdf", false},
		{"format allowed", ExtractFilter{Formats: FormatSet{"pdf": true}}, "a.pdf", "a.pdf", "application/pdf", true},
		{"format blocked", ExtractFilter{Formats: FormatSet{"png": true}}, "a.pdf", "a.pdf", "application/pdf", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.filter.Match(tc.path, tc.file, tc.mime); got != tc.want {
				t.Fatalf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractFormatSet(t *testing.T) {
	if got := ExtractFormatSet([]string{"", " "}); got != nil {
		t.Fatalf("empty set = %v, want nil", got)
	}
	got := ExtractFormatSet([]string{".PDF", " png "})
	if !got["pdf"] || !got["png"] || len(got) != 2 {
		t.Fatalf("set = %v", got)
	}
}

func TestOpusAudioFormat(t *testing.T) {
	found := false
	for _, f := range KnownFormats() {
		if f.Ext == "opus" {
			found = true
			if f.Kind != "audio" {
				t.Fatalf("opus kind = %q, want audio", f.Kind)
			}
		}
	}
	if !found {
		t.Fatal("opus missing from KnownFormats")
	}
	if !IsAudio("audio/opus") {
		t.Fatal("audio/opus should be audio")
	}
	if !IsExtractable("voice.opus", "audio/opus") {
		t.Fatal("opus should be extractable")
	}
	if !CanExtract("voice.opus", "audio/opus") {
		t.Fatal("opus should be extractable")
	}
	def := false
	for _, e := range DefaultImportFormats() {
		if e == "opus" {
			def = true
		}
	}
	if !def {
		t.Fatal("opus missing from DefaultImportFormats")
	}
}

func TestExtractableFormatsExcludeText(t *testing.T) {
	for _, f := range ExtractableFormats() {
		if f.Kind == "text" || f.Kind == "video" {
			t.Fatalf("ExtractableFormats returned %q (%s)", f.Ext, f.Kind)
		}
	}
	found := false
	for _, f := range ExtractableFormats() {
		if f.Ext == "png" {
			found = true
		}
	}
	if !found {
		t.Fatal("png missing from ExtractableFormats")
	}
}

func TestExtractAllFilterNarrowsCandidates(t *testing.T) {
	src := t.TempDir()
	// Two documents; only the docx should be re-extracted when filtered.
	docx := makeZip(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Fresh text</w:t></w:r></w:p></w:body></w:document>`,
	})
	if err := os.WriteFile(filepath.Join(src, "keep.docx"), docx, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "skip.txt"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t)
	if err := Import(context.Background(), NoopReporter{}, db, src, ImportOptions{
		Source:  SourceFilesystem,
		Formats: FormatSet{"docx": true, "txt": true},
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	// Clear the extracted markdown so a re-extract has work to do.
	if _, err := db.Exec(`UPDATE notes SET markdown=''`); err != nil {
		t.Fatal(err)
	}

	filter := ExtractFilter{Name: "keep", Formats: FormatSet{"docx": true}}
	if err := ExtractAll(context.Background(), NoopReporter{}, db, DefaultConfig(), filter, true); err != nil {
		t.Fatalf("extract all: %v", err)
	}

	var docxMD, txtMD string
	_ = db.QueryRow(`SELECT markdown FROM notes WHERE file_name='keep.docx'`).Scan(&docxMD)
	_ = db.QueryRow(`SELECT markdown FROM notes WHERE file_name='skip.txt'`).Scan(&txtMD)
	if !strings.Contains(docxMD, "Fresh text") {
		t.Fatalf("docx markdown = %q", docxMD)
	}
	if txtMD != "" {
		t.Fatalf("txt markdown should be untouched, got %q", txtMD)
	}
}
