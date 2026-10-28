package core

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS notes(
	id          INTEGER PRIMARY KEY,
	file_path   TEXT NOT NULL UNIQUE,   -- path relative to source folder
	abs_path    TEXT NOT NULL,          -- absolute path at import time
	file_name   TEXT NOT NULL,          -- filename only
	content        TEXT NOT NULL DEFAULT '', -- original text, or base64 when binaries are stored in the DB
	mime_type      TEXT NOT NULL DEFAULT '', -- MIME type (text/markdown, image/png, audio/mpeg…)
	extracted_text TEXT NOT NULL DEFAULT '', -- legacy inline extracted text (new imports use extracted_path)
	extracted_path TEXT NOT NULL DEFAULT '', -- link to the extracted .md file (relative to the data dir)
	blob_path      TEXT NOT NULL DEFAULT '', -- link to a materialised binary (zip/json imports)
	extract_error  TEXT NOT NULL DEFAULT '', -- why extraction failed (empty = no error)
	extract_tool   TEXT NOT NULL DEFAULT '', -- extractor/transcriber used, e.g. go:docx / ollama:whisper
	links_md       TEXT NOT NULL DEFAULT '', -- obsidian links ([[...]]) as a separate column
	out_hash       TEXT NOT NULL DEFAULT '', -- hash of the file written in phase 3
	imported_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tags(
	note_id INTEGER NOT NULL REFERENCES notes(id),
	tag     TEXT NOT NULL,
	PRIMARY KEY(note_id, tag)
);
CREATE TABLE IF NOT EXISTS links(
	src_id INTEGER NOT NULL REFERENCES notes(id),
	dst_id INTEGER NOT NULL REFERENCES notes(id),
	reason TEXT NOT NULL DEFAULT '',
	PRIMARY KEY(src_id, dst_id)
);
CREATE TABLE IF NOT EXISTS pairs(
	a_id    INTEGER NOT NULL,
	b_id    INTEGER NOT NULL,
	related INTEGER NOT NULL,
	model   TEXT NOT NULL,
	done_at TEXT NOT NULL,
	PRIMARY KEY(a_id, b_id)
);
CREATE TABLE IF NOT EXISTS prompts(
	name   TEXT PRIMARY KEY,
	prompt TEXT NOT NULL,
	type   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS process_output(
	note_id      INTEGER NOT NULL REFERENCES notes(id),
	process_name TEXT NOT NULL,
	output       TEXT NOT NULL,
	PRIMARY KEY(note_id, process_name)
);
CREATE TABLE IF NOT EXISTS export_templates(
	name     TEXT PRIMARY KEY,
	template TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings(
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// OpenDB opens (or creates) the sqlite database and seeds the default prompts.
func OpenDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if err := ensureColumn(db, "notes", "mime_type", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}
	for _, col := range []string{"extracted_text", "extracted_path", "blob_path", "extract_error", "extract_tool"} {
		if err := ensureColumn(db, "notes", col, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return nil, err
		}
	}
	if err := ensureColumn(db, "prompts", "type", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}
	migratePromptNames(db)
	seedPrompts(db)
	migrateExportTemplateNames(db)
	seedExportTemplates(db)
	migratePromptPlaceholders(db)
	migrateNoteStorage(db)
	return db, nil
}

// DefaultPairPrompt is the name of the built-in pair process.
const DefaultPairPrompt = "Tags and bidirectional linking (for obsidian)"

// DefaultSinglePrompt is the built-in one-by-one tagging process.
const DefaultSinglePrompt = "tag_single"

// DataExtractionProcess is the built-in single process that turns documents,
// images and audio into plain text.
const DataExtractionProcess = "Data extraction"

// DefaultSummaryProcess is the built-in single process that writes a very short
// summary for every note.
const DefaultSummaryProcess = "Short summary"

// SummaryMaxChars is the hard limit applied to the built-in summary output.
const SummaryMaxChars = 500

const dataExtractionFallback = "Опиши содержимое изображения и перепиши весь видимый текст. Отвечай на языке содержимого. Ответь только текстом, без вступлений и пояснений.\n\nФайл: ${FILE_NAME}"

const summaryFallback = "Сделай максимально короткое резюме заметки на языке заметки: одно-два предложения, только суть.\nЖелаемая длина — около 200 символов, жёсткий максимум — 500 символов.\nБез вступлений, без markdown, без списков.\n\n=== Заметка (файл: ${FILE_NAME}) ===\n${CONTENT}"

// DefaultExportTemplate is the name of the built-in export template that
// converts every imported file to a Markdown note.
const DefaultExportTemplate = "All files to Markdown"

// legacyExportTemplate is the old name of the built-in export template.
const legacyExportTemplate = "default"

// migrateExportTemplateNames renames the legacy "default" template to the
// descriptive built-in name, preserving any user edits.
func migrateExportTemplateNames(db *sql.DB) {
	var one int
	if db.QueryRow(`SELECT 1 FROM export_templates WHERE name=?`, legacyExportTemplate).Scan(&one) != nil {
		return // nothing to migrate
	}
	// Do not clobber an existing template that already uses the new name.
	if db.QueryRow(`SELECT 1 FROM export_templates WHERE name=?`, DefaultExportTemplate).Scan(&one) == nil {
		return
	}
	_, _ = db.Exec(`UPDATE export_templates SET name=? WHERE name=?`, DefaultExportTemplate, legacyExportTemplate)
}

// migratePromptNames renames the legacy "phase2" pair prompt to the descriptive
// default name, preserving any user edits and their stored process output.
func migratePromptNames(db *sql.DB) {
	var one int
	if db.QueryRow(`SELECT 1 FROM prompts WHERE name=?`, "phase2").Scan(&one) != nil {
		return // nothing to migrate
	}
	// Do not clobber an existing prompt that already uses the new name.
	if db.QueryRow(`SELECT 1 FROM prompts WHERE name=?`, DefaultPairPrompt).Scan(&one) == nil {
		return
	}
	if _, err := db.Exec(`UPDATE prompts SET name=? WHERE name=?`, DefaultPairPrompt, "phase2"); err != nil {
		return
	}
	_, _ = db.Exec(`UPDATE process_output SET process_name=? WHERE process_name=?`, DefaultPairPrompt, "phase2")
}

// legacyPlaceholder matches the old {KEY} form used by early prompt versions.
// The optional leading $ lets the migrator skip placeholders that already use
// the supported ${KEY} syntax.
var legacyPlaceholder = regexp.MustCompile(`\$?\{([A-Za-z_][A-Za-z0-9_.]*)\}`)

// migratePromptPlaceholders upgrades legacy {KEY} placeholders in stored
// prompts and export templates to the supported ${KEY} syntax. Only known
// variable names are changed, so JSON examples inside prompts stay intact.
func migratePromptPlaceholders(db *sql.DB) {
	known := map[string]bool{
		"FILE_NAME": true, "CONTENT": true, "LINKED_NOTES": true,
		"A_NAME": true, "A_CONTENT": true, "B_NAME": true, "B_CONTENT": true,
		"frontmatter_block": true, "frontmatter": true, "body": true, "related": true,
		"links": true, "tags": true, "tags_yaml": true, "tags_inline": true,
		"name": true, "title": true, "path": true,
	}
	if cols, err := noteColumns(db); err == nil {
		for _, c := range cols {
			known["db."+c] = true
			known["db_b."+c] = true
		}
	}

	upgrade := func(s string) string {
		return legacyPlaceholder.ReplaceAllStringFunc(s, func(match string) string {
			if strings.HasPrefix(match, "$") {
				return match
			}
			key := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
			if known[key] {
				return "${" + key + "}"
			}
			return match
		})
	}

	migrateTable := func(table, textColumn string) {
		rows, err := db.Query(fmt.Sprintf(`SELECT name, %s FROM %s`, textColumn, table))
		if err != nil {
			return
		}
		type update struct{ name, text string }
		var updates []update
		for rows.Next() {
			var name, text string
			if err := rows.Scan(&name, &text); err != nil {
				continue
			}
			if next := upgrade(text); next != text {
				updates = append(updates, update{name, next})
			}
		}
		rows.Close()
		for _, u := range updates {
			_, _ = db.Exec(fmt.Sprintf(`UPDATE %s SET %s=? WHERE name=?`, table, textColumn), u.text, u.name)
		}
	}
	migrateTable("prompts", "prompt")
	migrateTable("export_templates", "template")
}

// ensureColumn adds a column to an existing table if it is missing, so that
// databases created before the column existed keep working. It is a no-op when
// the column already exists.
func ensureColumn(db *sql.DB, table, column, decl string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

// ResetDB closes the database, removes its files and opens a fresh one.
func ResetDB(db *sql.DB, path string) (*sql.DB, error) {
	_ = db.Close()
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	if dir := dataDirForDBPath(path); dir != "" {
		_ = os.RemoveAll(dir)
	}
	return OpenDB(path)
}

func seedPrompts(db *sql.DB) {
	// try file prompts — first next to binary, then in cwd
	tryDir := func(dir string) bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".txt")
			ptype := guessPromptType(name, "")
			db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt, type) VALUES(?,?,?)`, name, string(data), ptype)
		}
		return true
	}
	if exe, err := os.Executable(); err == nil {
		tryDir(filepath.Join(filepath.Dir(exe), "prompts"))
	}
	tryDir("prompts")
	// always ensure the built-in prompts exist (INSERT OR IGNORE keeps any
	// user edits and any file-sourced version intact)
	pairFallback := "Ты помощник, который размечает личные заметки (дневник, события) для Obsidian.\nДаны две заметки: A и B.\n\n1. Для КАЖДОЙ заметки выдели от 2 до 8 коротких тегов, отражающих, о чём в ней говорится:\n   люди, места, темы, события, проекты, настроение. Теги — на языке заметки, нижний регистр,\n   одно-два слова (вместо пробела используй дефис), без символа #.\n2. Реши, есть ли между A и B реальная смысловая связь: общие люди, места, события,\n   продолжение истории, одна и та же тема. Если связи нет — related=false.\n   Если есть — related=true и в reason одна короткая фраза, почему они связаны.\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"tags_a\":[\"...\"],\"tags_b\":[\"...\"],\"related\":true,\"reason\":\"...\"}\n\n=== Заметка A (файл: ${A_NAME}) ===\n${A_CONTENT}\n\n=== Заметка B (файл: ${B_NAME}) ===\n${B_CONTENT}"
	db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt, type) VALUES(?,?,?)`, DefaultPairPrompt, pairFallback, "pair")
	db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt, type) VALUES(?,?,?)`, "eval",
		"Ты аналитик личных заметок. Оцени заметку на предмет уникальности информации.\n\n=== ОСНОВНАЯ ЗАМЕТКА (${FILE_NAME}) ===\n${CONTENT}\n\n=== СВЯЗАННЫЕ ЗАМЕТКИ ===\n${LINKED_NOTES}\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"novelty\":\"высокая/средняя/низкая\",\"summary\":\"краткое содержание заметки (1-2 предложения)\",\"new_info\":\"что нового в этой заметке по сравнению со связанными (или 'всё уже описано в других заметках')\"}", "")
	db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt, type) VALUES(?,?,?)`, DefaultSinglePrompt,
		"Ты помощник, который размечает личные заметки (дневник, события) для Obsidian.\nДана одна заметка.\n\nВыдели от 2 до 8 коротких тегов, отражающих, о чём в ней говорится:\nлюди, места, темы, события, проекты, настроение. Теги — на языке заметки, нижний регистр,\nодно-два слова (вместо пробела используй дефис), без символа #.\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"tags\":[\"...\"]}\n\n=== Заметка (файл: ${FILE_NAME}) ===\n${CONTENT}", "single")
	db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt, type) VALUES(?,?,?)`, DataExtractionProcess, dataExtractionFallback, "single")
	db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt, type) VALUES(?,?,?)`, DefaultSummaryProcess, summaryFallback, "single")
}

