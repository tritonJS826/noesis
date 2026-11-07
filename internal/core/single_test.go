package core

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRDFProcessStoresInNotesColumn(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)
	rdf := "@prefix n: <note:> .\nn:this n:mentions \"Alice\" ."

	if err := SaveProcessOutput(db, id, RDFProcess, "legacy output"); err != nil {
		t.Fatal(err)
	}
	if err := saveSingleProcessOutput(db, id, RDFProcess, rdf); err != nil {
		t.Fatalf("save RDF: %v", err)
	}

	var got string
	if err := db.QueryRow(`SELECT rdf FROM notes WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != rdf {
		t.Fatalf("rdf column = %q, want %q", got, rdf)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM process_output WHERE note_id=? AND process_name=?`, id, RDFProcess).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("process_output rows left for RDF: %d", rows)
	}
	if got := LoadProcessOutput(db, id, RDFProcess); got != rdf {
		t.Fatalf("LoadProcessOutput = %q, want %q", got, rdf)
	}
}

func TestMigrateRDFProcessOutput(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)
	if err := SaveProcessOutput(db, id, RDFProcess, "legacy rdf"); err != nil {
		t.Fatal(err)
	}

	migrateRDFProcessOutput(db)

	var got string
	if err := db.QueryRow(`SELECT rdf FROM notes WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "legacy rdf" {
		t.Fatalf("rdf column = %q, want legacy output", got)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM process_output WHERE process_name=?`, RDFProcess).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("legacy RDF process rows left: %d", rows)
	}

	// An existing RDF value must win over an older process_output row.
	if _, err := db.Exec(`UPDATE notes SET rdf='keep me' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := SaveProcessOutput(db, id, RDFProcess, "newer legacy"); err != nil {
		t.Fatal(err)
	}
	migrateRDFProcessOutput(db)
	if err := db.QueryRow(`SELECT rdf FROM notes WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "keep me" {
		t.Fatalf("rdf column was overwritten: %q", got)
	}
}

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

	var extracted, tool, extractErr string
	if err := db.QueryRow(`SELECT markdown, extract_tool, extract_error FROM notes WHERE id=?`, id).
		Scan(&extracted, &tool, &extractErr); err != nil {
		t.Fatal(err)
	}
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
