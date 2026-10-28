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

func TestMigratePromptPlaceholders(t *testing.T) {
	db := openTestDB(t)
	legacy := "=== {FILE_NAME} ===\n${CONTENT}\n{LINKED_NOTES}\n{\"novelty\":\"high\"}"
	if err := SavePrompt(db, "legacy", legacy); err != nil {
		t.Fatal(err)
	}
	if err := SaveExportTemplate(db, "legacy", "{body}${tags}"); err != nil {
		t.Fatal(err)
	}

	migratePromptPlaceholders(db)

	got := LoadPrompt(db, "legacy")
	want := "=== ${FILE_NAME} ===\n${CONTENT}\n${LINKED_NOTES}\n{\"novelty\":\"high\"}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := LoadExportTemplate(db, "legacy"); got != "${body}${tags}" {
		t.Fatalf("template got %q", got)
	}
}

func TestExpandPromptDoesNotExpandValues(t *testing.T) {
	got := expandPrompt("${CONTENT}", map[string]string{"CONTENT": "literal {FILE_NAME}", "FILE_NAME": "a.md"})
	if got != "literal {FILE_NAME}" {
		t.Fatalf("inserted value was re-expanded: %q", got)
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

func TestNoteRecord(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)
	if _, err := db.Exec(`INSERT INTO tags(note_id, tag) VALUES(?,?), (?,?)`, id, "b", id, "a"); err != nil {
		t.Fatal(err)
	}

	fields, err := NoteRecord(db, id)
	if err != nil {
		t.Fatalf("note record: %v", err)
	}
	got := make(map[string]string, len(fields))
	for _, f := range fields {
		got[f.Name] = f.Value
	}
	if got["file_name"] != "a.md" || got["file_path"] != "dir/a.md" || got["content"] != "hello" {
		t.Fatalf("unexpected record: %#v", got)
	}
	if got["tags"] != "a, b" {
		t.Fatalf("unexpected tags: %q", got["tags"])
	}
	if len(fields) != len(NotesColumns(db))+1 {
		t.Fatalf("got %d fields, want %d columns + tags", len(fields), len(NotesColumns(db)))
	}
}

func TestOpenDBMigratesLegacyPrompts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SavePrompt(db, "legacy", "=== {FILE_NAME} ===\n{CONTENT}"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if got := LoadPrompt(db, "legacy"); got != "=== ${FILE_NAME} ===\n${CONTENT}" {
		t.Fatalf("legacy prompt was not migrated: %q", got)
	}
	if got := LoadPrompt(db, "eval"); strings.Contains(got, "$${") {
		t.Fatalf("supported placeholder was double-expanded: %q", got)
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
