package core

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ImportSource identifies where the import reads its files from.
type ImportSource int

const (
	SourceFilesystem ImportSource = iota
	SourceZip
	SourceJSONArray
)

var sourceNames = [...]string{"filesystem", "zip archive", "json array"}

func (s ImportSource) String() string {
	if int(s) < len(sourceNames) {
		return sourceNames[s]
	}
	return "unknown"
}

// ParseImportSource maps a user-facing name to an ImportSource.
func ParseImportSource(s string) (ImportSource, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fs", "filesystem", "folder", "dir", "":
		return SourceFilesystem, nil
	case "zip", "archive":
		return SourceZip, nil
	case "json", "jsonarray", "json-array", "array":
		return SourceJSONArray, nil
	default:
		return SourceFilesystem, fmt.Errorf("unknown import source %q (filesystem, zip or json)", s)
	}
}

// FormatInfo describes one entry in the format catalog.
type FormatInfo struct {
	Ext  string
	Mime string
	Kind string // text / image / audio / video / document
}

// formatCatalog lists the formats open models (and the pipeline) can ingest.
var formatCatalog = []FormatInfo{
	// text
	{"md", "text/markdown", "text"},
	{"markdown", "text/markdown", "text"},
	{"txt", "text/plain", "text"},
	{"csv", "text/csv", "text"},
	{"json", "application/json", "text"},
	{"jsonl", "application/json", "text"},
	{"yaml", "application/x-yaml", "text"},
	{"yml", "application/x-yaml", "text"},
	{"xml", "application/xml", "text"},
	{"html", "text/html", "text"},
	{"htm", "text/html", "text"},
	{"log", "text/plain", "text"},
	{"sql", "text/plain", "text"},
	{"go", "text/plain", "text"},
	{"py", "text/plain", "text"},
	{"js", "text/plain", "text"},
	{"ts", "text/plain", "text"},
	{"rs", "text/plain", "text"},
	{"c", "text/plain", "text"},
	{"cpp", "text/plain", "text"},
	{"h", "text/plain", "text"},
	{"java", "text/plain", "text"},
	{"rb", "text/plain", "text"},
	{"sh", "text/plain", "text"},
	{"toml", "text/plain", "text"},
	{"ini", "text/plain", "text"},
	{"conf", "text/plain", "text"},
	{"mdx", "text/markdown", "text"},
	// images
	{"png", "image/png", "image"},
	{"jpg", "image/jpeg", "image"},
	{"jpeg", "image/jpeg", "image"},
	{"webp", "image/webp", "image"},
	{"avif", "image/avif", "image"},
	{"gif", "image/gif", "image"},
	{"bmp", "image/bmp", "image"},
	{"svg", "image/svg+xml", "image"},
	// audio
	{"mp3", "audio/mpeg", "audio"},
	{"wav", "audio/wav", "audio"},
	{"ogg", "audio/ogg", "audio"},
	{"m4a", "audio/mp4", "audio"},
	{"flac", "audio/flac", "audio"},
	// video
	{"mp4", "video/mp4", "video"},
	{"webm", "video/webm", "video"},
	// documents
	{"pdf", "application/pdf", "document"},
	{"docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "document"},
	{"pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "document"},
	{"xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "document"},
	{"odt", "application/vnd.oasis.opendocument.text", "document"},
	{"odp", "application/vnd.oasis.opendocument.presentation", "document"},
	{"ods", "application/vnd.oasis.opendocument.spreadsheet", "document"},
	{"doc", "application/msword", "document"},
	{"ppt", "application/vnd.ms-powerpoint", "document"},
	{"xls", "application/vnd.ms-excel", "document"},
	{"rtf", "application/rtf", "document"},
	{"epub", "application/epub+zip", "document"},
}

// extMime maps an extension (lowercase, with dot) to its canonical MIME type.
var extMime = func() map[string]string {
	m := make(map[string]string, len(formatCatalog)*2)
	for _, f := range formatCatalog {
		m["."+f.Ext] = f.Mime
	}
	return m
}()

// DefaultImportFormats returns the formats used when no explicit choice is
// made. It covers everyday text files, all supported office/document formats
// and audio files (which are transcribed locally through Ollama).
func DefaultImportFormats() []string {
	return []string{
		"md", "txt", "csv", "json", "yaml", "xml", "html", "log", "mdx",
		"pdf", "docx", "pptx", "xlsx", "odt", "odp", "ods",
		"doc", "ppt", "xls", "rtf", "epub",
		"mp3", "wav", "ogg", "m4a", "flac",
	}
}

// KnownFormats returns a copy of the format catalog, ordered as defined.
func KnownFormats() []FormatInfo {
	out := make([]FormatInfo, len(formatCatalog))
	copy(out, formatCatalog)
	return out
}

// FormatSet is the user-selected set of extensions to import (lowercase, no dot).
type FormatSet map[string]bool

// Contains reports whether the extension (with or without dot) is selected.
// A nil cast means "no filter — accept every format"; an empty non-nil set
// accepts nothing.
func (fs FormatSet) Contains(ext string) bool {
	if fs == nil {
		return true
	}
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	return fs[ext]
}

// mimeFor returns the MIME type for a file name. Falls back to text/plain
// when the extension is unknown but the bytes look textual, otherwise
// application/octet-stream.
func mimeFor(name string, head []byte) string {
	if m, ok := extMime[strings.ToLower(filepath.Ext(name))]; ok {
		return m
	}
	if len(head) > 0 && looksText(head) {
		return "text/plain"
	}
	return "application/octet-stream"
}

func looksText(head []byte) bool {
	n := len(head)
	if n > 512 {
		n = 512
	}
	if n == 0 {
		return false
	}
	for _, b := range head[:n] {
		if b == 0 {
			return false
		}
	}
	return true
}

// IsText reports whether a MIME type is human-readable text that can be sent
// to a model directly. Images, audio, video, PDFs and Office documents are
// binary and stored base64-encoded.
func IsText(mimeType string) bool {
	if mimeType == "" || strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "application/json", "application/xml", "application/x-yaml",
		"application/ld+json", "application/x-ndjson", "application/jsonl",
		"application/javascript", "application/x-sh", "application/x-shellscript",
		"application/x-httpd-php", "application/x-csv":
		return true
	}
	return false
}

// importFile is one file discovered by an import source.
type importFile struct {
	Path string // path relative to the import root
	Abs  string // absolute path on disk (filesystem source only)
	Data []byte // content already read (nil → read from disk at import time)
}

// ImportOptions configures one import run.
type ImportOptions struct {
	Source          ImportSource
	Formats         FormatSet
	Host            string // Ollama host (legacy audio backend)
	TranscribeModel string // Ollama transcription model (legacy)
	AudioEngine     string
	WhisperModel    string
	BinaryStorage   string // links (default) or db
	GoogleAPIKey    string
	SpeechLanguage  string
}

func (o ImportOptions) audioOptions() AudioOptions {
	engine := o.AudioEngine
	if strings.TrimSpace(engine) == "" {
		// Preserve the historical behaviour for callers that only set the
		// Ollama host/model fields.
		engine = AudioEngineOllama
	}
	return AudioOptions{
		Engine:         engine,
		WhisperModel:   o.WhisperModel,
		GoogleAPIKey:   o.GoogleAPIKey,
		SpeechLanguage: o.SpeechLanguage,
		OllamaHost:     o.Host,
		OllamaModel:    o.TranscribeModel,
	}
}

// Import reads files from src into the notes table according to opt. The
// meaning of src depends on Source: a folder, a zip archive or a JSON file.
func Import(ctx context.Context, r Reporter, db *sql.DB, src string, opt ImportOptions) error {
	if src == "" {
		return errors.New("specify source: -src FOLDER|ARCHIVE|JSON")
	}
	files, err := collectFiles(src, opt.Source)
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	label := "import " + opt.Source.String()
	if len(opt.Formats) > 0 {
		var exts []string
		for e := range opt.Formats {
			exts = append(exts, "."+e)
		}
		sort.Strings(exts)
		label += " [" + strings.Join(exts, ", ") + "]"
	}
	r.SetPhase(label, 0, len(files))
	r.Logf("found files: %d", len(files))
	binaryMode := NormalizeBinaryStorage(opt.BinaryStorage)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	inTx, added, skipped := 0, 0, 0
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		rel := filepath.ToSlash(f.Path)
		r.SetCurrent(rel)

		var one int
		if err := tx.QueryRow(`SELECT 1 FROM notes WHERE file_path=?`, rel).Scan(&one); err == nil {
			r.Advance(true)
			skipped++
			continue
		}

		var data []byte
		if f.Data != nil {
			data = f.Data
		} else {
			if data, err = os.ReadFile(f.Abs); err != nil {
				r.Fail("%s: %v", rel, err)
				r.Advance(true)
				continue
			}
		}

		if !opt.Formats.Contains(filepath.Ext(rel)) {
			r.Advance(true)
			skipped++
			continue
		}

		name := filepath.Base(rel)
		mimeType := mimeFor(name, data)
		content, blobPath := "", ""
		switch {
		case IsText(mimeType):
			content = string(data)
		case binaryMode == BinaryStorageDB:
			content = base64.StdEncoding.EncodeToString(data)
		case f.Abs == "":
			// Archive/JSON sources have no original file to link to. Keep the
			// binary in the sidecar directory instead of sqlite.
			blobPath, err = writeBlobFile(db, rel, name, data)
			if err != nil {
				r.Fail("%s: store binary: %v", rel, err)
				r.Advance(true)
				continue
			}
		}

		var extracted, extractedPath, extractErr, extractTool string
		if IsExtractable(name, mimeType) {
			text, tool, err := ExtractFileTextWithAudio(ctx, name, mimeType, data, opt.audioOptions())
			extractTool = tool
			if err != nil {
				extractErr = err.Error()
				r.Logf("%s: extraction failed: %v", rel, err)
			} else if strings.TrimSpace(text) != "" {
				link, storeErr := writeExtractedFile(db, rel, name, text)
				if storeErr != nil {
					// Never lose the extraction when the sidecar file cannot
					// be written; keep it inline and surface the problem.
					extracted = text
					extractErr = "store extracted text: " + storeErr.Error()
					r.Logf("%s: %s", rel, extractErr)
				} else {
					extractedPath = link
				}
			}
		}

		_, err = tx.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type,
			extracted_text, extracted_path, blob_path, extract_error, extract_tool, imported_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			rel, f.Abs, name, content, mimeType, extracted, extractedPath, blobPath,
			extractErr, extractTool, time.Now().Format(time.RFC3339))
		if err != nil {
			r.Fail("%s: %v", rel, err)
			r.Advance(true)
			continue
		}
		added++
		r.Advance(false)
		if inTx++; inTx >= 200 {
			if err := tx.Commit(); err != nil {
				return err
			}
			if tx, err = db.Begin(); err != nil {
				return err
			}
			inTx = 0
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.Logf("import: added new — %d, skipped — %d", added, skipped)
	return nil
}

func collectFiles(src string, source ImportSource) ([]importFile, error) {
	switch source {
	case SourceZip:
		return collectZip(src)
	case SourceJSONArray:
		return collectJSONArray(src)
	default:
		return collectFilesystem(src), nil
	}
}

func collectFilesystem(src string) []importFile {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil
	}
	var files []importFile
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != abs && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		files = append(files, importFile{Path: filepath.ToSlash(rel), Abs: p})
		return nil
	})
	return files
}