// DefaultExportTemplateBody reproduces the built-in Obsidian export format.
// It is expanded with the same ${...} syntax as prompts. The block variables
// already carry their own separators, so this matches the historical output
// byte-for-byte (including notes without front matter or links).
const DefaultExportTemplateBody = "${frontmatter_block}${body}${related}"

// seedExportTemplates makes sure a default export template exists.
func seedExportTemplates(db *sql.DB) {
	db.Exec(`INSERT OR IGNORE INTO export_templates(name, template) VALUES(?,?)`,
		DefaultExportTemplate, DefaultExportTemplateBody)
}

// guessPromptType returns a prompt type based on naming conventions.
func guessPromptType(name, fallback string) string {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "pair") || strings.Contains(lower, "phase2") ||
		strings.Contains(lower, "bidirectional") || strings.Contains(lower, "linking") {
		return "pair"
	}
	if strings.Contains(lower, "single") || strings.Contains(lower, "tag_single") || strings.Contains(lower, "one") ||
		strings.Contains(lower, "summary") || strings.Contains(lower, "extract") {
		return "single"
	}
	return fallback
}

// LoadPrompt returns a prompt by name, or an empty string if it does not exist.
func LoadPrompt(db *sql.DB, name string) string {
	var p string
	if db.QueryRow(`SELECT prompt FROM prompts WHERE name=?`, name).Scan(&p) == nil {
		return p
	}
	return ""
}

