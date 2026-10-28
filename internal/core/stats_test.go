package core

import (
	"testing"
)

func TestLoadFormatStats(t *testing.T) {
	db := openTestDB(t)
	add := func(path, name, markdown string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
			VALUES(?,?,?,'','text/plain',?,'2024-01-02')`, path, "/x/"+path, name, markdown); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	add("a.mp3", "a.mp3", "")
	add("b.mp3", "b.mp3", "transcript")
	add("sub/c.MP3", "c.MP3", "")
	add("d.md", "d.md", "hello")
	add("sub/e.MD", "e.MD", "world")
	add("README", "README", "no extension")

	stats := LoadFormatStats(db)
	got := map[string]FormatStat{}
	for _, st := range stats {
		got[st.Ext] = st
	}
	if got["mp3"].Total != 3 || got["mp3"].Extracted != 1 {
		t.Fatalf("mp3 = %#v", got["mp3"])
	}
	if got["md"].Total != 2 || got["md"].Extracted != 2 {
		t.Fatalf("md = %#v", got["md"])
	}
	if got["(none)"].Total != 1 || got["(none)"].Extracted != 1 {
		t.Fatalf("(none) = %#v", got["(none)"])
	}
	// Results are ordered by extension: "(none)" < md < mp3.
	if len(stats) != 3 || stats[0].Ext != "(none)" || stats[1].Ext != "md" || stats[2].Ext != "mp3" {
		t.Fatalf("order = %#v", stats)
	}
}

func TestFormatSelectionRoundTrip(t *testing.T) {
	if got := EncodeFormatSelection(nil); got != "*" {
		t.Fatalf("nil = %q, want *", got)
	}
	if got := EncodeFormatSelection(FormatSet{}); got != "none" {
		t.Fatalf("none = %q", got)
	}
	if got := EncodeFormatSelection(FormatSet{"png": true, "pdf": true}); got != "pdf,png" {
		t.Fatalf("set = %q, want pdf,png", got)
	}

	if _, ok := DecodeFormatSelection(""); ok {
		t.Fatal("empty value should not be considered set")
	}
	fs, ok := DecodeFormatSelection("*")
	if !ok || fs != nil {
		t.Fatalf("* -> %v, %v", fs, ok)
	}
	fs, ok = DecodeFormatSelection("none")
	if !ok || fs == nil || len(fs) != 0 {
		t.Fatalf("none -> %v, %v", fs, ok)
	}
	fs, ok = DecodeFormatSelection("PDF, png")
	if !ok || !fs["pdf"] || !fs["png"] || len(fs) != 2 {
		t.Fatalf("list -> %v, %v", fs, ok)
	}
}

func TestSavedImportFormats(t *testing.T) {
	db := openTestDB(t)
	if got := SavedImportFormats(db); len(got) != len(defaultFormatSet()) {
		t.Fatalf("default fallback = %v", got)
	}

	custom := DefaultAppSettings()
	custom.Formats = "png,pdf"
	if err := custom.Save(db); err != nil {
		t.Fatal(err)
	}
	got := SavedImportFormats(db)
	if !got["png"] || !got["pdf"] || len(got) != 2 {
		t.Fatalf("saved = %v", got)
	}

	custom.Formats = "*"
	if err := custom.Save(db); err != nil {
		t.Fatal(err)
	}
	if got := SavedImportFormats(db); got != nil {
		t.Fatalf("all = %v, want nil", got)
	}
}

func TestLinkedNotesText(t *testing.T) {
	db := openTestDB(t)
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
		VALUES('a.md','/x/a.md','a.md','','text/markdown','note A','2024-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	aID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
		VALUES('b.md','/x/b.md','b.md','','text/markdown','note B','2024-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	bID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO links(src_id, dst_id, reason) VALUES(?,?,?)`, aID, bID, "same topic"); err != nil {
		t.Fatal(err)
	}

	got := linkedNotesText(db, aID, 1000)
	if got != "=== b.md ===\nnote B" {
		t.Fatalf("linkedNotesText = %q", got)
	}
	if got := linkedNotesText(db, bID, 1000); got != "" {
		t.Fatalf("unlinked note = %q", got)
	}
}
