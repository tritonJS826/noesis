package core

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SyncItem represents one file-level difference between the database and the
// current folder contents.
type SyncItem struct {
	Path    string // relative path from the source folder
	Abs     string // absolute path
	Deleted bool   // present in the DB but missing on disk
	NewFile bool   // present on disk but missing from the DB
	Changed bool   // present in both but the file content changed on disk
	Name    string // file name only
	ID      int64  // note id for deleted/changed items (0 otherwise)
}

// sourceHash is the sha256 fingerprint of a source file's bytes. It is stored
// per note (notes.src_hash) so SyncDiff can detect content changes.
func sourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashFile reads a file and returns its sourceHash, or an error when the file
// cannot be read.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sourceHash(data), nil
}

// SyncDiff runs a comparison between the notes imported from a given source
// folder and the files currently on disk. Only files whose extension matches
// the supplied format set are considered for the new-files side; the deleted
// side covers every abs_path stored in the DB.
func SyncDiff(db *sql.DB, src string, formats FormatSet, skipDirs []string) ([]SyncItem, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("source folder not set")
	}
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return nil, fmt.Errorf("resolve source folder: %w", err)
	}

	// Read every abs_path from the database. Notes that came from a zip or
	// JSON source may have an empty abs_path — those are skipped.
	rows, err := db.Query(`SELECT id, file_path, abs_path, src_hash, src_mtime FROM notes ORDER BY file_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dbRow struct {
		id       int64
		rel, abs string
		hash     string
		mtime    int64
	}
	var dbRows []dbRow
	for rows.Next() {
		var r dbRow
		if err := rows.Scan(&r.id, &r.rel, &r.abs, &r.hash, &r.mtime); err != nil {
			return nil, err
		}
		if strings.TrimSpace(r.abs) != "" {
			dbRows = append(dbRows, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close the read cursor before the change check writes baseline hashes.
	rows.Close()

	// Build a set of known relative paths (both from DB and from disk).
	dbByAbs := map[string]string{} // abs -> rel
	dbByRel := map[string]bool{}   // rel -> true
	for _, r := range dbRows {
		dbByAbs[r.abs] = r.rel
		dbByRel[filepath.ToSlash(r.rel)] = true
	}

	// Walk the source folder and collect files that match the format set.
	diskByRel := map[string]string{} // rel -> abs
	_ = filepath.WalkDir(absSrc, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != absSrc && ShouldSkipDir(d.Name(), skipDirs) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(absSrc, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !formats.Contains(filepath.Ext(rel)) {
			return nil
		}
		diskByRel[rel] = p
		return nil
	})

	// Build the combined result.
	var items []SyncItem

	// 1. Notes that are in the DB but the abs_path no longer exists, and notes
	//    whose file content changed on disk. Change detection uses the stored
	//    src_hash; the mtime is only a fast path that avoids re-hashing files
	//    untouched since the last sync.
	for _, r := range dbRows {
		fi, statErr := os.Stat(r.abs)
		if statErr != nil {
			items = append(items, SyncItem{
				Path:    filepath.ToSlash(r.rel),
				Abs:     r.abs,
				Deleted: true,
				Name:    filepath.Base(r.abs),
				ID:      r.id,
			})
			continue
		}
		if r.hash == "" {
			// First time we see this note: record a baseline without flagging
			// it as changed (files imported before hashing existed).
			if h, herr := hashFile(r.abs); herr == nil {
				_, _ = db.Exec(`UPDATE notes SET src_hash=?, src_mtime=? WHERE id=?`,
					h, fi.ModTime().UnixNano(), r.id)
			}
			continue
		}
		if fi.ModTime().UnixNano() == r.mtime {
			continue
		}
		h, herr := hashFile(r.abs)
		if herr != nil {
			continue
		}
		if h != r.hash {
			items = append(items, SyncItem{
				Path:    filepath.ToSlash(r.rel),
				Abs:     r.abs,
				Changed: true,
				Name:    filepath.Base(r.abs),
				ID:      r.id,
			})
			continue
		}
		// Only the mtime moved; refresh it so the next diff takes the fast path.
		_, _ = db.Exec(`UPDATE notes SET src_mtime=? WHERE id=?`, fi.ModTime().UnixNano(), r.id)
	}

	// 2. Files on disk whose path was never imported.
	for rel, abs := range diskByRel {
		if dbByRel[rel] {
			continue
		}
		if _, exists := dbByAbs[abs]; exists {
			continue
		}
		items = append(items, SyncItem{
			Path:    rel,
			Abs:     abs,
			NewFile: true,
			Name:    filepath.Base(abs),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Deleted != items[j].Deleted {
			return items[i].Deleted // deleted first
		}
		if items[i].Changed != items[j].Changed {
			return items[i].Changed // changed next
		}
		return items[i].Path < items[j].Path
	})

	return items, nil
}

// SyncApply removes deleted-file notes from the database and imports the
// selected new files. The caller is responsible for providing only the IDs that
// the user explicitly chose.
func SyncApply(db *sql.DB, src string, removeIDs []int64, newPaths []string, opts ImportOptions) (removed int, imported int, err error) {
	absSrc, absErr := filepath.Abs(src)
	if absErr != nil {
		return 0, 0, fmt.Errorf("resolve source folder: %w", absErr)
	}

	// Remove selected notes and scrub their links. The returned removed count
	// is the number of notes, not the number of other notes whose links were
	// scrubbed (that is shown separately by RemoveNoteAndLinksByID's callers).
	for _, id := range removeIDs {
		n, _, rerr := RemoveNoteAndLinksByID(db, id)
		if rerr != nil {
			if err == nil {
				err = rerr
			}
			continue
		}
		removed += n
	}

	if len(newPaths) > 0 {
		// Build a minimal set of import files from the given paths.
		var files []importFile
		for _, p := range newPaths {
			abs, aerr := filepath.Abs(p)
			if aerr != nil {
				if err == nil {
					err = aerr
				}
				continue
			}
			rel, rerr := filepath.Rel(absSrc, abs)
			if rerr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				rel = filepath.Base(abs)
			}
			files = append(files, importFile{
				Path: filepath.ToSlash(rel),
				Abs:  abs,
			})
		}

		// Import these specific files. The Import function will skip those it
		// already knows, so we bypass the collectFiles step and insert directly.
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

		for _, f := range files {
			rel := filepath.ToSlash(f.Path)
			if !opts.Formats.Contains(filepath.Ext(rel)) {
				continue
			}
			var one int
			if db.QueryRow(`SELECT 1 FROM notes WHERE file_path=?`, rel).Scan(&one) == nil {
				continue
			}
			data, rerr := os.ReadFile(f.Abs)
			if rerr != nil {
				if err == nil {
					err = fmt.Errorf("%s: %w", rel, rerr)
				}
				continue
			}

			name := filepath.Base(rel)
			mimeType := mimeFor(name, data)

			binaryMode := NormalizeBinaryStorage(opts.BinaryStorage)
			content, blobPath := "", ""
			switch {
			case IsText(mimeType):
				content = string(data)
			case binaryMode == BinaryStorageDB:
				content = base64.StdEncoding.EncodeToString(data)
			case f.Abs == "":
				stored, berr := writeBlobFile(db, rel, name, data)
				if berr != nil {
					if err == nil {
						err = fmt.Errorf("%s: store binary: %w", rel, berr)
					}
					continue
				}
				blobPath = stored
			}

			// Importing only stores files: documents, images and audio are
			// turned into markdown later by the "Data extraction" process.
			// markdown is the original content for text files.
			markdown := ""
			if IsText(mimeType) {
				markdown = content
			}

			now := time.Now().Format(time.RFC3339)
			srcHash, srcMtime := sourceFileMeta(f.Abs, data)
			if _, exErr := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type,
				markdown, extracted_text, extracted_path, blob_path, extract_error, extract_tool, src_hash, src_mtime, imported_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				rel, f.Abs, name, content, mimeType,
				markdown, "", "", blobPath,
				"", "", srcHash, srcMtime, now,
			); exErr != nil {
				if err == nil {
					err = fmt.Errorf("%s: %w", rel, exErr)
				}
			} else {
				imported++
			}
		}
	}

	return removed, imported, err
}

// SyncNoteIDByPath returns the note id for a given file_path, or 0 if not found.
func SyncNoteIDByPath(db *sql.DB, filePath string) int64 {
	var id int64
	if db.QueryRow(`SELECT id FROM notes WHERE file_path=?`, filepath.ToSlash(filePath)).Scan(&id) == nil {
		return id
	}
	return 0
}

// sourceFileMeta returns the content hash and mtime of a source file. The abs
// path may be empty (archive/JSON source), in which case only the hash of the
// in-memory bytes is returned.
func sourceFileMeta(abs string, data []byte) (hash string, mtime int64) {
	hash = sourceHash(data)
	if abs != "" {
		if fi, err := os.Stat(abs); err == nil {
			mtime = fi.ModTime().UnixNano()
		}
	}
	return
}

// RefreshNoteFromDisk re-reads a note's source file and updates its stored
// content so later processes run against the current bytes. Extracted text and
// the markdown of binary files are reset; callers usually pair this with
// ClearDeprecatedMetadata. Notes from an archive/JSON source (empty abs_path)
// are left untouched.
func RefreshNoteFromDisk(db *sql.DB, id int64, binaryStorage string) error {
	var abs, name, mime string
	if err := db.QueryRow(`SELECT abs_path, file_name, mime_type FROM notes WHERE id=?`, id).
		Scan(&abs, &name, &mime); err != nil {
		return err
	}
	if strings.TrimSpace(abs) == "" {
		return nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	mime = mimeFor(name, data)
	content, markdown := "", ""
	switch {
	case IsText(mime):
		content = string(data)
		markdown = content
	case NormalizeBinaryStorage(binaryStorage) == BinaryStorageDB:
		content = base64.StdEncoding.EncodeToString(data)
	}
	hash, mtime := sourceFileMeta(abs, data)
	_, err = db.Exec(`UPDATE notes SET content=?, markdown=?, mime_type=?,
		extracted_text='', extracted_path='', extract_error='', extract_tool='',
		src_hash=?, src_mtime=? WHERE id=?`,
		content, markdown, mime, hash, mtime, id)
	return err
}

// ClearDeprecatedMetadata deletes the derived data of one note: RAG chunks, RDF
// facts, tags and every stored process output (summaries etc.). It also drops
// the RDF-derived links that touch the note and rebuilds links_md for the
// affected neighbours, so the note can be regenerated from scratch.
func ClearDeprecatedMetadata(db *sql.DB, noteID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, q := range []string{
		`DELETE FROM rag_chunks WHERE note_id=?`,
		`DELETE FROM rag_notes WHERE note_id=?`,
		`DELETE FROM tags WHERE note_id=?`,
		`DELETE FROM process_output WHERE note_id=?`,
		`DELETE FROM note_facts WHERE note_id=?`,
	} {
		if _, err := tx.Exec(q, noteID); err != nil {
			return err
		}
	}

	// Collect the notes whose RDF-derived links mention this one before the
	// links are dropped, so their links_md can be rebuilt afterwards.
	affected := map[int64]bool{noteID: true}
	rows, err := tx.Query(`SELECT src_id, dst_id FROM links WHERE reason LIKE ? AND (src_id=? OR dst_id=?)`,
		rdfLinkReasonPrefix+"%", noteID, noteID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var src, dst int64
		if err := rows.Scan(&src, &dst); err != nil {
			rows.Close()
			return err
		}
		affected[src] = true
		affected[dst] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM links WHERE reason LIKE ? AND (src_id=? OR dst_id=?)`,
		rdfLinkReasonPrefix+"%", noteID, noteID); err != nil {
		return err
	}

	if err := pruneRDF(tx); err != nil {
		return err
	}
	for id := range affected {
		if err := rebuildLinksMD(tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SyncApplyChanged refreshes changed notes from disk and clears their derived
// metadata so the processes regenerate it. It returns the number of notes
// refreshed.
func SyncApplyChanged(db *sql.DB, ids []int64, binaryStorage string) (int, error) {
	done := 0
	for _, id := range ids {
		if err := RefreshNoteFromDisk(db, id, binaryStorage); err != nil {
			return done, err
		}
		if err := ClearDeprecatedMetadata(db, id); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}
