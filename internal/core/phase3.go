package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reTags = regexp.MustCompile(`^tags:\s*(.*)$`)
	reItem = regexp.MustCompile(`^\s*-\s+(.+)$`)
)

func splitFrontMatter(s string) (fm []string, body string, ok bool) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(s, "---\n") {
		return nil, s, false
	}
	rest := s[4:]
	idx := -1
	switch {
	case strings.HasPrefix(rest, "---"):
		idx = 0
	default:
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			idx = i + 1
		} else if strings.HasSuffix(rest, "\n---") {
			idx = len(rest) - 3
		}
	}
	if idx < 0 {
		return nil, s, false
	}
	text := strings.TrimSuffix(rest[:idx], "\n")
	if text != "" {
		fm = strings.Split(text, "\n")
	}
	return fm, strings.TrimPrefix(rest[idx+3:], "\n"), true
}

// extractTags extracts the existing tags: key from front matter and returns the remaining lines.
func extractTags(fm []string) (rest []string, tags []string) {
	inTags := false
	add := func(s string) {
		if t := cleanTag(strings.Trim(strings.TrimSpace(s), `"'`)); t != "" {
			tags = append(tags, t)
		}
	}
	for _, l := range fm {
		if m := reTags.FindStringSubmatch(l); m != nil {
			inTags = true
			v := strings.Trim(strings.TrimSpace(m[1]), "[]")
			for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
				add(p)
			}
			continue
		}
		if inTags {
			if m := reItem.FindStringSubmatch(l); m != nil {
				add(m[1])
				continue
			}
			inTags = false
		}
		rest = append(rest, l)
	}
	return
}

func compose(content string, tags []string, linksMD string) string {
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

// Phase3 writes the vault: front matter with tags and [[wikilinks]].
func Phase3(ctx context.Context, r Reporter, db *sql.DB, out, src string) error {
	if out == "" {
		return errors.New("specify output folder: -out FOLDER")
	}
	absOut, _ := filepath.Abs(out)
	if src != "" {
		if absSrc, _ := filepath.Abs(src); absSrc == absOut {
			return errors.New("-out must not match source folder")
		}
	}
	var total int
	_ = db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&total)
	r.SetPhase("3/3 write vault → "+out, 0, total)

	type rec struct {
		id            int64
		path, content string
		links, hash   string
	}
	rows, err := db.Query(`SELECT id, file_path, content, links_md, out_hash FROM notes ORDER BY id`)
	if err != nil {
		return err
	}
	var recs []rec
	for rows.Next() {
		var rec rec
		if err := rows.Scan(&rec.id, &rec.path, &rec.content, &rec.links, &rec.hash); err != nil {
			rows.Close()
			return err
		}
		recs = append(recs, rec)
	}
	rows.Close()

	written := 0
	for _, rec := range recs {
		if ctx.Err() != nil {
			return nil
		}
		r.SetCurrent(rec.path)
		trows, err := db.Query(`SELECT tag FROM tags WHERE note_id=? ORDER BY tag`, rec.id)
		if err != nil {
			return err
		}
		var tags []string
		for trows.Next() {
			var t string
			_ = trows.Scan(&t)
			tags = append(tags, t)
		}
		trows.Close()

		text := compose(rec.content, tags, rec.links)
		sum := sha256.Sum256([]byte(text))
		h := hex.EncodeToString(sum[:])
		dst := filepath.Join(absOut, filepath.FromSlash(rec.path))
		if _, statErr := os.Stat(dst); rec.hash == h && statErr == nil {
			r.Advance(true)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			r.Fail("%s: %v", rec.path, err)
			r.Advance(true)
			continue
		}
		if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
			r.Fail("%s: %v", rec.path, err)
			r.Advance(true)
			continue
		}
		if _, err := db.Exec(`UPDATE notes SET out_hash=? WHERE id=?`, h, rec.id); err != nil {
			return err
		}
		written++
		r.Advance(false)
	}
	r.Logf("phase 3: written/updated files — %d", written)
	return nil
}
