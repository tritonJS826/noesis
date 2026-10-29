package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
)

// isRasterImage reports whether a MIME type is a raster image that needs a
// vision model. SVG (image/svg+xml) is XML text and is handled without one.
func isRasterImage(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml"
}

// CanExtract reports whether a file can be turned into markdown: documents and
// audio through the local extractors/transcriber, raster and SVG images through
// the vision model or their raw XML text.
func CanExtract(name, mimeType string) bool {
	return IsExtractable(name, mimeType) || strings.HasPrefix(mimeType, "image/")
}

// ExtractNoteText turns one stored file into markdown text. Documents and audio
// use the local extractors/transcriber; raster images are described by the
// configured vision model; SVG images are kept as their XML text.
//
// noteID is the note the file belongs to; 0 is allowed for a file that is not
// in the database yet (no db.* variables are then exposed to the prompt).
func ExtractNoteText(ctx context.Context, c Config, db *sql.DB, name, mimeType string, data []byte, noteID int64) (text, tool string, err error) {
	switch {
	case isRasterImage(mimeType):
		prompt := LoadPrompt(db, DataExtractionProcess)
		if strings.TrimSpace(prompt) == "" {
			prompt = dataExtractionFallback
		}
		vars := map[string]string{"FILE_NAME": name}
		if noteID != 0 {
			vars = mergeVars(vars, noteDBVars(db, "db", noteID))
		}
		// Ollama cannot identify every image format (e.g. AVIF), so non-PNG/JPEG
		// images are transcoded to PNG first — see image.go for the rationale.
		imgData, converted, err := visionImage(ctx, name, mimeType, data)
		if err != nil {
			return "", "ollama:vision", err
		}
		tool = "ollama:vision"
		if converted {
			tool += "+png"
		}
		text, err = askOllamaTextModel(ctx, c, c.visionModel(), expandPrompt(prompt, vars),
			[]string{base64.StdEncoding.EncodeToString(imgData)})
		return text, tool, err
	case strings.HasPrefix(mimeType, "image/"):
		// SVG and other XML-based images need no vision model.
		return string(data), "raw", nil
	default:
		return ExtractFileTextWithAudio(ctx, name, mimeType, data, c.audioOptions())
	}
}

// ExtractFilter narrows a re-extraction run. A zero value matches every
// extractable note.
type ExtractFilter struct {
	// Name matches a case-insensitive substring of the note's file path or
	// file name. Empty matches everything.
	Name string
	// Formats restricts the run to these extensions (lowercase, no dot). A nil
	// set accepts every extractable format.
	Formats FormatSet
}

// Match reports whether a note with the given path, name and MIME type should
// be re-extracted.
func (f ExtractFilter) Match(path, name, mimeType string) bool {
	if q := strings.ToLower(strings.TrimSpace(f.Name)); q != "" {
		if !strings.Contains(strings.ToLower(path), q) &&
			!strings.Contains(strings.ToLower(name), q) {
			return false
		}
	}
	if f.Formats != nil {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
		if !f.Formats[ext] {
			return false
		}
	}
	return true
}

// ExtractableFormats returns the catalog entries that can be turned into text
// (documents, images and audio), in catalog order.
func ExtractableFormats() []FormatInfo {
	var out []FormatInfo
	for _, f := range formatCatalog {
		switch f.Kind {
		case "document", "image", "audio":
			out = append(out, f)
		}
	}
	return out
}

// ExtractFormatSet builds a FormatSet over the given extensions. An empty list
// (or a list with no usable extensions) yields a nil set, which means "all
// extractable formats".
func ExtractFormatSet(exts []string) FormatSet {
	fs := make(FormatSet, len(exts))
	for _, e := range exts {
		e = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
		if e != "" {
			fs[e] = true
		}
	}
	if len(fs) == 0 {
		return nil
	}
	return fs
}
