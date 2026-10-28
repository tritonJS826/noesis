package core

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Binary storage modes. The default is "links": raw binaries stay next to the
// database (or at their original path) and only a link is stored in sqlite.
// "db" keeps the historical behaviour and embeds binaries base64-encoded in the
// notes.content column.
const (
	BinaryStorageLinks = "links"
	BinaryStorageDB    = "db"
)

// NormalizeBinaryStorage maps user-facing values to a supported mode. Unknown
// values fall back to links, which is the safe default for database size.
func NormalizeBinaryStorage(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case BinaryStorageDB, "database", "sqlite", "embed", "embedded", "base64":
		return BinaryStorageDB
	default:
		return BinaryStorageLinks
	}
}

// BinaryStorageLabel returns a human-readable label for a storage mode.
func BinaryStorageLabel(s string) string {
	if NormalizeBinaryStorage(s) == BinaryStorageDB {
		return "database (base64)"
	}
	return "links to source files"
}

// BinaryStorageIDs returns the selectable storage modes in display order.
func BinaryStorageIDs() []string {
	return []string{BinaryStorageLinks, BinaryStorageDB}
}

// BinaryStorageLabels returns labels matching BinaryStorageIDs, for select
// widgets and option pickers.
func BinaryStorageLabels() []string {
	ids := BinaryStorageIDs()
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = BinaryStorageLabel(id)
	}
	return out
}

var dataDirCache sync.Map // map[*sql.DB]string

// dataDirForDBPath returns the sidecar directory that stores extracted text and
// materialised binaries for a database file. Keeping it next to the database
// makes moving a database + data folder around straightforward.
func dataDirForDBPath(dbPath string) string {
	if dbPath == "" || dbPath == ":memory:" || strings.Contains(dbPath, "mode=memory") {
		return ""
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = dbPath
	}
	ext := filepath.Ext(abs)
	return strings.TrimSuffix(abs, ext) + ".data"
}

// dataDir returns (and caches) the sidecar directory for a database handle.
func dataDir(db *sql.DB) string {
	if db == nil {
		return ""
	}
	if v, ok := dataDirCache.Load(db); ok {
		return v.(string)
	}

	var file string
	rows, err := db.Query(`PRAGMA database_list`)
	if err == nil {
		for rows.Next() {
			var seq int
			var name, p string
			if rows.Scan(&seq, &name, &p) == nil && name == "main" {
				file = p
			}
		}
		rows.Close()
	}
	root := dataDirForDBPath(file)
	if root == "" {
		root = filepath.Join(os.TempDir(), "noesis-data")
	}
	dataDirCache.Store(db, root)
	return root
}

// storedPath resolves a database-relative link to an absolute path. Values that
// are already absolute are returned unchanged. Empty links resolve to "".
func storedPath(db *sql.DB, rel string) string {
	if strings.TrimSpace(rel) == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return rel
	}
	root := dataDir(db)
	if root == "" {
		return ""
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	inside, err := filepath.Rel(root, p)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(os.PathSeparator)) {
		return ""
	}
	return p
}

// safeFileName keeps a readable file name while removing path separators and
// characters that are invalid on common filesystems.
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "" || name == "." || name == ".." {
		name = "note"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "note"
	}
	runes := []rune(out)
	if len(runes) > 120 {
		ext := filepath.Ext(out)
		stem := strings.TrimSuffix(out, ext)
		rs := []rune(stem)
		if len(rs) > 120-len([]rune(ext)) {
			rs = rs[:120-len([]rune(ext))]
		}
		out = string(rs) + ext
	}
	return out
}

// storageKey is a short, stable prefix derived from the source path. It keeps
// files with the same base name from colliding and avoids unsafe nested paths.
func storageKey(rel string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(rel)))
	return hex.EncodeToString(sum[:8])
}

func writeStored(db *sql.DB, dir, rel, name string, data []byte) (string, error) {
	root := dataDir(db)
	if root == "" {
		return "", fmt.Errorf("data directory is not available")
	}
	outRel := path.Join(dir, storageKey(rel)+"-"+safeFileName(name))
	dst := storedPath(db, outRel)
	if dst == "" {
		return "", fmt.Errorf("refusing to write outside the data directory: %s", outRel)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", err
	}
	return outRel, nil
}

