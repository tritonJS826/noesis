package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ensureOllama verifies that the ollama host is reachable before running
// model phases.
func ensureOllama(ctx context.Context, c Config) error {
	hc := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.Host, "/")+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("ollama unavailable at %s: %w", c.Host, err)
	}
	resp.Body.Close()
	return nil
}

// FetchModels returns the models available on the ollama host.
func FetchModels(host string) ([]string, error) {
	hc := http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Get(strings.TrimRight(host, "/") + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Models []struct{ Name string } `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range result.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// EvalNote asks ollama how unique a note's information is compared to the
// notes it is linked to.
func EvalNote(ctx context.Context, db *sql.DB, noteID int64, host, model string, maxChars int, linkedIDs []int64) (string, error) {
	// load the note itself
	var content, fileName, mime string
	db.QueryRow(`SELECT file_name, content, mime_type FROM notes WHERE id=?`, noteID).Scan(&fileName, &content, &mime)
	content = textForModel(content, mime)

	// load linked notes
	var linkedTexts []string
	for _, lid := range linkedIDs {
		var lc, ln, lm string
		if db.QueryRow(`SELECT file_name, content, mime_type FROM notes WHERE id=?`, lid).Scan(&ln, &lc, &lm) == nil {
			lc = textForModel(lc, lm)
			if r := []rune(lc); len(r) > maxChars {
				lc = string(r[:maxChars]) + "\n[…truncated…]"
			}
			linkedTexts = append(linkedTexts, "=== "+ln+" ===\n"+lc)
		}
	}

	// truncate the note itself for the prompt
	if r := []rune(content); len(r) > maxChars {
		content = string(r[:maxChars]) + "\n[…truncated…]"
	}

	tmpl := LoadPrompt(db, "eval")
	if tmpl == "" {
		tmpl = "Ты аналитик личных заметок. Оцени заметку на предмет уникальности информации.\n\n=== ОСНОВНАЯ ЗАМЕТКА (${FILE_NAME}) ===\n${CONTENT}\n\n=== СВЯЗАННЫЕ ЗАМЕТКИ ===\n${LINKED_NOTES}\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"novelty\":\"высокая/средняя/низкая\",\"summary\":\"краткое содержание заметки (1-2 предложения)\",\"new_info\":\"что нового в этой заметке по сравнению со связанными (или 'всё уже описано в других заметках')\"}"
	}
	vars := map[string]string{
		"FILE_NAME":    fileName,
		"CONTENT":      content,
		"LINKED_NOTES": strings.Join(linkedTexts, "\n\n"),
	}
	vars = mergeVars(vars, noteDBVars(db, "db", noteID))
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
	var out struct{ Response string }
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("failed to read ollama response")
	}
	return stripCodeFences(out.Response), nil
}
