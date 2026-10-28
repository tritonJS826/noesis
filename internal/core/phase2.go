package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

type note struct {
	ID      int64
	Name    string
	Content string
}

type pairResult struct {
	TagsA   []string `json:"tags_a"`
	TagsB   []string `json:"tags_b"`
	Related bool     `json:"related"`
	Reason  string   `json:"reason"`
}

func loadNote(db *sql.DB, id int64, maxChars int) (note, error) {
	n := note{ID: id}
	var mime, extracted string
	err := db.QueryRow(`SELECT file_name, content, extracted_text, mime_type FROM notes WHERE id=?`, id).
		Scan(&n.Name, &n.Content, &extracted, &mime)
	if err != nil {
		return n, err
	}
	n.Content = textForModel(n.Content, extracted, mime)
	if r := []rune(n.Content); len(r) > maxChars {
		n.Content = string(r[:maxChars]) + "\n[…truncated…]"
	}
	return n, nil
}

func cleanTag(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "#")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == '/':
			return r
		case unicode.IsSpace(r):
			return '-'
		}
		return -1
	}, s)
	s = strings.Trim(s, "-/")
	if s == "" || len([]rune(s)) > 40 {
		return ""
	}
	if strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) == -1 {
		return "" // obsidian does not accept digit-only tags
	}
	return s
}

