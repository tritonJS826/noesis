package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// noteMimeType returns the stored MIME type, falling back to the extension for
// databases created before the mime_type column existed.
func noteMimeType(name, mime string) string {
	if mime != "" {
		return mime
	}
	return extMime[strings.ToLower(path.Ext(name))]
}

// askOllamaText sends a plain-text prompt and returns the model's answer. When
// images are supplied they are sent as base64 attachments for vision models.
func askOllamaText(ctx context.Context, c Config, prompt string, images []string) (string, error) {
	return askOllamaTextModel(ctx, c, c.Model, prompt, images)
}

// askOllamaTextModel is like askOllamaText but uses an explicit model name, so
// images can be described by a dedicated vision model.
func askOllamaTextModel(ctx context.Context, c Config, model, prompt string, images []string) (string, error) {
	if strings.TrimSpace(model) == "" {
		model = c.Model
	}
	payload := map[string]any{
		"model":   model,
		"prompt":  prompt,
		"stream":  false,
		"options": map[string]any{"temperature": 0},
	}
	if len(images) > 0 {
		payload["images"] = images
	}
	body, _ := json.Marshal(payload)

	rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST", strings.TrimRight(c.Host, "/")+"/api/generate", bytes.NewReader(body))
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
	var out struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("failed to read ollama response")
	}
	return strings.TrimSpace(stripCodeFences(out.Response)), nil
}

// limitRunes truncates s to at most max runes, adding an ellipsis when cut.
func limitRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	if max == 1 {
		return string(r[:1])
	}
	return string(r[:max-1]) + "…"
}

// saveTagsFromOutput stores tags when a single-process answer happens to be a
// JSON object with a "tags" array. This keeps older custom tag prompts working
// through the generic single-process runner.
func saveTagsFromOutput(db *sql.DB, id int64, output string) int {
	var res struct {
		Tags []string `json:"tags"`
	}
	if json.Unmarshal([]byte(stripCodeFences(output)), &res) != nil || len(res.Tags) == 0 {
		return 0
	}
	added := 0
	for _, t := range cleanTags(res.Tags) {
		r, err := db.Exec(`INSERT OR IGNORE INTO tags(note_id, tag) VALUES(?,?)`, id, t)
		if err != nil {
			continue
		}
		if n, _ := r.RowsAffected(); n > 0 {
			added++
		}
	}
	return added
}

// RunSingleProcess executes a one-by-one prompt for every note and stores the
// answer in process_output. The built-in summary process is additionally
// truncated to SummaryMaxChars.
func RunSingleProcess(ctx context.Context, r Reporter, db *sql.DB, c Config) error {
	promptName := c.PromptName
	if promptName == "" {
		promptName = DefaultSinglePrompt
	}
	tmpl := LoadPrompt(db, promptName)
	if strings.TrimSpace(tmpl) == "" {
		return fmt.Errorf("prompt %q not found", promptName)
	}
	if err := EnsureOllamaModel(ctx, r, c.Host, c.Model); err != nil {
		return err
	}

	rows, err := db.Query(`SELECT id FROM notes ORDER BY id`)
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
	if err := rows.Err(); err != nil {
		return err
	}

	workers := normalizedWorkers(c.Workers)
	r.SetPhase(fmt.Sprintf("single: %s (%s, ×%d)", promptName, c.Model, workers), 0, len(ids))
	if len(ids) == 0 {
		r.Logf("no notes")
		return nil
	}

	var mu sync.Mutex
	done := 0
	err = runConcurrent(ctx, workers, ids, func(id int64) error {
		n, err := loadNote(db, id, c.MaxChars)
		if err != nil {
			r.Fail("note %d: %v", id, err)
			r.Advance(false)
			return nil
		}
		vars := map[string]string{
			"FILE_NAME":    n.Name,
			"CONTENT":      n.Content,
			"LINKED_NOTES": linkedNotesText(db, id, c.MaxChars),
		}
		vars = mergeVars(vars, noteDBVars(db, "db", id))
		prompt := expandPrompt(tmpl, vars)

		r.SetCurrent(n.Name)
		out, err := askOllamaText(ctx, c, prompt, nil)
		if err != nil {
			r.Fail("%s: %v", n.Name, err)
			r.Advance(false)
			return nil
		}
		if promptName == DefaultSummaryProcess {
			out = limitRunes(out, SummaryMaxChars)
		}
		if out == "" {
			r.Fail("%s: empty answer", n.Name)
			r.Advance(false)
			return nil
		}

		// Serialise the writes: sqlite allows a single writer.
		saveMu.Lock()
		saveErr := SaveProcessOutput(db, id, promptName, out)
		added := 0
		if saveErr == nil {
			added = saveTagsFromOutput(db, id, out)
		}
		saveMu.Unlock()
		if saveErr != nil {
			r.Fail("%s: %v", n.Name, saveErr)
			r.Advance(false)
			return nil
		}
		r.AddRun(added, 0)
		r.Advance(false)
		mu.Lock()
		done++
		mu.Unlock()
		r.Logf("%s: %d chars", n.Name, len([]rune(out)))
		return nil
	})
	if err != nil {
		return err
	}
	r.Logf("single process %q: processed — %d", promptName, done)
	return nil
}

