package core

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertNote(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, imported_at)
		VALUES('dir/a.md','/x/dir/a.md','a.md','hello','text/markdown','2024-01-02')`)
	if err != nil {
		t.Fatalf("insert note: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestExpandPromptDollarSyntax(t *testing.T) {
	got := expandPrompt("a=${A} b=${B} c=${db.x}", map[string]string{"A": "1", "B": "2", "db.x": "3"})
	if got != "a=1 b=2 c=3" {
		t.Fatalf("got %q", got)
	}
	// Unknown placeholders are left untouched.
	if got := expandPrompt("${UNKNOWN}", nil); got != "${UNKNOWN}" {
		t.Fatalf("unknown placeholder changed: %q", got)
	}
}

func TestNoteDBVars(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)

	vars := noteDBVars(db, "db", id)
	if vars["db.file_name"] != "a.md" || vars["db.file_path"] != "dir/a.md" {
		t.Fatalf("unexpected db vars: %#v", vars)
	}
	got := expandPrompt("${db.file_name} in ${db.file_path}", vars)
	if got != "a.md in dir/a.md" {
		t.Fatalf("got %q", got)
	}
}

// legacyCompose is a copy of the pre-template export implementation, kept here
// so the default template is guaranteed to stay byte-for-byte compatible.
func legacyCompose(content string, tags []string, linksMD string) string {
	fm, body, has := splitFrontMatter(content)
	var existing []string
	if has {
		fm, existing = extractTags(fm)
	}
	seen := map[string]bool{}
	var all []string
	for _, t := range append(existing, tags...) {
		if !seen[t] {
			seen[t] = true
			all = append(all, t)
		}
	}
	var b strings.Builder
	if has || len(all) > 0 {
		b.WriteString("---\n")
		for _, l := range fm {
			b.WriteString(l + "\n")
		}
		if len(all) > 0 {
			b.WriteString("tags:\n")
			for _, t := range all {
				b.WriteString("  - " + t + "\n")
			}
		}
		b.WriteString("---\n\n")
	}
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n")
	if strings.TrimSpace(linksMD) != "" {
		b.WriteString("\n## Related notes\n" + linksMD + "\n")
	}
	return b.String()
}

func TestDefaultExportTemplateMatchesLegacy(t *testing.T) {
	cases := []struct {
		name    string
		content string
		tags    []string
		links   string
	}{
		{"frontmatter and links", "---\ntitle: X\ntags:\n  - old\n---\n\nHello\n", []string{"a"}, "- [[B]] — reason"},
		{"no frontmatter", "Hello", nil, ""},
		{"frontmatter no tags", "---\ntitle: X\n---\nHello\n", nil, ""},
		{"tags no frontmatter", "Hello", []string{"a", "b"}, ""},
		{"blank line after frontmatter", "---\ntitle: X\n---\n\nHello\n", []string{"a"}, "- [[B]]"},
	}
	for _, c := range cases {
		got := expandPrompt(DefaultExportTemplateBody, exportVars(c.content, c.tags, c.links))
		want := legacyCompose(c.content, c.tags, c.links)
		if got != want {
			t.Fatalf("%s:\ngot  %q\nwant %q", c.name, got, want)
		}
	}
}

func TestRenamePrompt(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)

	if err := SavePromptWithType(db, "old", "body", "pair"); err != nil {
		t.Fatal(err)
	}
	if err := SaveProcessOutput(db, id, "old", "out"); err != nil {
		t.Fatal(err)
	}

	if err := RenamePrompt(db, "old", "new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if LoadPrompt(db, "old") != "" {
		t.Fatal("old prompt still exists")
	}
	if LoadPrompt(db, "new") != "body" {
		t.Fatal("new prompt body missing")
	}
	if LoadProcessOutput(db, id, "new") != "out" {
		t.Fatal("process output was not moved")
	}

	// Renaming onto an existing name must fail.
	if err := SavePromptWithType(db, "taken", "x", "pair"); err != nil {
		t.Fatal(err)
	}
	if err := RenamePrompt(db, "new", "taken"); err == nil {
		t.Fatal("expected collision error")
	}
}

func TestMigratePromptNames(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`DELETE FROM prompts WHERE name=?`, DefaultPairPrompt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO prompts(name, prompt, type) VALUES('phase2','legacy','pair')`); err != nil {
		t.Fatal(err)
	}

	migratePromptNames(db)

	if LoadPrompt(db, "phase2") != "" {
		t.Fatal("phase2 should have been migrated away")
	}
	if LoadPrompt(db, DefaultPairPrompt) != "legacy" {
		t.Fatal("expected the legacy prompt to be renamed")
	}
}
