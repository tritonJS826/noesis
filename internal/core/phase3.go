package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path"
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

// exportVars builds the template variables available to export templates for
// one note. The ${frontmatter_block}, ${body} and ${related} variables already
// contain their own separators, so the default template reproduces the
// historical Obsidian output exactly.
func exportVars(content string, tags []string, linksMD string) map[string]string {
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

	var fmRaw strings.Builder
	for _, l := range fm {
		fmRaw.WriteString(l + "\n")
	}
	var tagsYAML strings.Builder
	if len(all) > 0 {
		tagsYAML.WriteString("tags:\n")
		for _, t := range all {
			tagsYAML.WriteString("  - " + t + "\n")
		}
	}
	var frontmatterBlock strings.Builder
	if has || len(all) > 0 {
		frontmatterBlock.WriteString("---\n")
		frontmatterBlock.WriteString(fmRaw.String())
		frontmatterBlock.WriteString(tagsYAML.String())
		frontmatterBlock.WriteString("---\n\n")
	}

	inline := make([]string, 0, len(all))
	for _, t := range all {
		inline = append(inline, "#"+t)
	}

	related := ""
	if strings.TrimSpace(linksMD) != "" {
		related = "\n## Related notes\n" + linksMD + "\n"
	}

	return map[string]string{
		"frontmatter_block": frontmatterBlock.String(),
		"frontmatter":       fmRaw.String(),
		"tags_yaml":         tagsYAML.String(),
		"tags":              tagsYAML.String(),
		"tags_inline":       strings.Join(inline, " "),
		"body":              strings.TrimRight(body, "\n") + "\n",
		"related":           related,
		"links":             linksMD,
	}
}

// renderExport expands the selected export template for one note. When the
// template is missing or empty the built-in default is used.
func renderExport(db *sql.DB, templateName string, id int64, path, content, linksMD string, tags []string) string {
	tmpl := ""
	if templateName != "" {
		tmpl = LoadExportTemplate(db, templateName)
	}
	if tmpl == "" {
		tmpl = DefaultExportTemplateBody
	}
	vars := exportVars(content, tags, linksMD)
	name := filepath.Base(path)
	vars["name"] = name
	vars["title"] = noteLinkName(name)
	vars["path"] = path
	vars = mergeVars(vars, noteDBVars(db, "db", id))
	return expandPrompt(tmpl, vars)
}

// isMarkdownExport reports whether the selected template is the built-in
// "All files to Markdown" export (an empty name also falls back to it).
func isMarkdownExport(templateName string) bool {
	return templateName == "" || templateName == DefaultExportTemplate
}

// exportPath returns the destination path for one note. The built-in Markdown
// template converts every imported file to a .md note, while custom templates
// keep the original relative path.
func exportPath(relPath, templateName string) string {
	rel := filepath.ToSlash(relPath)
	if !isMarkdownExport(templateName) {
		return rel
	}
	ext := path.Ext(rel)
	if strings.EqualFold(ext, ".md") || strings.EqualFold(ext, ".markdown") {
		return rel
	}
	// Dotfiles such as ".env" have no usable base name: keep them and add .md.
	if ext == "" || path.Base(rel) == ext {
		return rel + ".md"
	}
	return strings.TrimSuffix(rel, ext) + ".md"
}

// Phase3 writes the vault using the selected export template.
func Phase3(ctx context.Context, r Reporter, db *sql.DB, out, src, templateName string) error {
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
	r.SetPhase("3/3 export → "+out, 0, total)

	type rec struct {
		id                     int64
		path, content          string
		extracted, mime, links string
		hash                   string
	}
	rows, err := db.Query(`SELECT id, file_path, content, extracted_text, mime_type, links_md, out_hash FROM notes ORDER BY id`)
	if err != nil {
		return err
	}
	var recs []rec
	for rows.Next() {
		var rec rec
		if err := rows.Scan(&rec.id, &rec.path, &rec.content, &rec.extracted, &rec.mime, &rec.links, &rec.hash); err != nil {
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

		// Binary documents (PDF, Office, audio…) are exported as their plain
		// extracted text instead of the raw base64 payload. Older databases may
		// lack a mime type, so fall back to the file extension.
		mime := rec.mime
		if mime == "" {
			mime = extMime[strings.ToLower(path.Ext(rec.path))]
		}
		content := textForModel(rec.content, rec.extracted, mime)
		text := renderExport(db, templateName, rec.id, rec.path, content, rec.links, tags)
		sum := sha256.Sum256([]byte(text))
		h := hex.EncodeToString(sum[:])
		dst := filepath.Join(absOut, filepath.FromSlash(exportPath(rec.path, templateName)))
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