// promptPlaceholder matches the ${KEY} placeholder syntax used by prompts and
// export templates.
var promptPlaceholder = regexp.MustCompile(`\$\{([^{}]+)\}`)

// expandPrompt replaces ${KEY} placeholders in tmpl with values from vars.
// Unknown placeholders are left untouched so users can spot typos.
func expandPrompt(tmpl string, vars map[string]string) string {
	return promptPlaceholder.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		if v, ok := vars[key]; ok {
			return v
		}
		return match
	})
}

// noteColumns returns the column names of the notes table, in schema order.
func noteColumns(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`PRAGMA table_info(notes)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

// noteDBVars returns the notes-table columns of the given note as template
// variables with the supplied prefix, e.g. prefix "db" yields "db.file_path".
// This powers the ${db.<column>} / ${db_b.<column>} syntax in prompts and
// export templates.
func noteDBVars(db *sql.DB, prefix string, id int64) map[string]string {
	cols, err := noteColumns(db)
	if err != nil || len(cols) == 0 {
		return nil
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := db.QueryRow(`SELECT `+strings.Join(quoted, ",")+` FROM notes WHERE id=?`, id).Scan(ptrs...); err != nil {
		return nil
	}
	out := make(map[string]string, len(cols))
	for i, c := range cols {
		out[prefix+"."+c] = vals[i].String
	}
	// Extracted text lives in a linked Markdown file for new imports, so make
	// ${db.extracted_text} / ${db_b.extracted_text} return the actual text.
	if extracted := out[prefix+".extracted_text"]; strings.TrimSpace(extracted) == "" {
		if text := resolveExtractedText(db, "", out[prefix+".extracted_path"]); text != "" {
			out[prefix+".extracted_text"] = text
		}
	}
	return out
}

// mergeVars copies all key/value pairs from src into dst.
func mergeVars(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// NotesColumns returns the notes-table column names for UI help text.
func NotesColumns(db *sql.DB) []string {
	cols, err := noteColumns(db)
	if err != nil {
		return nil
	}
	return cols
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}

type Stats struct {
	Notes, Tagged, TagAssign, UniqTags, Links, PairsDone, Written int
}

// LoadStats returns aggregate counters used by the UI.
func LoadStats(db *sql.DB) Stats {
	var s Stats
	q := func(query string, dst *int) { _ = db.QueryRow(query).Scan(dst) }
	q(`SELECT COUNT(*) FROM notes`, &s.Notes)
	q(`SELECT COUNT(DISTINCT note_id) FROM tags`, &s.Tagged)
	q(`SELECT COUNT(*) FROM tags`, &s.TagAssign)
	q(`SELECT COUNT(DISTINCT tag) FROM tags`, &s.UniqTags)
	q(`SELECT COUNT(*)/2 FROM links`, &s.Links)
	q(`SELECT COUNT(*) FROM pairs`, &s.PairsDone)
	q(`SELECT COUNT(*) FROM notes WHERE out_hash<>''`, &s.Written)
	return s
}

func noteLinkName(fileName string) string {
	return strings.TrimSuffix(fileName, filepath.Ext(fileName))
}

// rebuildLinksMD rebuilds the links_md column for a note from the links table.
func rebuildLinksMD(e execer, noteID int64) error {
	rows, err := e.Query(`SELECT n.file_name, l.reason FROM links l
		JOIN notes n ON n.id=l.dst_id WHERE l.src_id=? ORDER BY n.file_name`, noteID)
	if err != nil {
		return err
	}
	var lines []string
	for rows.Next() {
		var name, reason string
		if err := rows.Scan(&name, &reason); err != nil {
			rows.Close()
			return err
		}
		line := "- [[" + noteLinkName(name) + "]]"
		if reason != "" {
			line += " — " + reason
		}
		lines = append(lines, line)
	}
	rows.Close()
	_, err = e.Exec(`UPDATE notes SET links_md=? WHERE id=?`, strings.Join(lines, "\n"), noteID)
	return err
}

// NoteInfo is a lightweight note reference used by file pickers.
type NoteInfo struct {
	ID   int64
	Name string
	Path string
}

// ListNotes returns all notes ordered by file name.
func ListNotes(db *sql.DB) ([]NoteInfo, error) {
	rows, err := db.Query(`SELECT id, file_name, file_path FROM notes ORDER BY file_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []NoteInfo
	for rows.Next() {
		var n NoteInfo
		if err := rows.Scan(&n.ID, &n.Name, &n.Path); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// NoteDetail is a richer note record used by the database browser.
type NoteDetail struct {
	ID         int64
	FileName   string
	FilePath   string
	AbsPath    string
	MimeType   string
	ImportedAt string
	Tags       string // comma-separated tag list
	Links      int    // number of outgoing links
}

// Format returns the note's MIME type, falling back to its file extension.
func (n NoteDetail) Format() string {
	if n.MimeType != "" {
		return n.MimeType
	}
	ext := strings.TrimPrefix(filepath.Ext(n.FileName), ".")
	if ext == "" {
		return "—"
	}
	return ext
}

// ListNotesDetailed returns every note with its format, tags and outgoing link
// count, ordered by path. It is intended for the database browser.
func ListNotesDetailed(db *sql.DB) ([]NoteDetail, error) {
	rows, err := db.Query(`
		SELECT n.id, n.file_name, n.file_path, n.abs_path, n.mime_type, n.imported_at,
		       COALESCE((SELECT GROUP_CONCAT(tag, ', ') FROM tags WHERE note_id=n.id), ''),
		       (SELECT COUNT(*) FROM links WHERE src_id=n.id)
		FROM notes n
		ORDER BY n.file_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []NoteDetail
	for rows.Next() {
		var n NoteDetail
		if err := rows.Scan(&n.ID, &n.FileName, &n.FilePath, &n.AbsPath, &n.MimeType, &n.ImportedAt, &n.Tags, &n.Links); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// NoteField is one named value of a note, used by the database detail view.
type NoteField struct {
	Name  string
	Value string
}

// NoteRecord returns every notes-table column of a note (in schema order) plus
// its tags, so the UI can show the complete record.
func NoteRecord(db *sql.DB, id int64) ([]NoteField, error) {
	cols, err := noteColumns(db)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("notes table has no columns")
	}

	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := db.QueryRow(`SELECT `+strings.Join(quoted, ",")+` FROM notes WHERE id=?`, id).Scan(ptrs...); err != nil {
		return nil, err
	}

	fields := make([]NoteField, 0, len(cols)+1)
	for i, c := range cols {
		value := vals[i].String
		if c == "extracted_text" && strings.TrimSpace(value) == "" {
			for j, other := range cols {
				if other == "extracted_path" {
					value = resolveExtractedText(db, value, vals[j].String)
					break
				}
			}
		}
		fields = append(fields, NoteField{Name: c, Value: value})
	}

	rows, err := db.Query(`SELECT tag FROM tags WHERE note_id=? ORDER BY tag`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fields = append(fields, NoteField{Name: "tags", Value: strings.Join(tags, ", ")})

	// Process outputs (summaries, extracted text, custom processes) are shown
	// after the table columns so they can be inspected in the record view.
	prows, err := db.Query(`SELECT process_name, output FROM process_output WHERE note_id=? ORDER BY process_name`, id)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var name, output string
		if err := prows.Scan(&name, &output); err != nil {
			return nil, err
		}
		fields = append(fields, NoteField{Name: "process: " + name, Value: output})
	}
	if err := prows.Err(); err != nil {
		return nil, err
	}
	return fields, nil
}

// LinkedNoteIDs returns IDs of notes linked from the given note.
func LinkedNoteIDs(db *sql.DB, id int64) ([]int64, error) {
	rows, err := db.Query(`SELECT dst_id FROM links WHERE src_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListPrompts returns prompt names ordered alphabetically.
func ListPrompts(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM prompts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// SavePrompt stores a prompt in the database, preserving its existing type.
func SavePrompt(db *sql.DB, name, text string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO prompts(name, prompt, type) VALUES(?,?,COALESCE((SELECT type FROM prompts WHERE name=?),''))`, name, text, name)
	return err
}

// SavePromptWithType stores a prompt with an explicit type.
func SavePromptWithType(db *sql.DB, name, text, ptype string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO prompts(name, prompt, type) VALUES(?,?,?)`, name, text, ptype)
	return err
}

// DeletePrompt removes a prompt and its associated process output from the database.
func DeletePrompt(db *sql.DB, name string) error {
	if _, err := db.Exec(`DELETE FROM process_output WHERE process_name=?`, name); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM prompts WHERE name=?`, name)
	return err
}

// RenamePrompt renames a prompt (process) and moves its stored process output
// to the new name. It returns an error when the target name already exists.
func RenamePrompt(db *sql.DB, oldName, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("process name must not be empty")
	}
	if newName == oldName {
		return nil
	}
	var one int
	if db.QueryRow(`SELECT 1 FROM prompts WHERE name=?`, newName).Scan(&one) == nil {
		return fmt.Errorf("process %q already exists", newName)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE prompts SET name=? WHERE name=?`, newName, oldName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE process_output SET process_name=? WHERE process_name=?`, newName, oldName); err != nil {
		return err
	}
	return tx.Commit()
}

// ─── export templates ───────────────────────────────────────────────────────

// ListExportTemplates returns template names ordered alphabetically.
func ListExportTemplates(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM export_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// LoadExportTemplate returns the template body, or an empty string if missing.
func LoadExportTemplate(db *sql.DB, name string) string {
	var t string
	if db.QueryRow(`SELECT template FROM export_templates WHERE name=?`, name).Scan(&t) == nil {
		return t
	}
	return ""
}

// ExportTemplateExists reports whether a template with this name exists.
func ExportTemplateExists(db *sql.DB, name string) bool {
	var one int
	return db.QueryRow(`SELECT 1 FROM export_templates WHERE name=?`, name).Scan(&one) == nil
}

// SaveExportTemplate creates or replaces an export template.
func SaveExportTemplate(db *sql.DB, name, text string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO export_templates(name, template) VALUES(?,?)`, name, text)
	return err
}

// DeleteExportTemplate removes an export template by name.
func DeleteExportTemplate(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM export_templates WHERE name=?`, name)
	return err
}

// RenameExportTemplate renames a template, rejecting collisions.
func RenameExportTemplate(db *sql.DB, oldName, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("template name must not be empty")
	}
	if newName == oldName {
		return nil
	}
	var one int
	if db.QueryRow(`SELECT 1 FROM export_templates WHERE name=?`, newName).Scan(&one) == nil {
		return fmt.Errorf("template %q already exists", newName)
	}
	_, err := db.Exec(`UPDATE export_templates SET name=? WHERE name=?`, newName, oldName)
	return err
}

// SaveProcessOutput stores the process output for a specific note.
func SaveProcessOutput(db *sql.DB, noteID int64, processName, output string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO process_output(note_id, process_name, output) VALUES(?,?,?)`, noteID, processName, output)
	return err
}

// LoadProcessOutput returns the stored output for a note and process. The
// built-in "Data extraction" process no longer duplicates the extracted text in
// sqlite: when there is no row, the linked Markdown file is read instead.
func LoadProcessOutput(db *sql.DB, noteID int64, processName string) string {
	var out string
	if db.QueryRow(`SELECT output FROM process_output WHERE note_id=? AND process_name=?`, noteID, processName).Scan(&out) == nil {
		return out
	}
	if processName == DataExtractionProcess {
		var inline, link string
		if db.QueryRow(`SELECT extracted_text, extracted_path FROM notes WHERE id=?`, noteID).Scan(&inline, &link) == nil {
			return resolveExtractedText(db, inline, link)
		}
	}
	return ""
}

// ListPromptsByType returns prompt names filtered by type, ordered alphabetically.
func ListPromptsByType(db *sql.DB, ptype string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM prompts WHERE type=? ORDER BY name`, ptype)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// LoadPromptType returns the type of a prompt, or empty string.
func LoadPromptType(db *sql.DB, name string) string {
	var ptype string
	if db.QueryRow(`SELECT type FROM prompts WHERE name=?`, name).Scan(&ptype) == nil {
		return ptype
	}
	return ""
}

// ReloadPromptFromFile reads prompts/<name>.txt, saves it to the database and
// returns the new text.
func ReloadPromptFromFile(db *sql.DB, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join("prompts", name+".txt"))
	if err != nil {
		return "", err
	}
	if err := SavePrompt(db, name, string(data)); err != nil {
		return "", err
	}
	return string(data), nil
}