type extractDataCandidate struct {
	id      int64
	path    string
	name    string
	abs     string
	blob    string
	content string
	mime    string
	text    string
	tool    string
}

// ExtractDataForAll turns every imported document, image and audio file into
// plain text. Documents and audio use the local extractors/transcriber; images
// are described by the configured Ollama vision model. Results are written to
// the extracted_text column and to the process output of the built-in
// "Data extraction" process so they can be inspected from the database browser.
func ExtractDataForAll(ctx context.Context, r Reporter, db *sql.DB, c Config) error {
	rows, err := db.Query(`SELECT id, file_path, file_name, abs_path, blob_path, content, mime_type,
		markdown, extract_tool FROM notes ORDER BY id`)
	if err != nil {
		return err
	}
	var candidates []extractDataCandidate
	for rows.Next() {
		var n extractDataCandidate
		if err := rows.Scan(&n.id, &n.path, &n.name, &n.abs, &n.blob, &n.content, &n.mime,
			&n.text, &n.tool); err != nil {
			rows.Close()
			return err
		}
		n.mime = noteMimeType(n.name, n.mime)
		if !CanExtract(n.name, n.mime) {
			continue
		}
		candidates = append(candidates, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	mimes := make([]string, len(candidates))
	for i, n := range candidates {
		mimes[i] = n.mime
	}
	if len(candidates) > 0 {
		if err := ensureExtractionModels(ctx, r, c, mimes); err != nil {
			return err
		}
	}

	workers := normalizedWorkers(c.Workers)
	r.SetPhase(fmt.Sprintf("data extraction (docs, images, audio, ×%d)", workers), 0, len(candidates))
	if len(candidates) == 0 {
		r.Logf("nothing to extract")
		return nil
	}

	var mu sync.Mutex
	done := 0
	err = runConcurrent(ctx, workers, candidates, func(n extractDataCandidate) error {
		r.SetCurrent(n.name)

		text, tool := n.text, n.tool
		extractedNow := false
		if strings.TrimSpace(text) == "" {
			data, err := noteBytes(db, n.abs, n.blob, n.content, n.mime)
			if err != nil {
				saveMu.Lock()
				_, _ = db.Exec(`UPDATE notes SET extract_error=?, extract_tool='' WHERE id=?`, err.Error(), n.id)
				saveMu.Unlock()
				r.Fail("%s: %v", n.name, err)
				r.Advance(false)
				return nil
			}

			text, tool, err = ExtractNoteText(ctx, c, db, n.name, n.mime, data, n.id)
			if err != nil {
				saveMu.Lock()
				_, _ = db.Exec(`UPDATE notes SET extract_error=?, extract_tool=? WHERE id=?`, err.Error(), tool, n.id)
				saveMu.Unlock()
				r.Fail("%s: %v", n.name, err)
				r.Advance(false)
				return nil
			}
			text = cleanExtracted(text)
			extractedNow = true
		}

		if strings.TrimSpace(text) == "" {
			saveMu.Lock()
			_, _ = db.Exec(`UPDATE notes SET extract_error=? WHERE id=?`, "no text extracted", n.id)
			saveMu.Unlock()
			r.Fail("%s: no text extracted", n.name)
			r.Advance(false)
			return nil
		}
		// Store the result directly in the markdown column — it is the single
		// source of truth for export, LLM prompts and the database browser.
		if extractedNow {
			saveMu.Lock()
			_, writeErr := db.Exec(`UPDATE notes SET markdown=?, extract_error='', extract_tool=? WHERE id=?`, text, tool, n.id)
			if writeErr == nil {
				_, _ = db.Exec(`DELETE FROM process_output WHERE note_id=? AND process_name=?`, n.id, DataExtractionProcess)
			}
			saveMu.Unlock()
			if writeErr != nil {
				r.Fail("%s: %v", n.name, writeErr)
				r.Advance(false)
				return nil
			}
		}
		mu.Lock()
		done++
		mu.Unlock()
		r.Advance(false)
		r.Logf("%s: %d chars", n.name, len([]rune(text)))
		return nil
	})
	if err != nil {
		return err
	}
	r.Logf("data extraction: processed — %d", done)
	return nil
}
