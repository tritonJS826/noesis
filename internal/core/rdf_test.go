package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRDFEntityLabels(t *testing.T) {
	rdf := `@prefix n: <note:> .
n:this n:mentions n:Alice .
n:this n:feels "very calm" .
n:this n:count "42" .
n:this n:empty "" .`
	got := rdfEntityLabels(rdf)
	if _, ok := got["alice"]; !ok {
		t.Fatalf("missing entity alice: %#v", got)
	}
	for _, unwanted := range []string{"this", "very calm", "42", ""} {
		if _, ok := got[unwanted]; ok {
			t.Fatalf("unexpected entity key %q in %#v", unwanted, got)
		}
	}
}

func TestMigrateRDFPrompt(t *testing.T) {
	db := openTestDB(t)
	if err := SavePrompt(db, RDFProcess, legacyRDFFallback); err != nil {
		t.Fatal(err)
	}
	migrateRDFPrompt(db)
	if got := LoadPrompt(db, RDFProcess); got != rdfFallback {
		t.Fatalf("RDF prompt was not upgraded: %q", got)
	}

	if err := SavePrompt(db, RDFProcess, "my custom prompt"); err != nil {
		t.Fatal(err)
	}
	migrateRDFPrompt(db)
	if got := LoadPrompt(db, RDFProcess); got != "my custom prompt" {
		t.Fatalf("user-edited RDF prompt was overwritten: %q", got)
	}
}

func TestRunRDFProcessBuildsGraphLinks(t *testing.T) {
	db := openTestDB(t)
	a := insertNote(t, db)
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
		VALUES('dir/b.md','/x/dir/b.md','b.md','world','text/markdown','world','2024-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := res.LastInsertId()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"m"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":"@prefix n: <note:> .\nn:this n:mentions n:Alice ."}`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Host = srv.URL
	cfg.Model = "m"
	cfg.PromptName = RDFProcess
	cfg.Workers = 2
	if err := RunSingleProcess(context.Background(), NoopReporter{}, db, cfg); err != nil {
		t.Fatalf("run RDF process: %v", err)
	}

	for _, id := range []int64{a, b} {
		var rdf string
		if err := db.QueryRow(`SELECT rdf FROM notes WHERE id=?`, id).Scan(&rdf); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rdf, "n:Alice") {
			t.Fatalf("note %d has no RDF output: %q", id, rdf)
		}
	}
	var links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM links WHERE reason LIKE 'RDF shared:%'`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Fatalf("RDF process created %d graph links, want 2", links)
	}
}

func TestRebuildRDFLinks(t *testing.T) {
	db := openTestDB(t)
	a := insertNote(t, db)
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
		VALUES('dir/b.md','/x/dir/b.md','b.md','world','text/markdown','world','2024-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := res.LastInsertId()

	if _, err := db.Exec(`UPDATE notes SET rdf=? WHERE id=?`,
		"@prefix n: <note:> .\nn:this n:mentions n:Alice .\nn:this n:has_topic n:Gardening .", a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE notes SET rdf=? WHERE id=?`,
		"@prefix n: <note:> .\nn:Alice n:works_with n:Bob .\nn:this n:has_topic n:Gardening .", b); err != nil {
		t.Fatal(err)
	}

	created, err := rebuildRDFLinks(db)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if created != 2 {
		t.Fatalf("created %d links, want 2 bidirectional links", created)
	}

	var links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM links WHERE reason LIKE 'RDF shared:%'`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Fatalf("stored %d RDF links, want 2", links)
	}

	for id, other := range map[int64]string{a: "b", b: "a"} {
		var linksMD string
		if err := db.QueryRow(`SELECT links_md FROM notes WHERE id=?`, id).Scan(&linksMD); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(linksMD, "[["+other+"]]") {
			t.Fatalf("note %d links_md = %q, want [[%s]]", id, linksMD, other)
		}
		if !strings.Contains(linksMD, rdfLinkReasonPrefix) {
			t.Fatalf("note %d links_md missing RDF reason: %q", id, linksMD)
		}
	}

	// Re-running must refresh instead of duplicating links.
	created, err = rebuildRDFLinks(db)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if created != 2 {
		t.Fatalf("second rebuild refreshed %d links, want 2", created)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM links WHERE reason LIKE 'RDF shared:%'`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Fatalf("second rebuild left %d RDF links, want 2", links)
	}

	// Changing the RDF removes links that no longer share an entity.
	if _, err := db.Exec(`UPDATE notes SET rdf=? WHERE id=?`,
		"@prefix n: <note:> .\nn:this n:mentions n:Bob .", b); err != nil {
		t.Fatal(err)
	}
	created, err = rebuildRDFLinks(db)
	if err != nil {
		t.Fatalf("third rebuild: %v", err)
	}
	if created != 0 {
		t.Fatalf("third rebuild created %d links, want 0", created)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM links WHERE reason LIKE 'RDF shared:%'`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("stale RDF links left: %d", links)
	}
	var linksMD string
	if err := db.QueryRow(`SELECT links_md FROM notes WHERE id=?`, a).Scan(&linksMD); err != nil {
		t.Fatal(err)
	}
	if linksMD != "" {
		t.Fatalf("stale links_md left: %q", linksMD)
	}
}
