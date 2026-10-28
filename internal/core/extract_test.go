package core

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractDocumentFormats(t *testing.T) {
	docx := makeZip(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
			<w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t xml:space="preserve"> world</w:t></w:r></w:p>
			<w:p><w:r><w:t>Second paragraph</w:t></w:r></w:p>
		</w:body></w:document>`,
	})
	pptx := makeZip(t, map[string]string{
		"ppt/slides/slide10.xml": `<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:r><a:t>Ten</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide2.xml":  `<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:p><a:r><a:t>Two</a:t></a:r></a:p></p:sld>`,
	})
	xlsx := makeZip(t, map[string]string{
		"xl/sharedStrings.xml": `<sst><si><t>Name</t></si><si><t>Alice</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
			<row><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
			<row><c r="A2"><v>42</v></c></row>
		</sheetData></worksheet>`,
	})
	odt := makeZip(t, map[string]string{
		"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
			<office:body><office:text>
				<text:p>Hello<text:s c="2"/>world<text:line-break/>next</text:p>
				<text:p>Second</text:p>
			</office:text></office:body>
		</office:document-content>`,
	})
	epub := makeZip(t, map[string]string{
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`,
		"OEBPS/content.opf": `<package><manifest>
			<item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
			<item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
		</manifest><spine><itemref idref="c1"/><itemref idref="c2"/></spine></package>`,
		"OEBPS/chapter1.xhtml": `<html><head><title>Ignored</title></head><body><p>Chapter one</p><p>Text</p></body></html>`,
		"OEBPS/chapter2.xhtml": `<html><body><h1>Chapter two</h1><p>More</p></body></html>`,
	})
	rtf := []byte(`{\rtf1\ansi{\fonttbl{\f0 Arial;}}Hello \b world\b0\par Second line}`)

	cases := []struct {
		name string
		data []byte
		want []string
	}{
		{"docx", docx, []string{"Hello world", "Second paragraph"}},
		{"pptx", pptx, []string{"Two", "Ten"}},
		{"xlsx", xlsx, []string{"Name\tAlice", "42"}},
		{"odt", odt, []string{"Hello  world", "next", "Second"}},
		{"epub", epub, []string{"Chapter one", "Text", "Chapter two", "More"}},
		{"rtf", rtf, []string{"Hello world", "Second line"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, tool, err := ExtractDocument(context.Background(), "sample."+tc.name, tc.data)
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if !strings.HasPrefix(tool, "go:") {
				t.Fatalf("tool = %q, want go:*", tool)
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("text %q does not contain %q", text, want)
				}
			}
		})
	}
}

func minimalPDF() []byte {
	stream := "BT /F1 24 Tf 72 720 Td (Hello PDF) Tj ET\n"
	objs := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(stream), stream),
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var offsets []int
	for _, obj := range objs {
		offsets = append(offsets, b.Len())
		b.WriteString(obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestExtractPDF(t *testing.T) {
	data := minimalPDF()
	text, _, err := ExtractDocument(context.Background(), "sample.pdf", data)
	if err != nil {
		t.Fatalf("extract pdf: %v", err)
	}
	if !strings.Contains(text, "Hello PDF") {
		t.Fatalf("text = %q", text)
	}
	goText, err := extractPDFGo(data)
	if err != nil {
		t.Fatalf("pure-go pdf fallback: %v", err)
	}
	if !strings.Contains(goText, "Hello PDF") {
		t.Fatalf("pure-go text = %q", goText)
	}
}

func TestTranscribeAudio(t *testing.T) {
	var gotModel, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		file, _, err := r.FormFile("file")
		if err == nil {
			data, _ := io.ReadAll(file)
			gotFile = string(data)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello from whisper"}`)
	}))
	defer srv.Close()

	text, tool, err := TranscribeAudio(context.Background(), srv.URL, "whisper", "sample.wav", []byte("RIFFfake"))
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "hello from whisper" {
		t.Fatalf("text = %q", text)
	}
	if tool != "ollama:whisper" {
		t.Fatalf("tool = %q", tool)
	}
	if gotModel != "whisper" || gotFile != "RIFFfake" {
		t.Fatalf("request model=%q file=%q", gotModel, gotFile)
	}
}

func TestImportExtractsDocumentsAndAudio(t *testing.T) {
	src := t.TempDir()
	docx := makeZip(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Imported document</w:t></w:r></w:p></w:body></w:document>`,
	})
	if err := os.WriteFile(filepath.Join(src, "note.docx"), docx, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "voice.wav"), []byte("RIFFfake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "broken.pdf"), []byte("not a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"spoken words"}`)
	}))
	defer srv.Close()

	db := openTestDB(t)
	err := Import(context.Background(), NoopReporter{}, db, src, ImportOptions{
		Source:          SourceFilesystem,
		Formats:         FormatSet{"docx": true, "wav": true, "pdf": true},
		Host:            srv.URL,
		TranscribeModel: "whisper",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	type row struct {
		name, extracted, extractErr, tool string
	}
	rows, err := db.Query(`SELECT file_name, markdown, extract_error, extract_tool FROM notes ORDER BY file_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.extracted, &r.extractErr, &r.tool); err != nil {
			t.Fatal(err)
		}
		got[r.name] = r
	}
	if r := got["note.docx"]; r.extracted != "Imported document" || r.extractErr != "" || r.tool != "go:docx" {
		t.Fatalf("docx row = %#v", r)
	}
	if r := got["voice.wav"]; r.extracted != "spoken words" || r.extractErr != "" || r.tool != "ollama:whisper" {
		t.Fatalf("audio row = %#v", r)
	}
	if r := got["broken.pdf"]; r.extracted != "" || r.extractErr == "" {
		t.Fatalf("pdf row = %#v", r)
	}
}

func TestOpenDBAddsExtractionColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE notes(
		id INTEGER PRIMARY KEY,
		file_path TEXT NOT NULL UNIQUE,
		abs_path TEXT NOT NULL,
		file_name TEXT NOT NULL,
		content TEXT NOT NULL,
		mime_type TEXT NOT NULL DEFAULT '',
		links_md TEXT NOT NULL DEFAULT '',
		out_hash TEXT NOT NULL DEFAULT '',
		imported_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()
	cols := NotesColumns(db)
	for _, want := range []string{"extracted_text", "extracted_path", "blob_path", "extract_error", "extract_tool"} {
		found := false
		for _, c := range cols {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing column %q in %v", want, cols)
		}
	}
}

func TestTranscribeAudioErrorIsStoredByImport(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "voice.wav"), []byte("RIFFfake"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	db := openTestDB(t)
	if err := Import(context.Background(), NoopReporter{}, db, src, ImportOptions{
		Source:          SourceFilesystem,
		Formats:         FormatSet{"wav": true},
		Host:            srv.URL,
		TranscribeModel: "missing",
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	var extracted, extractErr, tool string
	if err := db.QueryRow(`SELECT markdown, extract_error, extract_tool FROM notes WHERE file_name='voice.wav'`).
		Scan(&extracted, &extractErr, &tool); err != nil {
		t.Fatal(err)
	}
	if extracted != "" || !strings.Contains(extractErr, "404") || tool != "ollama:missing" {
		t.Fatalf("extracted=%q error=%q tool=%q", extracted, extractErr, tool)
	}
}
