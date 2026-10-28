package core

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractDataForAllDocuments(t *testing.T) {
	db := openTestDB(t)

	rtf := []byte(`{\rtf1\ansi{\fonttbl{\f0 Arial;}}Hello \b world\b0\par Second line}`)
	path := filepath.Join(t.TempDir(), "sample.rtf")
	if err := os.WriteFile(path, rtf, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, imported_at)
		VALUES('sample.rtf',?,?,'','application/rtf','2024-01-02')`,
		path, "sample.rtf")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	// Make sure the fallback base64 path also works if the file disappears.
	if _, err := db.Exec(`UPDATE notes SET content=? WHERE id=?`, base64.StdEncoding.EncodeToString(rtf), id); err != nil {
		t.Fatal(err)
	}

	if err := ExtractDataForAll(context.Background(), NoopReporter{}, db, DefaultConfig()); err != nil {
		t.Fatalf("extract: %v", err)
	}

	var extracted, tool, extractErr, extractedPath string
	if err := db.QueryRow(`SELECT extracted_text, extracted_path, extract_tool, extract_error FROM notes WHERE id=?`, id).
		Scan(&extracted, &extractedPath, &tool, &extractErr); err != nil {
		t.Fatal(err)
	}
	extracted = resolveExtractedText(db, extracted, extractedPath)
	if !strings.Contains(extracted, "Hello world") || !strings.Contains(extracted, "Second line") {
		t.Fatalf("unexpected extracted text: %q", extracted)
	}
	if tool != "go:rtf" {
		t.Fatalf("tool = %q, want go:rtf", tool)
	}
	if extractErr != "" {
		t.Fatalf("unexpected extraction error: %q", extractErr)
	}
	if got := LoadProcessOutput(db, id, DataExtractionProcess); got != extracted {
		t.Fatalf("process output = %q, want %q", got, extracted)
	}
}
