package core

import (
	"database/sql"
	"encoding/base64"
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
	Name    string // file name only
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
	rows, err := db.Query(`SELECT file_path, abs_path FROM notes ORDER BY file_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dbRow struct {
		rel, abs string
	}
	var dbRows []dbRow
	for rows.Next() {
		var r dbRow
		if err := rows.Scan(&r.rel, &r.abs); err != nil {
			return nil, err
		}
		if strings.TrimSpace(r.abs) != "" {
			dbRows = append(dbRows, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

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

	// 1. Notes that are in the DB but the abs_path no longer exists.
	for _, r := range dbRows {
		if _, err := os.Stat(r.abs); err == nil {
			continue
		}
		items = append(items, SyncItem{
			Path:    filepath.ToSlash(r.rel),
			Abs:     r.abs,
			Deleted: true,
			Name:    filepath.Base(r.abs),
		})
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
			if _, exErr := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type,
				markdown, extracted_text, extracted_path, blob_path, extract_error, extract_tool, imported_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				rel, f.Abs, name, content, mimeType,
				markdown, "", "", blobPath,
				"", "", now,
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
