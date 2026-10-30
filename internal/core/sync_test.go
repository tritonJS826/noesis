package core

import (
	"os"
	"path/filepath"
	"testing"
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
