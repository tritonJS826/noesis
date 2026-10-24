// obsidianize — превращает папку md-заметок в Obsidian-vault с тегами и двусторонними ссылками.
//
// Сборка:
//
//	go mod init obsidianize && go get modernc.org/sqlite && go build -o obsidianize .
//
// Фазы (каждая инкрементальная, можно запускать отдельно и повторно):
//
//	phase1  — импорт md-файлов в sqlite
//	phase2  — ollama: попарный анализ заметок -> теги + связи
//	phase3  — запись клона папки с front matter (tags) и [[ссылками]]
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// defaultModel переопределяется при сборке через -ldflags "-X main.defaultModel=..."
var defaultModel = "gemma4:31b-cloud"

// ───────────────────────────── DB ─────────────────────────────

const schema = `
CREATE TABLE IF NOT EXISTS notes(
	id          INTEGER PRIMARY KEY,
	file_path   TEXT NOT NULL UNIQUE,   -- путь относительно исходной папки
	abs_path    TEXT NOT NULL,          -- абсолютный путь на момент импорта
	file_name   TEXT NOT NULL,          -- только имя файла
	content     TEXT NOT NULL,          -- оригинал, не меняется
	links_md    TEXT NOT NULL DEFAULT '', -- obsidian-ссылки ([[...]]) отдельной колонкой
	out_hash    TEXT NOT NULL DEFAULT '', -- хеш записанного в фазе 3 файла
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
`

func openDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return db, nil
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}

type Stats struct {
	Notes, Tagged, TagAssign, UniqTags, Links, PairsDone, Written int
}

