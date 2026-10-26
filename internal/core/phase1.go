package core

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Phase1 imports markdown files from src into sqlite.
func Phase1(ctx context.Context, r Reporter, db *sql.DB, src string) error {
	if src == "" {
		return errors.New("specify source folder: -src FOLDER")
	}
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	var files []string
	err = filepath.WalkDir(absSrc, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != absSrc && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".md") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	r.SetPhase("1/3 import md → sqlite", 0, len(files))
	r.Logf("found md files: %d", len(files))

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	inTx := 0
	added := 0
	for _, p := range files {
		if ctx.Err() != nil {
			break
		}
		rel, _ := filepath.Rel(absSrc, p)
		rel = filepath.ToSlash(rel)
		r.SetCurrent(rel)

		var one int
		if err := tx.QueryRow(`SELECT 1 FROM notes WHERE file_path=?`, rel).Scan(&one); err == nil {
			r.Advance(true)
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			r.Fail("%s: %v", rel, err)
			r.Advance(true)
			continue
		}
		_, err = tx.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, imported_at) VALUES(?,?,?,?,?)`,
			rel, p, filepath.Base(p), string(data), time.Now().Format(time.RFC3339))
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
	r.Logf("phase 1: added new — %d", added)
	return nil
}