func cleanTags(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = cleanTag(t)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func cleanReason(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ", "[[", "", "]]", "").Replace(strings.TrimSpace(s))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

var reCodeFence = regexp.MustCompile("(?s)^```(?:json)?\\s*\\n?(.*)\\n?```\\s*$")

func stripCodeFences(s string) string {
	m := reCodeFence.FindStringSubmatch(strings.TrimSpace(s))
	if m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

func buildPrompt(db *sql.DB, c Config, a, b note) string {
	promptName := c.PromptName
	if promptName == "" {
		promptName = DefaultPairPrompt
	}
	tmpl := LoadPrompt(db, promptName)
	if tmpl == "" {
		tmpl = "Ты помощник, который размечает личные заметки (дневник, события) для Obsidian.\nДаны две заметки: A и B.\n\n1. Для КАЖДОЙ заметки выдели от 2 до 8 коротких тегов, отражающих, о чём в ней говорится:\n   люди, места, темы, события, проекты, настроение. Теги — на языке заметки, нижний регистр,\n   одно-два слова (вместо пробела используй дефис), без символа #.\n2. Реши, есть ли между A и B реальная смысловая связь: общие люди, места, события,\n   продолжение истории, одна и та же тема. Если связи нет — related=false.\n   Если есть — related=true и в reason одна короткая фраза, почему они связаны.\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"tags_a\":[\"...\"],\"tags_b\":[\"...\"],\"related\":true,\"reason\":\"...\"}\n\n=== Заметка A (файл: ${A_NAME}) ===\n${A_CONTENT}\n\n=== Заметка B (файл: ${B_NAME}) ===\n${B_CONTENT}"
	}
	vars := map[string]string{
		"A_NAME":    a.Name,
		"A_CONTENT": a.Content,
		"B_NAME":    b.Name,
		"B_CONTENT": b.Content,
	}
	vars = mergeVars(vars, noteDBVars(db, "db", a.ID))
	vars = mergeVars(vars, noteDBVars(db, "db_b", b.ID))
	return expandPrompt(tmpl, vars)
}

func askOllama(ctx context.Context, c Config, prompt string) (pairResult, error) {
	var res pairResult
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
		return res, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return res, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return res, err
	}
	clean := stripCodeFences(out.Response)
	if err := json.Unmarshal([]byte(clean), &res); err != nil {
		return res, fmt.Errorf("invalid JSON from model: %w", err)
	}
	return res, nil
}

// savePair saves the pair result and returns how many NEW tags and links were added.
func savePair(ctx context.Context, db *sql.DB, a, b note, r pairResult, model, promptName string) (newTags, newLinks int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	insTags := func(id int64, tags []string) error {
		for _, t := range cleanTags(tags) {
			res, err := tx.Exec(`INSERT OR IGNORE INTO tags(note_id, tag) VALUES(?,?)`, id, t)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				newTags++
			}
		}
		return nil
	}
	if err := insTags(a.ID, r.TagsA); err != nil {
		return 0, 0, err
	}
	if err := insTags(b.ID, r.TagsB); err != nil {
		return 0, 0, err
	}
	rel := 0
	if r.Related {
		rel = 1
		reason := cleanReason(r.Reason)
		var one int
		if tx.QueryRow(`SELECT 1 FROM links WHERE src_id=? AND dst_id=?`, a.ID, b.ID).Scan(&one) != nil {
			newLinks = 1
		}
		// bidirectional link
		for _, p := range [][2]int64{{a.ID, b.ID}, {b.ID, a.ID}} {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO links(src_id, dst_id, reason) VALUES(?,?,?)`, p[0], p[1], reason); err != nil {
				return 0, 0, err
			}
		}
		if err := rebuildLinksMD(tx, a.ID); err != nil {
			return 0, 0, err
		}
		if err := rebuildLinksMD(tx, b.ID); err != nil {
			return 0, 0, err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO pairs(a_id, b_id, related, model, done_at) VALUES(?,?,?,?,?)`,
		a.ID, b.ID, rel, model, time.Now().Format(time.RFC3339)); err != nil {
		return 0, 0, err
	}
	err = tx.Commit()
	if err != nil {
		return newTags, newLinks, err
	}
	// Save process output per note
	if promptName != "" {
		tagsA, _ := json.Marshal(map[string]any{"tags": r.TagsA})
		tagsB, _ := json.Marshal(map[string]any{"tags": r.TagsB})
		SaveProcessOutput(db, a.ID, promptName, string(tagsA))
		SaveProcessOutput(db, b.ID, promptName, string(tagsB))
	}
	return newTags, newLinks, nil
}

// saveMu protects savePair from races during parallel links_md rebuilds.
var saveMu sync.Mutex

// Phase2 asks ollama for tags and links for every pair of notes.
func Phase2(ctx context.Context, r Reporter, db *sql.DB, c Config) error {
	// check ollama availability
	if err := ensureOllama(ctx, c); err != nil {
		return err
	}

	var ids []int64
	rows, err := db.Query(`SELECT id FROM notes ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	done := map[[2]int64]bool{}
	prow, err := db.Query(`SELECT a_id, b_id FROM pairs`)
	if err != nil {
		return err
	}
	for prow.Next() {
		var x, y int64
		_ = prow.Scan(&x, &y)
		done[[2]int64{x, y}] = true
	}
	prow.Close()

	n := len(ids)
	total := n * (n - 1) / 2
	workers := c.Workers
	if workers < 1 {
		workers = 1
	}
	r.SetPhase("2/3 ollama: tags & links ("+c.Model+", ×"+fmt.Sprint(workers)+")", len(done), total)
	if total == 0 {
		r.Logf("not enough notes for pair comparison")
		return nil
	}
	r.Logf("total pairs: %d, already done: %d, workers: %d", total, len(done), workers)

	// collect all unprocessed pairs
	type job struct {
		a, b note
		key  [2]int64
	}
	var jobs []job
	for i := 0; i < n; i++ {
		var a note
		loadedA := false
		for j := i + 1; j < n; j++ {
			key := [2]int64{ids[i], ids[j]}
			if done[key] {
				continue
			}
			if !loadedA {
				if a, err = loadNote(db, ids[i], c.MaxChars); err != nil {
					return err
				}
				loadedA = true
			}
			b, err := loadNote(db, ids[j], c.MaxChars)
			if err != nil {
				return err
			}
			jobs = append(jobs, job{a, b, key})
		}
	}
	remaining := len(jobs)
	r.Logf("remaining: %d pairs", remaining)

	// worker pool
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	errCh := make(chan error, 1) // buffer 1 — first fatal error stops everything

	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		// check for fatal error
		select {
		case e := <-errCh:
			wg.Wait()
			return e
		default:
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()

			r.SetCurrent(j.a.Name + "  ↔  " + j.b.Name)

			var res pairResult
			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				if res, lastErr = askOllama(ctx, c, buildPrompt(db, c, j.a, j.b)); lastErr == nil {
					break
				}
				if ctx.Err() != nil {
					return
				}
			}
			if lastErr != nil {
				r.Fail("%s ↔ %s: %v", j.a.Name, j.b.Name, lastErr)
				r.Advance(false)
				return
			}
			saveMu.Lock()
			nt, nl, err := savePair(ctx, db, j.a, j.b, res, c.Model, c.PromptName)
			saveMu.Unlock()
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			r.AddRun(nt, nl)
			r.Advance(false)
			if res.Related {
				r.Logf("link: %s ↔ %s", j.a.Name, j.b.Name)
			}
			r.Logf("%s ↔ %s: +%d tags", j.a.Name, j.b.Name, nt)
		}(j)
	}
	wg.Wait()

	// check for fatal errors after completion
	select {
	case e := <-errCh:
		return e
	default:
	}
	return nil
}