func collectZip(src string) ([]importFile, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var files []importFile
	for _, zf := range r.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		files = append(files, importFile{Path: filepath.ToSlash(zf.Name), Data: data})
	}
	return files, nil
}

// JSONItem is one entry in a JSON-array import source.
type JSONItem struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Data    string `json:"data"` // base64 bytes for binary files
	Name    string `json:"name"` // fallback when path is empty
}

func collectJSONArray(src string) ([]importFile, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	var files []importFile

	var items []JSONItem
	if err := json.Unmarshal(raw, &items); err == nil && len(items) > 0 {
		for i, it := range items {
			f := importFile{Path: it.Path}
			if f.Path == "" {
				f.Path = it.Name
			}
			if f.Path == "" {
				f.Path = fmt.Sprintf("item-%03d", i+1)
			}
			switch {
			case it.Data != "":
				if b, err := base64.StdEncoding.DecodeString(it.Data); err == nil {
					f.Data = b
				}
			case it.Content != "":
				f.Data = []byte(it.Content)
			}
			files = append(files, f)
		}
		return files, nil
	}

	// fall back to an array of file paths on disk
	var paths []string
	if err := json.Unmarshal(raw, &paths); err != nil {
		return nil, fmt.Errorf("json array source: %w", err)
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		files = append(files, importFile{Path: filepath.Base(p), Abs: abs, Data: data})
	}
	return files, nil
}
