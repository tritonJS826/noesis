package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncApplyStoresRelativePath(t *testing.T) {
	db := openTestDB(t)
	src := t.TempDir()
	sub := filepath.Join(src, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "new.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, imported, err := SyncApply(db, src, nil, []string{file}, ImportOptions{})
	if err != nil {
		t.Fatalf("SyncApply: %v", err)
	}
	if removed != 0 || imported != 1 {
		t.Fatalf("removed=%d imported=%d, want 0/1", removed, imported)
	}
	var stored string
	if err := db.QueryRow(`SELECT file_path FROM notes`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "sub/new.txt" {
		t.Fatalf("stored path = %q, want sub/new.txt", stored)
	}
}

func TestSyncApplyRemovedCountIsNotesOnly(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)
	if _, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, imported_at)
		VALUES('dir/b.md','/x/dir/b.md','b.md','x','text/markdown','2024-01-02')`); err != nil {
		t.Fatal(err)
	}
	var otherID int64
	if err := db.QueryRow(`SELECT id FROM notes WHERE file_path='dir/b.md'`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO links(src_id, dst_id, reason) VALUES(?,?, '')`, id, otherID); err != nil {
		t.Fatal(err)
	}

	removed, imported, err := SyncApply(db, t.TempDir(), []int64{id}, nil, ImportOptions{})
	if err != nil {
		t.Fatalf("SyncApply: %v", err)
	}
	if removed != 1 || imported != 0 {
		t.Fatalf("removed=%d imported=%d, want 1/0", removed, imported)
	}
}

func TestSyncApplyReportsReadErrors(t *testing.T) {
	db := openTestDB(t)
	src := t.TempDir()
	missing := filepath.Join(src, "gone.txt")

	_, imported, err := SyncApply(db, src, nil, []string{missing}, ImportOptions{})
	if err == nil {
		t.Fatal("expected an error for the missing file")
	}
	if imported != 0 {
		t.Fatalf("imported=%d, want 0", imported)
	}
}

func TestSyncDiffDetectsChangedFile(t *testing.T) {
	db := openTestDB(t)
	src := t.TempDir()
	file := filepath.Join(src, "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, imported, err := SyncApply(db, src, nil, []string{file}, ImportOptions{}); err != nil || imported != 1 {
		t.Fatalf("import: imported=%d err=%v", imported, err)
	}

	items, err := SyncDiff(db, src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("fresh import shows %d differences, want 0: %+v", len(items), items)
	}

	// Change the content and move the mtime so the fast path is bypassed.
	if err := os.WriteFile(file, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}

	items, err = SyncDiff(db, src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Changed || items[0].ID == 0 {
		t.Fatalf("changed file not detected: %+v", items)
	}
	if items[0].Name != "note.txt" {
		t.Fatalf("changed item name = %q", items[0].Name)
	}
}

func TestSyncApplyChangedClearsMetadata(t *testing.T) {
	db := openTestDB(t)
	src := t.TempDir()
	file := filepath.Join(src, "note.md")
	if err := os.WriteFile(file, []byte("# hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, imported, err := SyncApply(db, src, nil, []string{file}, ImportOptions{}); err != nil || imported != 1 {
		t.Fatalf("import: imported=%d err=%v", imported, err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM notes`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	// Seed derived metadata of every kind.
	for _, q := range []string{
		`INSERT INTO tags(note_id, tag) VALUES(?, 'x')`,
		`INSERT INTO process_output(note_id, process_name, output) VALUES(?, 'Short summary', 's')`,
		`INSERT INTO rag_notes(note_id, hash, model, chunks, dim, indexed_at) VALUES(?, 'h','m',1,1,'now')`,
		`INSERT INTO rag_chunks(note_id, chunk_idx, text, model, dim, embedding, created_at) VALUES(?,0,'t','m',1,'x','now')`,
	} {
		if _, err := db.Exec(q, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := storeNoteRDF(db, id, "@prefix n: <note:> .\nn:this n:mentions n:Alice ."); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file, []byte("# changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}

	done, err := SyncApplyChanged(db, []int64{id}, "")
	if err != nil {
		t.Fatal(err)
	}
	if done != 1 {
		t.Fatalf("refreshed %d notes, want 1", done)
	}
	for _, table := range []string{"tags", "process_output", "note_facts", "rag_notes", "rag_chunks"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s not cleared: %d rows", table, n)
		}
	}
	var content, markdown, stored string
	if err := db.QueryRow(`SELECT content, markdown, src_hash FROM notes WHERE id=?`, id).
		Scan(&content, &markdown, &stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "changed") || markdown != content {
		t.Fatalf("content not refreshed: content=%q markdown=%q", content, markdown)
	}
	if stored == "" {
		t.Fatal("src_hash not updated")
	}

	// After applying, the file no longer shows as changed.
	items, err := SyncDiff(db, src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Changed {
			t.Fatalf("still flagged as changed: %+v", it)
		}
	}
}

func TestSyncApplyHonoursFormatFilter(t *testing.T) {
	db := openTestDB(t)
	src := t.TempDir()
	file := filepath.Join(src, "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, imported, err := SyncApply(db, src, nil, []string{file}, ImportOptions{Formats: FormatSet{"md": true}})
	if err != nil {
		t.Fatalf("SyncApply: %v", err)
	}
	if imported != 0 {
		t.Fatalf("imported=%d, want 0 (txt excluded)", imported)
	}
}
