package core

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS notes(
	id          INTEGER PRIMARY KEY,
	file_path   TEXT NOT NULL UNIQUE,   -- path relative to source folder
	abs_path    TEXT NOT NULL,          -- absolute path at import time
	file_name   TEXT NOT NULL,          -- filename only
	content     TEXT NOT NULL,          -- original, never changed
	links_md    TEXT NOT NULL DEFAULT '', -- obsidian links ([[...]]) as a separate column
	out_hash    TEXT NOT NULL DEFAULT '', -- hash of the file written in phase 3
	imported_at TEXT NOT NULL
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
	prompt TEXT NOT NULL
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
	seedPrompts(db)
	return db, nil
}

// ResetDB closes the database, removes its files and opens a fresh one.
func ResetDB(db *sql.DB, path string) (*sql.DB, error) {
	_ = db.Close()
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	return OpenDB(path)
}

func seedPrompts(db *sql.DB) {
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM prompts`).Scan(&count)
	if count > 0 {
		return
	}
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
			db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt) VALUES(?,?)`, name, string(data))
		}
		return true
	}
	if exe, err := os.Executable(); err == nil {
		tryDir(filepath.Join(filepath.Dir(exe), "prompts"))
	}
	tryDir("prompts")
	// if still empty — default prompts
	db.QueryRow(`SELECT COUNT(*) FROM prompts`).Scan(&count)
	if count > 0 {
		return
	}
	db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt) VALUES(?,?)`, "phase2",
		"Ты помощник, который размечает личные заметки (дневник, события) для Obsidian.\nДаны две заметки: A и B.\n\n1. Для КАЖДОЙ заметки выдели от 2 до 8 коротких тегов, отражающих, о чём в ней говорится:\n   люди, места, темы, события, проекты, настроение. Теги — на языке заметки, нижний регистр,\n   одно-два слова (вместо пробела используй дефис), без символа #.\n2. Реши, есть ли между A и B реальная смысловая связь: общие люди, места, события,\n   продолжение истории, одна и та же тема. Если связи нет — related=false.\n   Если есть — related=true и в reason одна короткая фраза, почему они связаны.\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"tags_a\":[\"...\"],\"tags_b\":[\"...\"],\"related\":true,\"reason\":\"...\"}\n\n=== Заметка A (файл: {A_NAME}) ===\n{A_CONTENT}\n\n=== Заметка B (файл: {B_NAME}) ===\n{B_CONTENT}")
	db.Exec(`INSERT OR IGNORE INTO prompts(name, prompt) VALUES(?,?)`, "eval",
		"Ты аналитик личных заметок. Оцени заметку на предмет уникальности информации.\n\n=== ОСНОВНАЯ ЗАМЕТКА ({FILE_NAME}) ===\n{CONTENT}\n\n=== СВЯЗАННЫЕ ЗАМЕТКИ ===\n{LINKED_NOTES}\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"novelty\":\"высокая/средняя/низкая\",\"summary\":\"краткое содержание заметки (1-2 предложения)\",\"new_info\":\"что нового в этой заметке по сравнению со связанными (или 'всё уже описано в других заметках')\"}")
}

// LoadPrompt returns a prompt by name, or an empty string if it does not exist.
func LoadPrompt(db *sql.DB, name string) string {
	var p string
	if db.QueryRow(`SELECT prompt FROM prompts WHERE name=?`, name).Scan(&p) == nil {
		return p
	}
	return ""
}

func expandPrompt(tmpl string, vars map[string]string) string {
	s := tmpl
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
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
}

// ListNotes returns all notes ordered by file name.
func ListNotes(db *sql.DB) ([]NoteInfo, error) {
	rows, err := db.Query(`SELECT id, file_name FROM notes ORDER BY file_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []NoteInfo
	for rows.Next() {
		var n NoteInfo
		if err := rows.Scan(&n.ID, &n.Name); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
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

// SavePrompt stores a prompt in the database.
func SavePrompt(db *sql.DB, name, text string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO prompts(name, prompt) VALUES(?,?)`, name, text)
	return err
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
