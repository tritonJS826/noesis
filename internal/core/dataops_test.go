package core

import "testing"

// Regression: process_output.note_id references notes(id); deleting a note that
// has saved process output must not violate the foreign key.
func TestDeleteNoteRemovesProcessOutput(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)
	if err := SaveProcessOutput(db, id, DataExtractionProcess, "extracted text"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tags(note_id, tag) VALUES(?,?)`, id, "topic"); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveNoteByID(db, id)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM process_output WHERE note_id=?`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("process_output rows left: %d", rows)
	}
}

func TestRemoveNoteAndLinksWithOutput(t *testing.T) {
	db := openTestDB(t)
	// Two notes where the second links to the first.
	a := insertNote(t, db)
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, imported_at)
		VALUES('dir/b.md','/x/dir/b.md','b.md','world','text/markdown','2024-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO links(src_id, dst_id, reason) VALUES(?,?,?)`, b, a, "related"); err != nil {
		t.Fatal(err)
	}
	if err := SaveProcessOutput(db, a, DefaultSummaryProcess, "short"); err != nil {
		t.Fatal(err)
	}

	removed, scrubbed, err := RemoveNoteAndLinksByID(db, a)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if removed != 1 || scrubbed != 1 {
		t.Fatalf("removed=%d scrubbed=%d", removed, scrubbed)
	}
	var links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM links WHERE src_id=? OR dst_id=?`, a, a).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("links left: %d", links)
	}
}
