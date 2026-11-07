package core

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportPath(t *testing.T) {
	cases := []struct {
		rel, template, want string
	}{
		{"dir/a.md", DefaultExportTemplate, "dir/a.md"},
		{"dir/a.pdf", DefaultExportTemplate, "dir/a.md"},
		{"dir/a.pdf", DefaultRDFExportTemplate, "dir/a.md"},
		{"dir/a.txt", "", "dir/a.md"},
		{"dir/a.pdf", "custom", "dir/a.pdf"},
		{"noext", DefaultExportTemplate, "noext.md"},
		{"dir/.env", DefaultExportTemplate, "dir/.env.md"},
	}
	for _, c := range cases {
		if got := exportPath(c.rel, c.template); got != c.want {
			t.Fatalf("exportPath(%q, %q) = %q, want %q", c.rel, c.template, got, c.want)
		}
	}
}

func TestPhase3ExportsPlainTextForDocuments(t *testing.T) {
	db := openTestDB(t)
	raw := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 binary payload"))
	if _, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
		VALUES('docs/a.pdf','/x/docs/a.pdf','a.pdf',?,'application/pdf','Hello from the PDF','2024-01-02')`, raw); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := Phase3(context.Background(), NoopReporter{}, db, out, "", ""); err != nil {
		t.Fatalf("phase3: %v", err)
	}

	dst := filepath.Join(out, "docs", "a.md")
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if got := string(data); !strings.Contains(got, "Hello from the PDF") || strings.Contains(got, raw) {
		t.Fatalf("export is not plain text: %q", got)
	}
}

func TestPhase3RDFExportTemplate(t *testing.T) {
	db := openTestDB(t)
	id := insertNote(t, db)
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
		VALUES('dir/b.md','/x/dir/b.md','b.md','world','text/markdown','world','2024-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := res.LastInsertId()
	rdf := "@prefix n: <note:> .\nn:this n:mentions n:Alice ."
	if _, err := db.Exec(`UPDATE notes SET markdown='Original note', rdf=? WHERE id=?`, rdf, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE notes SET rdf=? WHERE id=?`, rdf, b); err != nil {
		t.Fatal(err)
	}
	if got := LoadExportTemplate(db, DefaultRDFExportTemplate); got != DefaultRDFExportTemplateBody {
		t.Fatalf("RDF template not seeded: %q", got)
	}

	out := t.TempDir()
	if err := Phase3(context.Background(), NoopReporter{}, db, out, "", DefaultRDFExportTemplate); err != nil {
		t.Fatalf("phase3: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(out, "dir", "a.md"))
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "```turtle") || !strings.Contains(got, rdf) {
		t.Fatalf("RDF export missing triples:\n%s", got)
	}
	if !strings.Contains(got, "## Related notes") || !strings.Contains(got, "[[b]]") {
		t.Fatalf("RDF export missing graph links:\n%s", got)
	}
	if strings.Contains(got, "Original note") {
		t.Fatalf("RDF-only export included the note body:\n%s", got)
	}
}

func TestMigrateRDFExportTemplate(t *testing.T) {
	db := openTestDB(t)
	if err := SaveExportTemplate(db, DefaultRDFExportTemplate, legacyRDFExportTemplateBody); err != nil {
		t.Fatal(err)
	}
	migrateRDFExportTemplate(db)
	if got := LoadExportTemplate(db, DefaultRDFExportTemplate); got != DefaultRDFExportTemplateBody {
		t.Fatalf("RDF template was not upgraded: %q", got)
	}

	custom := "custom ${rdf} ${related}"
	if err := SaveExportTemplate(db, DefaultRDFExportTemplate, custom); err != nil {
		t.Fatal(err)
	}
	migrateRDFExportTemplate(db)
	if got := LoadExportTemplate(db, DefaultRDFExportTemplate); got != custom {
		t.Fatalf("user-edited RDF template was overwritten: %q", got)
	}
}

func TestMigrateExportTemplateNames(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`DELETE FROM export_templates`); err != nil {
		t.Fatal(err)
	}
	if err := SaveExportTemplate(db, legacyExportTemplate, "custom body"); err != nil {
		t.Fatal(err)
	}

	migrateExportTemplateNames(db)

	if LoadExportTemplate(db, legacyExportTemplate) != "" {
		t.Fatal("legacy template still exists")
	}
	if got := LoadExportTemplate(db, DefaultExportTemplate); got != "custom body" {
		t.Fatalf("template body was not preserved: %q", got)
	}
}
