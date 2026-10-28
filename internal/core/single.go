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
	"path"
	"strings"
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
	payload := map[string]any{
		"model":   c.Model,
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
	if err := ensureOllama(ctx, c); err != nil {
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

	r.SetPhase("single: "+promptName+" ("+c.Model+")", 0, len(ids))
	if len(ids) == 0 {
		r.Logf("no notes")
		return nil
	}

	done := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil
		}
		n, err := loadNote(db, id, c.MaxChars)
		if err != nil {
			r.Fail("note %d: %v", id, err)
			r.Advance(false)
			continue
		}
		vars := map[string]string{
			"FILE_NAME": n.Name,
			"CONTENT":   n.Content,
		}
		vars = mergeVars(vars, noteDBVars(db, "db", id))
		prompt := expandPrompt(tmpl, vars)

		r.SetCurrent(n.Name)
		out, err := askOllamaText(ctx, c, prompt, nil)
		if err != nil {
			r.Fail("%s: %v", n.Name, err)
			r.Advance(false)
			continue
		}
		if promptName == DefaultSummaryProcess {
			out = limitRunes(out, SummaryMaxChars)
		}
		if out == "" {
			r.Fail("%s: empty answer", n.Name)
			r.Advance(false)
			continue
		}
		if err := SaveProcessOutput(db, id, promptName, out); err != nil {
			r.Fail("%s: %v", n.Name, err)
			r.Advance(false)
			continue
		}
		added := saveTagsFromOutput(db, id, out)
		r.AddRun(added, 0)
		r.Advance(false)
		done++
		r.Logf("%s: %d chars", n.Name, len([]rune(out)))
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
		if !IsExtractable(n.name, n.mime) && !strings.HasPrefix(n.mime, "image/") {
			continue
		}
		candidates = append(candidates, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	r.SetPhase("data extraction (docs, images, audio)", 0, len(candidates))
	if len(candidates) == 0 {
		r.Logf("nothing to extract")
		return nil
	}

	done := 0
	for _, n := range candidates {
		if ctx.Err() != nil {
			return nil
		}
		r.SetCurrent(n.name)

		text, tool := n.text, n.tool
		extractedNow := false
		if strings.TrimSpace(text) == "" {
			data, err := noteBytes(db, n.abs, n.blob, n.content, n.mime)
			if err != nil {
				_, _ = db.Exec(`UPDATE notes SET extract_error=?, extract_tool='' WHERE id=?`, err.Error(), n.id)
				r.Fail("%s: %v", n.name, err)
				r.Advance(false)
				continue
			}

			if strings.HasPrefix(n.mime, "image/") && n.mime != "image/svg+xml" {
				prompt := LoadPrompt(db, DataExtractionProcess)
				if strings.TrimSpace(prompt) == "" {
					prompt = dataExtractionFallback
				}
				vars := map[string]string{"FILE_NAME": n.name}
				vars = mergeVars(vars, noteDBVars(db, "db", n.id))
				text, err = askOllamaText(ctx, c, expandPrompt(prompt, vars),
					[]string{base64.StdEncoding.EncodeToString(data)})
				tool = "ollama:vision"
			} else if strings.HasPrefix(n.mime, "image/") {
				// SVG is XML text, so no vision model is needed.
				text, tool = string(data), "raw"
			} else {
				text, tool, err = ExtractFileTextWithAudio(ctx, n.name, n.mime, data, c.audioOptions())
			}
			if err != nil {
				_, _ = db.Exec(`UPDATE notes SET extract_error=?, extract_tool=? WHERE id=?`, err.Error(), tool, n.id)
				r.Fail("%s: %v", n.name, err)
				r.Advance(false)
				continue
			}
			text = cleanExtracted(text)
			extractedNow = true
		}

		if strings.TrimSpace(text) == "" {
			_, _ = db.Exec(`UPDATE notes SET extract_error=? WHERE id=?`, "no text extracted", n.id)
			r.Fail("%s: no text extracted", n.name)
			r.Advance(false)
			continue
		}
		// Store the result directly in the markdown column — it is the single
		// source of truth for export, LLM prompts and the database browser.
		if extractedNow {
			if _, err := db.Exec(`UPDATE notes SET markdown=?, extract_error='', extract_tool=? WHERE id=?`, text, tool, n.id); err != nil {
				r.Fail("%s: %v", n.name, err)
				r.Advance(false)
				continue
			}
			_, _ = db.Exec(`DELETE FROM process_output WHERE note_id=? AND process_name=?`, n.id, DataExtractionProcess)
		}
		done++
		r.Advance(false)
		r.Logf("%s: %d chars", n.name, len([]rune(text)))
	}
	r.Logf("data extraction: processed — %d", done)
	return nil
}
