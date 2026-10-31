package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSkipDirPatternsAndMatch(t *testing.T) {
	if got := SkipDirPatterns(" .private , node_modules ,, "); len(got) != 2 || got[0] != ".private" || got[1] != "node_modules" {
		t.Fatalf("SkipDirPatterns = %#v", got)
	}
	if got := SkipDirPatterns(""); got == nil || len(got) != 0 {
		t.Fatalf("empty list should be non-nil and empty, got %#v", got)
	}

	cases := []struct {
		name     string
		patterns []string
		want     bool
	}{
		{".private", []string{".*"}, true},
		{"public", []string{".*"}, false},
		{".private", []string{".private"}, true},
		{"node_modules", []string{".private", "node_modules"}, true},
		{"anything", nil, false},
		{"anything", []string{}, false},
	}
	for _, c := range cases {
		if got := ShouldSkipDir(c.name, c.patterns); got != c.want {
			t.Fatalf("ShouldSkipDir(%q, %#v) = %v, want %v", c.name, c.patterns, got, c.want)
		}
	}
}

func TestImportHonoursSkipDirs(t *testing.T) {
	src := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("keep.txt", "keep")
	write(".private/secret.txt", "secret")
	write("node_modules/dep.txt", "dep")

	imported := func(t *testing.T, skip []string) map[string]bool {
		t.Helper()
		db := openTestDB(t)
		err := Import(context.Background(), NoopReporter{}, db, src, ImportOptions{
			Source:   SourceFilesystem,
			Formats:  FormatSet{"txt": true},
			SkipDirs: skip,
		})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		rows, err := db.Query(`SELECT file_path FROM notes`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]bool{}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			got[p] = true
		}
		return got
	}

	// Default pattern (".*") skips hidden folders but keeps everything else.
	def := imported(t, SkipDirPatterns(DefaultSkipDirs))
	if !def["keep.txt"] || !def["node_modules/dep.txt"] {
		t.Fatalf("default skip dropped visible files: %#v", def)
	}
	if def[".private/secret.txt"] {
		t.Fatalf("default skip did not drop .private: %#v", def)
	}

	// An explicit name list only skips the named folder.
	custom := imported(t, SkipDirPatterns(".private"))
	if !custom["keep.txt"] || !custom["node_modules/dep.txt"] || custom[".private/secret.txt"] {
		t.Fatalf("custom skip list wrong: %#v", custom)
	}

	// An empty list skips nothing.
	none := imported(t, SkipDirPatterns(""))
	if !none[".private/secret.txt"] {
		t.Fatalf("empty skip list should import hidden folders: %#v", none)
	}
}

func TestRemoveNotesByIDs(t *testing.T) {
	db := openTestDB(t)
	id1 := insertNote(t, db)
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, imported_at)
		VALUES('dir/b.md','/x/dir/b.md','b.md','bye','text/markdown','2024-01-03')`)
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := res.LastInsertId()

	removed, err := RemoveNotesByIDs(db, []int64{id1, id2})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("notes left = %d, want 0", count)
	}

	// No ids is a no-op.
	if n, err := RemoveNotesByIDs(db, nil); err != nil || n != 0 {
		t.Fatalf("empty remove = (%d, %v), want (0, nil)", n, err)
	}
}