func loadStats(db *sql.DB) Stats {
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

// rebuildLinksMD пересобирает колонку links_md для заметки из таблицы links.
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

// ───────────────────────────── TUI ─────────────────────────────

const (
	cReset = "\x1b[0m"
	cBold  = "\x1b[1m"
	cDim   = "\x1b[2m"
	cCyan  = "\x1b[36m"
	cGreen = "\x1b[32m"
	cYel   = "\x1b[33m"
	cRed   = "\x1b[31m"
	eol    = "\x1b[K"
)

type UI struct {
	db  *sql.DB
	tty bool

	mu       sync.Mutex
	phase    string
	current  string
	done     int
	total    int
	ran      int // сколько обработано в этом запуске фазы (для ETA)
	started  time.Time
	errs     int
	logs     []string
	pairsTot int

	runTags   int           // новых тегов в этом запуске фазы
	runLinks  int           // новых связей в этом запуске фазы
	curStart  time.Time     // когда началась текущая операция
	lastDur   time.Duration // длительность предыдущей операции
	lastTags  int           // сколько тегов дала предыдущая пара
	lastLinks int           // 1, если предыдущая пара дала связь

	quit chan struct{}
	wg   sync.WaitGroup
}

func newUI(db *sql.DB) *UI {
	st, _ := os.Stdout.Stat()
	return &UI{db: db, tty: st != nil && st.Mode()&os.ModeCharDevice != 0, started: time.Now()}
}

func (u *UI) setPhase(name string, done, total int) {
	u.mu.Lock()
	u.phase, u.done, u.total, u.ran, u.started, u.current = name, done, total, 0, time.Now(), ""
	u.runTags, u.runLinks, u.lastDur, u.lastTags, u.lastLinks = 0, 0, 0, 0, 0
	u.curStart = time.Now()
	u.mu.Unlock()
}
func (u *UI) setCurrent(s string) {
	u.mu.Lock()
	u.current = s
	u.curStart = time.Now()
	u.mu.Unlock()
}

// addRun фиксирует результат обработанной пары: сколько новых тегов и связей она дала.
func (u *UI) addRun(tags, links int) {
	u.mu.Lock()
	u.runTags += tags
	u.runLinks += links
	u.lastTags, u.lastLinks = tags, links
	u.lastDur = time.Since(u.curStart)
	u.mu.Unlock()
}
func (u *UI) advance(skipped bool) {
	u.mu.Lock()
	u.done++
	if !skipped {
		u.ran++
	}
	u.mu.Unlock()
}
func (u *UI) logf(format string, a ...any) {
	u.mu.Lock()
	u.logs = append(u.logs, time.Now().Format("15:04:05 ")+fmt.Sprintf(format, a...))
	if len(u.logs) > 8 {
		u.logs = u.logs[len(u.logs)-8:]
	}
	u.mu.Unlock()
}
func (u *UI) fail(format string, a ...any) {
	u.mu.Lock()
	u.errs++
	u.mu.Unlock()
	u.logf(cRed+"ошибка: "+cReset+format, a...)
}

func bar(done, total, w int) string {
	if total <= 0 {
		return strings.Repeat("░", w)
	}
	f := done * w / total
	if f > w {
		f = w
	}
	return cGreen + strings.Repeat("█", f) + cDim + strings.Repeat("░", w-f) + cReset
}

func (u *UI) render() string {
	s := loadStats(u.db)
	u.mu.Lock()
	defer u.mu.Unlock()

	var b strings.Builder
	w := func(format string, a ...any) { b.WriteString(fmt.Sprintf(format, a...) + eol + "\n") }

	n := s.Notes
	pairsTotal := n * (n - 1) / 2
	phase := u.phase
	if phase == "" {
		phase = "—"
	}
	line := strings.Repeat("─", 64)

	w(" %s%sobsidianize%s  фаза: %s%s%s   %s(Ctrl+C — остановка, прогресс сохраняется)%s", cBold, cCyan, cReset, cYel, phase, cReset, cDim, cReset)
	w(" %s%s%s", cDim, line, cReset)
	w(" Оригиналов в базе   : %s%d%s", cBold, s.Notes, cReset)
	w(" Заметок с тегами    : %s%d%s / %d", cBold, s.Tagged, cReset, s.Notes)
	w(" Тегов               : %s%d%s уникальных  (%d назначений)", cBold, s.UniqTags, cReset, s.TagAssign)
	w(" Связей              : %s%d%s пар (двусторонних)", cBold, s.Links, cReset)
	w(" Пар проанализировано: %s%d%s / %d", cBold, s.PairsDone, cReset, pairsTotal)
	w(" Записано в vault    : %s%d%s / %d", cBold, s.Written, cReset, s.Notes)
	w(" %s%s%s", cDim, line, cReset)

	pct := 0
	if u.total > 0 {
		pct = u.done * 100 / u.total
	}
	elapsed := time.Since(u.started)
	eta, speed, avg := "—", "—", "—"
	if u.ran > 0 && elapsed > 0 {
		per := elapsed / time.Duration(u.ran)
		avg = per.Round(10 * time.Millisecond).String()
		speed = fmt.Sprintf("%.1f", float64(u.ran)/elapsed.Minutes())
		if u.total > u.done {
			eta = (per * time.Duration(u.total-u.done)).Round(time.Second).String()
		} else {
			eta = "0s"
		}
	}
	w(" %s %s%3d%%%s  %s%d%s / %d", bar(u.done, u.total, 36), cBold, pct, cReset, cBold, u.done, cReset, u.total)
	w(" Прошло %s  ·  осталось ~%s  ·  %s шт/мин  ·  ср. %s/шт",
		elapsed.Round(time.Second), eta, speed, avg)
	w(" В этом запуске: %s+%d%s обработано, %s+%d%s тегов, %s+%d%s связей, осталось %d",
		cGreen, u.ran, cReset, cGreen, u.runTags, cReset, cGreen, u.runLinks, cReset, u.total-u.done)
	cur := u.current
	if r := []rune(cur); len(r) > 70 {
		cur = string(r[:70]) + "…"
	}
	w(" Сейчас: %s", cur)
	if u.current != "" {
		last := "—"
		if u.lastDur > 0 {
			last = fmt.Sprintf("%s (+%d тегов, +%d связей)", u.lastDur.Round(10*time.Millisecond), u.lastTags, u.lastLinks)
		}
		w(" Текущая операция идёт %s  ·  предыдущая: %s", time.Since(u.curStart).Round(100*time.Millisecond), last)
	} else {
		w("")
	}
	errc := cGreen
	if u.errs > 0 {
		errc = cRed
	}
	w(" Ошибок: %s%d%s", errc, u.errs, cReset)
	w(" %sЛог:%s", cDim, cReset)
	for i := 0; i < 8; i++ {
		if i < len(u.logs) {
			l := u.logs[i]
			w("  %s", l)
		} else {
			w("")
		}
	}
	return b.String()
}

func (u *UI) start() {
	u.quit = make(chan struct{})
	if !u.tty {
		return
	}
	fmt.Print("\x1b[?1049h\x1b[?25l")
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fmt.Print("\x1b[H" + u.render() + "\x1b[J")
			case <-u.quit:
				return
			}
		}
	}()
}

