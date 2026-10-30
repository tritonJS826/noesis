package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// findNotesByPath returns notes whose file_path or file_name contains the
// query (case-insensitive). An empty query matches all notes.
func findNotesByPath(db *sql.DB, query string) ([]NoteInfo, error) {
	q := strings.ToLower(query)
	rows, err := db.Query(`SELECT id, file_name, file_path FROM notes ORDER BY file_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoteInfo
	for rows.Next() {
		var n NoteInfo
		var path string
		if err := rows.Scan(&n.ID, &n.Name, &path); err != nil {
			return nil, err
		}
		if q == "" || strings.Contains(strings.ToLower(path), q) || strings.Contains(strings.ToLower(n.Name), q) {
			n.Path = path
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// RemoveNote deletes the note with the matching path from the database, along
// with its tags, pairs and links. References kept in other notes' links_md are
// left as-is.
func RemoveNote(db *sql.DB, query string) (int, error) {
	notes, err := findNotesByPath(db, query)
	if err != nil {
		return 0, err
	}
	return deleteNotes(db, notes)
}

// RemoveNoteAndLinks deletes the note and also removes every obsidian link to
// it from all other notes (their links_md column is rebuilt).
func RemoveNoteAndLinks(db *sql.DB, query string) (removed int, scrubbed int, err error) {
	notes, err := findNotesByPath(db, query)
	if err != nil {
		return 0, 0, err
	}
	return removeNotesAndScrub(db, notes)
}

// RemoveNoteByID deletes a single note identified by id.
func RemoveNoteByID(db *sql.DB, id int64) (int, error) {
	return deleteNotes(db, []NoteInfo{{ID: id}})
}

// RemoveNoteAndLinksByID deletes a single note and scrubs its links from other
// notes.
func RemoveNoteAndLinksByID(db *sql.DB, id int64) (int, int, error) {
	return removeNotesAndScrub(db, []NoteInfo{{ID: id}})
}

// removeNotesAndScrub deletes notes and rebuilds the links_md of every other
// note that had linked to one of them, so the [[link]] disappears.
func removeNotesAndScrub(db *sql.DB, notes []NoteInfo) (int, int, error) {
	affected := affectedSources(db, notes)
	removed, err := deleteNotes(db, notes)
	if err != nil {
		return 0, 0, err
	}
	for _, s := range affected {
		_ = rebuildLinksMD(db, s)
	}
	return removed, len(affected), nil
}

// affectedSources returns the ids of notes that link to any of the given notes
// (the sources whose links_md must be rebuilt after removal).
func affectedSources(db *sql.DB, notes []NoteInfo) []int64 {
	if len(notes) == 0 {
		return nil
	}
	rows, err := db.Query(`SELECT DISTINCT src_id FROM links WHERE dst_id IN (`+placeholders(len(notes))+`)`, idArgs(notes)...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	removed := make(map[int64]bool, len(notes))
	for _, n := range notes {
		removed[n.ID] = true
	}
	seen := map[int64]bool{}
	var out []int64
	for rows.Next() {
		var s int64
		_ = rows.Scan(&s)
		if removed[s] || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func idArgs(notes []NoteInfo) []any {
	args := make([]any, len(notes))
	for i, n := range notes {
		args[i] = n.ID
	}
	return args
}

// deleteNotes removes the given notes along with their tags, pairs, links and
// process output rows. The callers decide whether to also scrub the references
// from other notes' links_md.
func deleteNotes(db *sql.DB, notes []NoteInfo) (int, error) {
	if len(notes) == 0 {
		return 0, nil
	}
	// Remember sidecar files before deleting rows so they can be cleaned up
	// after a successful commit. Original files linked through abs_path are
	// deliberately left untouched.
	var stored []string
	for _, n := range notes {
		var extractedPath, blobPath string
		if db.QueryRow(`SELECT extracted_path, blob_path FROM notes WHERE id=?`, n.ID).Scan(&extractedPath, &blobPath) == nil {
			stored = append(stored, extractedPath, blobPath)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, n := range notes {
		if _, err := tx.Exec(`DELETE FROM tags WHERE note_id=?`, n.ID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM pairs WHERE a_id=? OR b_id=?`, n.ID, n.ID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM links WHERE src_id=? OR dst_id=?`, n.ID, n.ID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM process_output WHERE note_id=?`, n.ID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM rag_chunks WHERE note_id=?`, n.ID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM rag_notes WHERE note_id=?`, n.ID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM notes WHERE id=?`, n.ID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, p := range stored {
		removeStoredFile(db, p)
	}
	return len(notes), nil
}

// NoteContent holds a note's textual content for prompting (binary content is
// replaced by a placeholder).
type NoteContent struct {
	ID      int64
	Name    string
	Path    string
	Content string
	Mime    string
}

func loadNoteContent(db *sql.DB, id int64, maxChars int) (NoteContent, error) {
	var n NoteContent
	var markdown string
	err := db.QueryRow(`SELECT id, file_name, file_path, markdown, mime_type FROM notes WHERE id=?`, id).
		Scan(&n.ID, &n.Name, &n.Path, &markdown, &n.Mime)
	if err != nil {
		return n, err
	}
	n.Content = textForModel(markdown, n.Mime)
	if r := []rune(n.Content); len(r) > maxChars {
		n.Content = string(r[:maxChars]) + "\n[…truncated…]"
	}
	return n, nil
}

// textForModel returns the human-readable text for a note. For text notes
// markdown is the original content; for binary documents, audio and images it
// is the extracted text / transcript / description. When nothing is available
// it returns a placeholder so prompts remain understandable.
func textForModel(markdown, mime string) string {
	if strings.TrimSpace(markdown) != "" {
		return markdown
	}
	if mime != "" && !IsText(mime) {
		return fmt.Sprintf("[binary file: %s — no extracted text available]", mime)
	}
	return ""
}

// CompareFile evaluates a file's novelty using the legacy Ollama audio backend.
func CompareFile(ctx context.Context, db *sql.DB, path, host, model, transcribeModel string, maxChars int) (string, error) {
	return CompareFileWithAudio(ctx, db, path, host, model, AudioOptions{
		Engine:      AudioEngineOllama,
		OllamaHost:  host,
		OllamaModel: transcribeModel,
	}, maxChars)
}

// CompareFileWithAudio evaluates a file's novelty the same way EvalNote does,
// but without adding it to the database. The file is compared against ALL notes
// already in the db treated as "related notes".
func CompareFileWithAudio(ctx context.Context, db *sql.DB, path, host, model string, audio AudioOptions, maxChars int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	name := filepathBase(path)
	mimeType := mimeFor(name, data)
	rawContent := string(data)
	if !IsText(mimeType) {
		rawContent = base64.StdEncoding.EncodeToString(data)
	}

	var extracted, extractTool, extractErrText string
	if IsExtractable(name, mimeType) {
		extracted, extractTool, err = ExtractFileTextWithAudio(ctx, name, mimeType, data, audio)
		if err != nil {
			extractErrText = err.Error()
			return "", fmt.Errorf("%s: %w", name, err)
		}
	}

	// Build the markdown equivalent for a file not yet in the database.
	// Text files get their raw content; binary files get extracted text.
	md := rawContent
	if !IsText(mimeType) && strings.TrimSpace(extracted) != "" {
		md = extracted
	}
	content := textForModel(md, mimeType)
	if r := []rune(content); len(r) > maxChars {
		content = string(r[:maxChars]) + "\n[…truncated…]"
	}

	var linkedTexts []string
	rows, err := db.Query(`SELECT file_name, markdown, mime_type FROM notes ORDER BY file_name`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var noteName, md, mime string
		if err := rows.Scan(&noteName, &md, &mime); err != nil {
			rows.Close()
			return "", err
		}
		text := textForModel(md, mime)
		if r := []rune(text); len(r) > maxChars {
			text = string(r[:maxChars]) + "\n[…truncated…]"
		}
		linkedTexts = append(linkedTexts, "=== "+noteName+" ===\n"+text)
	}
	rows.Close()

	tmpl := LoadPrompt(db, "eval")
	if tmpl == "" {
		tmpl = "Ты аналитик личных заметок. Оцени заметку на предмет уникальности информации.\n\n=== ОСНОВНАЯ ЗАМЕТКА (${FILE_NAME}) ===\n${CONTENT}\n\n=== СВЯЗАННЫЕ ЗАМЕТКИ ===\n${LINKED_NOTES}\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"novelty\":\"высокая/средняя/низкая\",\"summary\":\"краткое содержание заметки (1-2 предложения)\",\"new_info\":\"что нового в этой заметке по сравнению со связанными (или 'всё уже описано в других заметках')\"}"
	}
	// The compared file is not in the database, so expose its own fields under
	// the db.* namespace to keep templates working.
	vars := map[string]string{
		"FILE_NAME":         name,
		"CONTENT":           content,
		"LINKED_NOTES":      strings.Join(linkedTexts, "\n\n"),
		"db.file_name":      name,
		"db.file_path":      path,
		"db.content":        rawContent,
		"db.markdown":       md,
		"db.extracted_text": md,
		"db.extract_error":  extractErrText,
		"db.extract_tool":   extractTool,
		"db.mime_type":      mimeType,
	}
	prompt := expandPrompt(tmpl, vars)

	body, _ := json.Marshal(map[string]any{
		"model":   model,
		"prompt":  prompt,
		"stream":  false,
		"format":  "json",
		"options": map[string]any{"temperature": 0},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(host, "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct{ Response string }
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("failed to read ollama response")
	}
	return stripCodeFences(out.Response), nil
}

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// TagSingle asks ollama for tags for a single note and stores them. Returns
// the number of new tags added.
func TagSingle(ctx context.Context, db *sql.DB, c Config, id int64) (int, error) {
	promptName := c.PromptName
	if promptName == "" {
		promptName = "tag_single"
	}
	tmpl := LoadPrompt(db, promptName)
	if tmpl == "" {
		tmpl = "Ты помощник, который размечает личные заметки (дневник, события) для Obsidian.\nДана одна заметка.\n\nВыдели от 2 до 8 коротких тегов, отражающих, о чём в ней говорится:\nлюди, места, темы, события, проекты, настроение. Теги — на языке заметки, нижний регистр,\nодно-два слова (вместо пробела используй дефис), без символа #.\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"tags\":[\"...\"]}\n\n=== Заметка (файл: ${FILE_NAME}) ===\n${CONTENT}"
	}
	n, err := loadNoteContent(db, id, c.MaxChars)
	if err != nil {
		return 0, err
	}
	vars := map[string]string{
		"FILE_NAME":    n.Name,
		"CONTENT":      n.Content,
		"LINKED_NOTES": linkedNotesText(db, id, c.MaxChars),
	}
	vars = mergeVars(vars, noteDBVars(db, "db", id))
	prompt := expandPrompt(tmpl, vars)

	var res struct {
		Tags []string `json:"tags"`
	}
	body, _ := json.Marshal(map[string]any{
		"model":   c.Model,
		"prompt":  prompt,
		"stream":  false,
		"format":  "json",
		"options": map[string]any{"temperature": 0},
	})
	rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, "POST", strings.TrimRight(c.Host, "/")+"/api/generate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct{ Response string }
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, err
	}
	if err := json.Unmarshal([]byte(stripCodeFences(out.Response)), &res); err != nil {
		return 0, fmt.Errorf("invalid JSON from model: %w", err)
	}

	// Serialise the writes: sqlite allows a single writer, and TagSingle may run
	// concurrently under the worker pool.
	saveMu.Lock()
	defer saveMu.Unlock()

	// Save process output for this note
	if promptName != "" {
		_ = SaveProcessOutput(db, id, promptName, stripCodeFences(out.Response))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	added := 0
	for _, t := range cleanTags(res.Tags) {
		r, err := tx.Exec(`INSERT OR IGNORE INTO tags(note_id, tag) VALUES(?,?)`, id, t)
		if err != nil {
			return 0, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			added++
		}
	}
	return added, tx.Commit()
}

// TagAllSingle runs a one-by-one processing phase. The built-in tagging prompt
// keeps its historical "only untagged notes" behaviour; the data extraction
// process fills extracted text for documents/images/audio; every other single
// prompt is executed as a generic process and its output is stored.
func TagAllSingle(ctx context.Context, r Reporter, db *sql.DB, c Config) error {
	switch c.PromptName {
	case DataExtractionProcess:
		return ExtractDataForAll(ctx, r, db, c)
	case RAGIndexProcess:
		return BuildRAGIndex(ctx, r, db, c)
	case "", DefaultSinglePrompt:
		// built-in tagging below
	default:
		return RunSingleProcess(ctx, r, db, c)
	}

	if err := EnsureOllamaModel(ctx, r, c.Host, c.Model); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT id FROM notes WHERE id NOT IN (SELECT DISTINCT note_id FROM tags) ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	promptName := c.PromptName
	if promptName == "" {
		promptName = DefaultSinglePrompt
	}

	workers := normalizedWorkers(c.Workers)
	r.SetPhase(fmt.Sprintf("tags one-by-one (%s, ×%d)", c.Model, workers), 0, len(ids))
	if len(ids) == 0 {
		r.Logf("all notes already tagged")
		return nil
	}

	var mu sync.Mutex
	done := 0
	err = runConcurrent(ctx, workers, ids, func(id int64) error {
		n, _ := loadNoteContent(db, id, c.MaxChars)
		r.SetCurrent(n.Name)
		added, err := TagSingle(ctx, db, c, id)
		if err != nil {
			r.Fail("%s: %v", n.Name, err)
			r.Advance(false)
			return nil
		}
		r.AddRun(added, 0)
		r.Advance(false)
		mu.Lock()
		done++
		mu.Unlock()
		r.Logf("%s: +%d tags", n.Name, added)
		return nil
	})
	if err != nil {
		return err
	}
	r.Logf("one-by-one: tagged — %d", done)
	return nil
}

// FindFiles searches the filesystem starting from the given directory for files
// whose path contains the query (case-insensitive). Returns at most 200 matches.
func FindFiles(dir, query string) ([]string, error) {
	lower := strings.ToLower(query)
	var matches []string
	err := filepathWalk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if lower == "" || strings.Contains(strings.ToLower(path), lower) {
			matches = append(matches, path)
			if len(matches) >= 200 {
				return fmt.Errorf("limit") // break early
			}
		}
		return nil
	})
	if err != nil && err.Error() != "limit" {
		return nil, err
	}
	return matches, nil
}

func filepathWalk(dir string, fn func(path string, info os.FileInfo, err error) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		fullPath := filepathJoin(dir, e.Name())
		if err := fn(fullPath, info, nil); err != nil {
			if err.Error() == "limit" {
				return err
			}
			continue
		}
		if e.IsDir() {
			if err := filepathWalk(fullPath, fn); err != nil {
				if err.Error() == "limit" {
					return err
				}
				continue
			}
		}
	}
	return nil
}

func filepathJoin(dir, name string) string {
	sep := string(os.PathSeparator)
	return strings.TrimRight(dir, "/\\") + sep + name
}