// writeExtractedFile stores extracted document/audio/image text as a Markdown
// file and returns its database-relative link.
func writeExtractedFile(db *sql.DB, rel, name, text string) (string, error) {
	return writeStored(db, "extracted", rel, safeFileName(name)+".md", []byte(text))
}

// writeBlobFile materialises a binary imported from an archive or JSON source
// so that later re-extraction does not need the original container.
func writeBlobFile(db *sql.DB, rel, name string, data []byte) (string, error) {
	return writeStored(db, "blobs", rel, safeFileName(name), data)
}

// readStoredFile reads a database-relative link.
func readStoredFile(db *sql.DB, rel string) ([]byte, error) {
	p := storedPath(db, rel)
	if p == "" {
		return nil, fmt.Errorf("invalid stored path %q", rel)
	}
	return os.ReadFile(p)
}

// removeStoredFile deletes a file referenced by a database link. Absolute
// original paths are never removed: only files inside the sidecar directory.
func removeStoredFile(db *sql.DB, rel string) {
	if strings.TrimSpace(rel) == "" || filepath.IsAbs(rel) {
		return
	}
	if p := storedPath(db, rel); p != "" {
		_ = os.Remove(p)
	}
}

// resolveExtractedText returns the extracted text stored inline (legacy
// databases) or in the linked Markdown file.
func resolveExtractedText(db *sql.DB, inline, link string) string {
	if strings.TrimSpace(link) != "" {
		if data, err := readStoredFile(db, link); err == nil {
			return string(data)
		}
	}
	return inline
}

// migrateNoteStorage is a best-effort, idempotent upgrade for databases created
// before extracted text and binaries were moved out of sqlite. It never fails
// OpenDB: notes that cannot be migrated keep their inline values.
func migrateNoteStorage(db *sql.DB) {
	migrateExtractedToFiles(db)
	migrateBinaryLinks(db)
}

func migrateExtractedToFiles(db *sql.DB) {
	type rec struct {
		id                 int64
		rel, name, content string
	}
	rows, err := db.Query(`SELECT id, file_path, file_name, extracted_text FROM notes WHERE extracted_text<>''`)
	if err != nil {
		return
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if rows.Scan(&r.id, &r.rel, &r.name, &r.content) == nil {
			recs = append(recs, r)
		}
	}
	rows.Close()

	for _, r := range recs {
		link, err := writeExtractedFile(db, r.rel, r.name, r.content)
		if err != nil {
			continue
		}
		if _, err := db.Exec(`UPDATE notes SET extracted_text='', extracted_path=? WHERE id=?`, link, r.id); err != nil {
			continue
		}
		// The "Data extraction" process used to duplicate the same text; the
		// linked file is now the single source of truth.
		_, _ = db.Exec(`DELETE FROM process_output WHERE note_id=? AND process_name=?`, r.id, DataExtractionProcess)
	}
}

func migrateBinaryLinks(db *sql.DB) {
	var mode string
	_ = db.QueryRow(`SELECT value FROM settings WHERE name='binary_storage'`).Scan(&mode)
	if NormalizeBinaryStorage(mode) != BinaryStorageLinks {
		return
	}

	type rec struct {
		id               int64
		rel, abs, name   string
		content, mime    string
	}
	rows, err := db.Query(`SELECT id, file_path, abs_path, file_name, content, mime_type FROM notes WHERE content<>''`)
	if err != nil {
		return
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if rows.Scan(&r.id, &r.rel, &r.abs, &r.name, &r.content, &r.mime) == nil {
			recs = append(recs, r)
		}
	}
	rows.Close()

	for _, r := range recs {
		mime := noteMimeType(r.name, r.mime)
		if IsText(mime) {
			continue
		}
		if r.abs != "" {
			if _, err := os.Stat(r.abs); err == nil {
				_, _ = db.Exec(`UPDATE notes SET content='' WHERE id=?`, r.id)
				continue
			}
		}
		data, err := base64.StdEncoding.DecodeString(r.content)
		if err != nil {
			continue
		}
		link, err := writeBlobFile(db, r.rel, r.name, data)
		if err != nil {
			continue
		}
		_, _ = db.Exec(`UPDATE notes SET content='', blob_path=? WHERE id=?`, link, r.id)
	}
}