func (u *UI) stop() {
	close(u.quit)
	u.wg.Wait()
	if u.tty {
		fmt.Print("\x1b[?25h\x1b[?1049l")
	}
	fmt.Print(strings.ReplaceAll(u.render(), eol, ""))
}

// ───────────────────────────── Phase 1 ─────────────────────────────

func phase1(ctx context.Context, ui *UI, db *sql.DB, src string) error {
	if src == "" {
		return errors.New("укажите исходную папку: -src ПАПКА")
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
	ui.setPhase("1/3 импорт md → sqlite", 0, len(files))
	ui.logf("найдено md-файлов: %d", len(files))

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
		ui.setCurrent(rel)

		var one int
		if err := tx.QueryRow(`SELECT 1 FROM notes WHERE file_path=?`, rel).Scan(&one); err == nil {
			ui.advance(true)
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			ui.fail("%s: %v", rel, err)
			ui.advance(true)
			continue
		}
		_, err = tx.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, imported_at) VALUES(?,?,?,?,?)`,
			rel, p, filepath.Base(p), string(data), time.Now().Format(time.RFC3339))
		if err != nil {
			ui.fail("%s: %v", rel, err)
			ui.advance(true)
			continue
		}
		added++
		ui.advance(false)
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
	ui.logf("фаза 1: добавлено новых — %d", added)
	return nil
}

// ───────────────────────────── Phase 2 ─────────────────────────────

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

type cfg struct {
	Host     string
	Model    string
	MaxChars int
}

func loadNote(db *sql.DB, id int64, maxChars int) (note, error) {
	n := note{ID: id}
	err := db.QueryRow(`SELECT file_name, content FROM notes WHERE id=?`, id).Scan(&n.Name, &n.Content)
	if len(n.Content) > maxChars {
		r := []rune(n.Content)
		if len(r) > maxChars {
			n.Content = string(r[:maxChars]) + "\n[…обрезано…]"
		}
	}
	return n, err
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
		return "" // obsidian не принимает теги только из цифр
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

func buildPrompt(a, b note) string {
	return `Ты помощник, который размечает личные заметки (дневник, события) для Obsidian.
Даны две заметки: A и B.

1. Для КАЖДОЙ заметки выдели от 2 до 8 коротких тегов, отражающих, о чём в ней говорится:
   люди, места, темы, события, проекты, настроение. Теги — на языке заметки, нижний регистр,
   одно-два слова (вместо пробела используй дефис), без символа #.
2. Реши, есть ли между A и B реальная смысловая связь: общие люди, места, события,
   продолжение истории, одна и та же тема. Если связи нет — related=false.
   Если есть — related=true и в reason одна короткая фраза, почему они связаны.

Ответь ТОЛЬКО JSON, без пояснений, в формате:
{"tags_a":["..."],"tags_b":["..."],"related":true,"reason":"..."}

=== Заметка A (файл: ` + a.Name + `) ===
` + a.Content + `

=== Заметка B (файл: ` + b.Name + `) ===
` + b.Content + `
`
}

func askOllama(ctx context.Context, c cfg, prompt string) (pairResult, error) {
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
		return res, fmt.Errorf("невалидный JSON от модели: %w", err)
	}
	return res, nil
}

// savePair сохраняет результат пары и возвращает, сколько НОВЫХ тегов и связей добавилось.
func savePair(ctx context.Context, db *sql.DB, a, b note, r pairResult, model string) (newTags, newLinks int, err error) {
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
		// двусторонняя связь
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
	return newTags, newLinks, tx.Commit()
}

func phase2(ctx context.Context, ui *UI, db *sql.DB, c cfg) error {
	// проверка доступности ollama
	hc := http.Client{Timeout: 5 * time.Second}
	if resp, err := hc.Get(strings.TrimRight(c.Host, "/") + "/api/tags"); err != nil {
		return fmt.Errorf("ollama недоступна по %s: %w", c.Host, err)
	} else {
		resp.Body.Close()
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
	ui.setPhase("2/3 ollama: теги и связи ("+c.Model+")", len(done), total)
	if total == 0 {
		ui.logf("недостаточно заметок для сравнения пар")
		return nil
	}
	ui.logf("пар всего: %d, уже готово: %d", total, len(done))

	for i := 0; i < n; i++ {
		var a note
		aLoaded := false
		for j := i + 1; j < n; j++ {
			if ctx.Err() != nil {
				return nil
			}
			key := [2]int64{ids[i], ids[j]}
			if done[key] {
				continue
			}
			if !aLoaded {
				if a, err = loadNote(db, ids[i], c.MaxChars); err != nil {
					return err
				}
				aLoaded = true
			}
			b, err := loadNote(db, ids[j], c.MaxChars)
			if err != nil {
				return err
			}
			ui.setCurrent(a.Name + "  ↔  " + b.Name)

			var res pairResult
			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				if res, lastErr = askOllama(ctx, c, buildPrompt(a, b)); lastErr == nil {
					break
				}
				if ctx.Err() != nil {
					return nil
				}
			}
			if lastErr != nil {
				ui.fail("%s ↔ %s: %v", a.Name, b.Name, lastErr)
				ui.advance(false) // не помечаем как done — повторится при следующем запуске
				continue
			}
			nt, nl, err := savePair(ctx, db, a, b, res, c.Model)
			if err != nil {
				return err
			}
			done[key] = true
			ui.addRun(nt, nl)
			ui.advance(false)
			if res.Related {
				ui.logf(cGreen+"связь"+cReset+": %s ↔ %s", a.Name, b.Name)
			}
			ui.logf("%s ↔ %s: +%d тегов", a.Name, b.Name, nt)
		}
	}
	return nil
}

// ───────────────────────────── Phase 3 ─────────────────────────────

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

// extractTags вынимает существующий ключ tags: из front matter и возвращает остальные строки.
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
		b.WriteString("\n## Связанные заметки\n" + linksMD + "\n")
	}
	return b.String()
}

func phase3(ctx context.Context, ui *UI, db *sql.DB, out, src string) error {
	if out == "" {
		return errors.New("укажите папку для клона: -out ПАПКА")
	}
	absOut, _ := filepath.Abs(out)
	if src != "" {
		if absSrc, _ := filepath.Abs(src); absSrc == absOut {
			return errors.New("-out не должен совпадать с исходной папкой")
		}
	}
	var total int
	_ = db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&total)
	ui.setPhase("3/3 запись vault → "+out, 0, total)

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
		var r rec
		if err := rows.Scan(&r.id, &r.path, &r.content, &r.links, &r.hash); err != nil {
			rows.Close()
			return err
		}
		recs = append(recs, r)
	}
	rows.Close()

	written := 0
	for _, r := range recs {
		if ctx.Err() != nil {
			return nil
		}
		ui.setCurrent(r.path)
		trows, err := db.Query(`SELECT tag FROM tags WHERE note_id=? ORDER BY tag`, r.id)
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

		text := compose(r.content, tags, r.links)
		sum := sha256.Sum256([]byte(text))
		h := hex.EncodeToString(sum[:])
		dst := filepath.Join(absOut, filepath.FromSlash(r.path))
		if _, statErr := os.Stat(dst); r.hash == h && statErr == nil {
			ui.advance(true)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			ui.fail("%s: %v", r.path, err)
			ui.advance(true)
			continue
		}
		if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
			ui.fail("%s: %v", r.path, err)
			ui.advance(true)
			continue
		}
		if _, err := db.Exec(`UPDATE notes SET out_hash=? WHERE id=?`, h, r.id); err != nil {
			return err
		}
		written++
		ui.advance(false)
	}
	ui.logf("фаза 3: записано/обновлено файлов — %d", written)
	return nil
}

// ───────────────────────────── main ─────────────────────────────

const usage = `obsidianize — конвертер md-заметок в Obsidian-vault (теги + двусторонние ссылки через ollama)

Использование:
  obsidianize phase1 -src ПАПКА_С_MD [-db notes.db]
      Создаёт sqlite (если нет) и импортирует все .md (путь, имя файла, текст).
      Уже импортированные файлы пропускаются.

  obsidianize phase2 [-db notes.db] [-model gemma4:31b-cloud] [-host http://localhost:11434] [-maxchars 4000]
      Ollama попарно сравнивает заметки (1-2, 1-3, 2-3 ...), ставит теги и [[ссылки]].
      Уже обработанные пары пропускаются; новые заметки добавляют только новые пары.
      Внимание: пар N*(N-1)/2 — на больших коллекциях это долго.

  obsidianize phase3 -out ПАПКА_VAULT [-src ИСХОДНАЯ] [-db notes.db]
      Пишет клон папки с front matter (tags) и разделом «Связанные заметки».
      Неизменившиеся файлы не перезаписываются.

  obsidianize all -src ... -out ...   — все три фазы подряд
  obsidianize status [-db notes.db]   — только статистика

Ctrl+C безопасно останавливает работу — прогресс сохраняется в базе.
`

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Print(usage)
		return
	}
	cmd := os.Args[1]
	fl := flag.NewFlagSet(cmd, flag.ExitOnError)
	dbPath := fl.String("db", "notes.db", "путь к sqlite-базе")
	src := fl.String("src", "", "исходная папка с md (phase1)")
	out := fl.String("out", "", "папка для результата (phase3)")
	model := fl.String("model", defaultModel, "модель ollama")
	host := fl.String("host", "http://localhost:11434", "адрес ollama")
	maxChars := fl.Int("maxchars", 4000, "максимум символов заметки в промпте")
	_ = fl.Parse(os.Args[2:])

	switch cmd {
	case "phase1", "phase2", "phase3", "all", "status":
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	db, err := openDB(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqlite:", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSig()

	ui := newUI(db)
	ui.start()

	var runErr error
	run := func(f func() error) {
		if runErr == nil && ctx.Err() == nil {
			runErr = f()
		}
	}
	c := cfg{Host: *host, Model: *model, MaxChars: *maxChars}
	switch cmd {
	case "phase1":
		run(func() error { return phase1(ctx, ui, db, *src) })
	case "phase2":
		run(func() error { return phase2(ctx, ui, db, c) })
	case "phase3":
		run(func() error { return phase3(ctx, ui, db, *out, *src) })
	case "all":
		run(func() error { return phase1(ctx, ui, db, *src) })
		run(func() error { return phase2(ctx, ui, db, c) })
		run(func() error { return phase3(ctx, ui, db, *out, *src) })
	case "status":
		ui.setPhase("status", 0, 0)
	}

	ui.stop()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "\n"+cRed+"ошибка:"+cReset, runErr)
		os.Exit(1)
	}
	if ctx.Err() != nil {
		fmt.Println("\nОстановлено. Прогресс сохранён, запустите команду снова, чтобы продолжить.")
	}
}
