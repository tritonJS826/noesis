// obsidianize — converts a folder of md notes into an Obsidian vault with tags and bidirectional links.
//
// Phases (each incremental, can be run separately and repeatedly):
//
//	phase1  — import md files into sqlite
//	phase2  — ollama: pairwise note analysis -> tags + links
//	phase3  — write vault clone with front matter (tags) and [[wikilinks]]
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	_ "modernc.org/sqlite"
)

// defaultModel can be overridden at build time via -ldflags "-X main.defaultModel=..."
var defaultModel = "gemma4:31b-cloud"

// ───────────────────────────── DB ─────────────────────────────

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
	seedPrompts(db)
	return db, nil
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

func loadPrompt(db *sql.DB, name string) string {
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
	ran      int // processed in this phase run (for ETA)
	started  time.Time
	errs     int
	logs     []string
	pairsTot int

	runTags   int           // new tags in this phase run
	runLinks  int           // new links in this phase run
	curStart  time.Time     // when the current operation started
	lastDur   time.Duration // duration of the previous operation
	lastTags  int           // tags produced by previous pair
	lastLinks int           // 1 if the previous pair produced a link

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

// addRun records the result of a processed pair: how many new tags and links it produced.
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
	u.logf(cRed+"error: "+cReset+format, a...)
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

	w(" %s%sobsidianize%s  phase: %s%s%s   %s(Ctrl+C — stop, progress is saved)%s", cBold, cCyan, cReset, cYel, phase, cReset, cDim, cReset)
	w(" %s%s%s", cDim, line, cReset)
	w(" Originals in db    : %s%d%s", cBold, s.Notes, cReset)
	w(" Notes with tags    : %s%d%s / %d", cBold, s.Tagged, cReset, s.Notes)
	w(" Tags               : %s%d%s unique  (%d assignments)", cBold, s.UniqTags, cReset, s.TagAssign)
	w(" Links              : %s%d%s pairs (bidirectional)", cBold, s.Links, cReset)
	w(" Pairs analyzed     : %s%d%s / %d", cBold, s.PairsDone, cReset, pairsTotal)
	w(" Written to vault   : %s%d%s / %d", cBold, s.Written, cReset, s.Notes)
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
	w(" Elapsed %s  ·  remaining ~%s  ·  %s items/min  ·  avg %s/item",
		elapsed.Round(time.Second), eta, speed, avg)
	w(" This run: %s+%d%s processed, %s+%d%s tags, %s+%d%s links, %d left",
		cGreen, u.ran, cReset, cGreen, u.runTags, cReset, cGreen, u.runLinks, cReset, u.total-u.done)
	cur := u.current
	if r := []rune(cur); len(r) > 70 {
		cur = string(r[:70]) + "…"
	}
	w(" Now: %s", cur)
	if u.current != "" {
		last := "—"
		if u.lastDur > 0 {
			last = fmt.Sprintf("%s (+%d tags, +%d links)", u.lastDur.Round(10*time.Millisecond), u.lastTags, u.lastLinks)
		}
		w(" Current operation running %s  ·  previous: %s", time.Since(u.curStart).Round(100*time.Millisecond), last)
	} else {
		w("")
	}
	errc := cGreen
	if u.errs > 0 {
		errc = cRed
	}
	w(" Errors: %s%d%s", errc, u.errs, cReset)
	w(" %sLog:%s", cDim, cReset)
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

// ═══════════════════════ TUI (bubbletea) ═══════════════════════

type tuiScreen int

const (
	scrMenu tuiScreen = iota
	scrSettings
	scrModelPicker
	scrRunning
	scrFilePicker
	scrEval
)

type ollamaMsg struct {
	models []string
	err    error
}
type phaseMsg struct{ err error }
type resetMsg struct {
	err error
	db  *sql.DB
}
type evalMsg struct {
	result string
	err    error
}
type noteInfo struct {
	id   int64
	name string
}

type tuiModel struct {
	db      *sql.DB
	program *tea.Program
	screen  tuiScreen
	cursor  int

	src, out, dbPath, modelName, host string
	maxChars, workers                  int

	ollamaModels []string
	ollamaOK     bool
	ollamaErr    string
	ollamaReady  bool

	textInput    textinput.Model
	editingField string

	phaseErr error

	confirmReset bool
	resetting    bool
	resetErr     string

	fileNotes    []noteInfo
	evalResult   string
	evalErr      string
	evalLoading  bool
	fileFilter   string

	promptList    []string
	promptViewing string
	promptText    string

	width int
}

// ─── TUI styles ───────────────────────────────────────────────────────────────

var (
	tuiBorder = lipgloss.Border{
		Top: "─", Bottom: "─", Left: "│", Right: "│",
		TopLeft: "┌", TopRight: "┐", BottomLeft: "└", BottomRight: "┘",
	}
	tuiBox = lipgloss.NewStyle().Border(tuiBorder).Padding(0, 1)

	tuiSel   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	tuiDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiRed   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiGreen = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	tuiYel   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

func tuiLabel(k, v string) string         { return k + ": " + v }
func tuiEmpty(v string) string            { if v == "" { return tuiDim.Render("(not set)") }; return v }

func newTUIModel(db *sql.DB) *tuiModel {
	ti := textinput.New()
	ti.Prompt = ""

	return &tuiModel{
		db:        db,
		screen:    scrMenu,
		src:       ".",
		out:       "out",
		dbPath:    "notes.db",
		modelName: defaultModel,
		host:      "http://localhost:11434",
		maxChars:  4000,
		workers:   1,
		textInput: ti,
	}
}

// ─── commands ─────────────────────────────────────────────────────────────────

func fetchOllamaCmd(host string) tea.Cmd {
	return func() tea.Msg {
		models, err := tuiFetchModels(host)
		return ollamaMsg{models, err}
	}
}

func runPhaseCmd(m *tuiModel) tea.Cmd {
	phase := m.cursor
	src, out, modelName, host, maxChars, workers := m.src, m.out, m.modelName, m.host, m.maxChars, m.workers
	return func() tea.Msg {
		m.program.ReleaseTerminal()
		defer m.program.RestoreTerminal()

		cfg := cfg{Host: host, Model: modelName, MaxChars: maxChars, Workers: workers}
		ui := newUI(m.db)
		ui.start()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		var err error
		switch phase {
		case 0:
			err = phase1(ctx, ui, m.db, src)
		case 1:
			err = phase2(ctx, ui, m.db, cfg)
		case 2:
			err = phase3(ctx, ui, m.db, out, src)
		case 3:
			err = phase1(ctx, ui, m.db, src)
			if err == nil && ctx.Err() == nil {
				err = phase2(ctx, ui, m.db, cfg)
			}
			if err == nil && ctx.Err() == nil {
				err = phase3(ctx, ui, m.db, out, src)
			}
		}
		ui.stop()
		return phaseMsg{err}
	}
}

func resetDBCmd(db *sql.DB, path string) tea.Cmd {
	return func() tea.Msg {
		db.Close()
		os.Remove(path)
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
		newDB, err := openDB(path)
		return resetMsg{err, newDB}
	}
}

func evalNoteCmd(db *sql.DB, noteID int64, host, model string, maxChars int, linkedIDs []int64) tea.Cmd {
	return func() tea.Msg {
		// load the note itself
		var content, fileName string
		db.QueryRow(`SELECT file_name, content FROM notes WHERE id=?`, noteID).Scan(&fileName, &content)

		// load linked notes
		var linkedTexts []string
		for _, lid := range linkedIDs {
			var lc, ln string
			if db.QueryRow(`SELECT file_name, content FROM notes WHERE id=?`, lid).Scan(&ln, &lc) == nil {
				r := []rune(lc)
				if len(r) > maxChars {
					lc = string(r[:maxChars]) + "\n[…truncated…]"
				}
				linkedTexts = append(linkedTexts, "=== "+ln+" ===\n"+lc)
			}
		}

		// truncate the note itself for the prompt
		r := []rune(content)
		if len(r) > maxChars {
			content = string(r[:maxChars]) + "\n[…truncated…]"
		}

		tmpl := loadPrompt(db, "eval")
		if tmpl == "" {
			tmpl = "Ты аналитик личных заметок. Оцени заметку на предмет уникальности информации.\n\n=== ОСНОВНАЯ ЗАМЕТКА ({FILE_NAME}) ===\n{CONTENT}\n\n=== СВЯЗАННЫЕ ЗАМЕТКИ ===\n{LINKED_NOTES}\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"novelty\":\"высокая/средняя/низкая\",\"summary\":\"краткое содержание заметки (1-2 предложения)\",\"new_info\":\"что нового в этой заметке по сравнению со связанными (или 'всё уже описано в других заметках')\"}"
		}
		prompt := expandPrompt(tmpl, map[string]string{
			"FILE_NAME":    fileName,
			"CONTENT":      content,
			"LINKED_NOTES": strings.Join(linkedTexts, "\n\n"),
		})

		body, _ := json.Marshal(map[string]any{
			"model":   model,
			"prompt":  prompt,
			"stream":  false,
			"format":  "json",
			"options": map[string]any{"temperature": 0},
		})
		resp, err := http.Post(strings.TrimRight(host, "/")+"/api/generate", "application/json", bytes.NewReader(body))
		if err != nil {
			return evalMsg{err: err}
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out struct{ Response string }
		if err := json.Unmarshal(raw, &out); err != nil {
			return evalMsg{err: fmt.Errorf("failed to read ollama response")}
		}
		clean := stripCodeFences(out.Response)
		return evalMsg{result: clean}
	}
}

// ─── Init / Update / View ─────────────────────────────────────────────────────

func (m *tuiModel) Init() tea.Cmd {
	return fetchOllamaCmd(m.host)
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// if we're editing a field — delegate to textinput
	if m.editingField != "" {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				m.saveField()
				m.editingField = ""
				return m, fetchOllamaCmd(m.host)
			case "esc":
				m.editingField = ""
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		return m, m.handleKey(msg)

	case ollamaMsg:
		m.ollamaReady = true
		if msg.err != nil {
			m.ollamaOK = false
			m.ollamaErr = msg.err.Error()
		} else {
			m.ollamaOK = true
			m.ollamaModels = msg.models
		}
		return m, nil

	case phaseMsg:
		m.phaseErr = msg.err
		m.screen = scrMenu
		return m, nil

	case resetMsg:
		m.resetting = false
		if msg.err != nil {
			m.resetErr = msg.err.Error()
		} else {
			m.db = msg.db
			m.resetErr = ""
		}
		return m, nil

	case evalMsg:
		m.evalLoading = false
		if msg.err != nil {
			m.evalErr = msg.err.Error()
		} else {
			m.evalResult = msg.result
		}
		return m, nil
	}
	return m, nil
}

func (m *tuiModel) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch m.screen {
	case scrMenu:
		return m.menuKeys(msg)
	case scrSettings:
		return m.settingsKeys(msg)
	case scrModelPicker:
		return m.modelPickerKeys(msg)
	case scrFilePicker:
		return m.filePickerKeys(msg)
	case scrEval:
		return m.evalKeys(msg)
	case scrRunning:
	}
	return nil
}

func (m *tuiModel) menuKeys(msg tea.KeyMsg) tea.Cmd {
	// waiting for reset confirmation
	if m.confirmReset {
		switch msg.String() {
		case "y", "Y":
			m.confirmReset = false
			m.resetting = true
			m.resetErr = ""
			return resetDBCmd(m.db, m.dbPath)
		default:
			m.confirmReset = false
		}
		return nil
	}

	items := 6 // 0..6
	switch msg.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.resetErr = ""
	case "down":
		if m.cursor < items {
			m.cursor++
		}
		m.resetErr = ""
	case "enter":
		switch m.cursor {
		case 4: // settings
			m.screen = scrSettings
			m.cursor = 0
		case 5: // evaluate file
			m.openFilePicker()
		case 6: // reset database
			m.confirmReset = true
		default:
			// check obligatory fields
			if missing := m.checkMissing(); missing != "" {
				m.phaseErr = fmt.Errorf("Not set: %s", missing)
			} else {
				m.screen = scrRunning
				m.phaseErr = nil
				return runPhaseCmd(m)
			}
		}
	}
	return nil
}

func (m *tuiModel) settingsKeys(msg tea.KeyMsg) tea.Cmd {
	if m.promptList != nil {
		return m.promptKeys(msg)
	}
	max := 7 // 0..7
	switch msg.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < max {
			m.cursor++
		}
	case "esc":
		m.screen = scrMenu
		m.cursor = 4
	case "enter":
		m.startEdit()
	}
	return nil
}

func (m *tuiModel) modelPickerKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.ollamaModels)-1 {
			m.cursor++
		}
	case "enter":
		if len(m.ollamaModels) > 0 && m.cursor < len(m.ollamaModels) {
			m.modelName = m.ollamaModels[m.cursor]
		}
		m.screen = scrSettings
		m.cursor = 3
	case "esc":
		m.screen = scrSettings
		m.cursor = 3
	}
	return nil
}

func (m *tuiModel) openFilePicker() {
	rows, err := m.db.Query(`SELECT id, file_name FROM notes ORDER BY file_name`)
	if err != nil {
		return
	}
	defer rows.Close()
	m.fileNotes = nil
	for rows.Next() {
		var ni noteInfo
		rows.Scan(&ni.id, &ni.name)
		m.fileNotes = append(m.fileNotes, ni)
	}
	m.cursor = 0
	m.fileFilter = ""
	m.screen = scrFilePicker
}

func (m *tuiModel) filePickerKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		filtered := m.filteredNotes()
		if m.cursor < len(filtered)-1 {
			m.cursor++
		}
	case "enter":
		filtered := m.filteredNotes()
		if len(filtered) > 0 && m.cursor < len(filtered) {
			id := filtered[m.cursor].id
			linkedRows, _ := m.db.Query(`SELECT dst_id FROM links WHERE src_id=?`, id)
			var linked []int64
			for linkedRows.Next() {
				var lid int64
				linkedRows.Scan(&lid)
				linked = append(linked, lid)
			}
			linkedRows.Close()
			m.evalResult = ""
			m.evalErr = ""
			m.evalLoading = true
			m.screen = scrEval
			return evalNoteCmd(m.db, id, m.host, m.modelName, m.maxChars, linked)
		}
	case "esc":
		m.screen = scrMenu
		m.cursor = 5
	case "backspace":
		r := []rune(m.fileFilter)
		if len(r) > 0 {
			m.fileFilter = string(r[:len(r)-1])
		}
		m.cursor = 0
	default:
		// any printable character — type to filter
		if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
			m.fileFilter += string(msg.Runes[0])
			m.cursor = 0
		}
	}
	return nil
}

func (m *tuiModel) filteredNotes() []noteInfo {
	if m.fileFilter == "" {
		return m.fileNotes
	}
	lower := strings.ToLower(m.fileFilter)
	var out []noteInfo
	for _, n := range m.fileNotes {
		if strings.Contains(strings.ToLower(n.name), lower) {
			out = append(out, n)
		}
	}
	return out
}

func (m *tuiModel) evalKeys(msg tea.KeyMsg) tea.Cmd {
	if !m.evalLoading {
		m.screen = scrMenu
		m.cursor = 5
	}
	return nil
}

// ─── field editing ───────────────────────────────────────────────────────────

func (m *tuiModel) startEdit() {
	switch m.cursor {
	case 0: // SRC
		m.textInput.SetValue(m.src)
		m.textInput.Focus()
		m.editingField = "SRC — path to md notes"
	case 1: // OUT
		m.textInput.SetValue(m.out)
		m.textInput.Focus()
		m.editingField = "OUT — vault folder"
	case 2: // DB
		m.textInput.SetValue(m.dbPath)
		m.textInput.Focus()
		m.editingField = "DB — database file"
	case 3: // MODEL
		if m.ollamaOK && len(m.ollamaModels) > 0 {
			m.screen = scrModelPicker
			m.cursor = 0
			for i, mod := range m.ollamaModels {
				if mod == m.modelName {
					m.cursor = i
					break
				}
			}
			return
		}
		m.textInput.SetValue(m.modelName)
		m.textInput.Focus()
		m.editingField = "MODEL — ollama model"
	case 4: // HOST
		m.textInput.SetValue(m.host)
		m.textInput.Focus()
		m.editingField = "HOST — ollama host"
	case 5: // MAXCHARS
		m.textInput.SetValue(fmt.Sprintf("%d", m.maxChars))
		m.textInput.Focus()
		m.editingField = "MAXCHARS — max chars"
	case 6: // WORKERS
		m.textInput.SetValue(fmt.Sprintf("%d", m.workers))
		m.textInput.Focus()
		m.editingField = "WORKERS — concurrency (1–100)"
	case 7: // PROMPTS
		m.editingField = ""
		m.openPromptList()
		return
	}
}

func (m *tuiModel) saveField() {
	val := m.textInput.Value()
	switch m.cursor {
	case 0:
		m.src = val
	case 1:
		m.out = val
	case 2:
		m.dbPath = val
	case 3:
		m.modelName = val
	case 4:
		m.host = val
	case 5:
		if n, _ := fmt.Sscanf(val, "%d", &m.maxChars); n != 1 {
			m.maxChars = 4000
		}
	case 6:
		if n, _ := fmt.Sscanf(val, "%d", &m.workers); n != 1 || m.workers < 1 {
			m.workers = 1
		}
		if m.workers > 100 {
			m.workers = 100
		}
	case 7: // PROMPTS — save edited prompt
		name := strings.TrimPrefix(m.editingField, "Prompt: ")
		m.db.Exec(`INSERT OR REPLACE INTO prompts(name, prompt) VALUES(?,?)`, name, val)
		m.promptViewing = name
		m.promptText = val
		m.promptList = []string{name}
	}
}

func (m *tuiModel) checkMissing() string {
	var missing []string
	switch m.cursor {
	case 0: // phase1
		if m.src == "" {
			missing = append(missing, "SRC")
		}
	case 1: // phase2 — database is enough
	case 2: // phase3
		if m.out == "" {
			missing = append(missing, "OUT")
		}
		if m.src == "" {
			missing = append(missing, "SRC")
		}
	case 3: // all
		if m.src == "" {
			missing = append(missing, "SRC")
		}
		if m.out == "" {
			missing = append(missing, "OUT")
		}
	}
	return strings.Join(missing, ", ")
}

func (m *tuiModel) openPromptList() {
	rows, _ := m.db.Query(`SELECT name FROM prompts ORDER BY name`)
	defer rows.Close()
	m.promptList = []string{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		m.promptList = append(m.promptList, n)
	}
	if len(m.promptList) == 0 {
		m.promptList = []string{"phase2", "eval"}
	}
	m.cursor = 0
	m.promptViewing = ""
}

func (m *tuiModel) promptKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.promptViewing == "" {
			if m.cursor < len(m.promptList)-1 {
				m.cursor++
			}
		}
	case "enter":
		if m.promptViewing == "" && m.cursor < len(m.promptList) {
			m.promptViewing = m.promptList[m.cursor]
			m.promptText = loadPrompt(m.db, m.promptViewing)
			m.cursor = 0
		}
	case "r", "R":
		if m.promptViewing != "" {
			// reload from file
			data, err := os.ReadFile("prompts/" + m.promptViewing + ".txt")
			if err == nil {
				m.db.Exec(`INSERT OR REPLACE INTO prompts(name, prompt) VALUES(?,?)`, m.promptViewing, string(data))
				m.promptText = string(data)
			}
		}
	case "e", "E":
		if m.promptViewing != "" {
			m.textInput.SetValue(m.promptText)
			m.textInput.Focus()
			m.editingField = "Prompt: " + m.promptViewing
			m.promptList = nil
		}
	case "esc":
		if m.promptViewing != "" {
			m.promptViewing = ""
			m.cursor = 0
		} else {
			m.promptList = nil
			m.promptViewing = ""
			m.screen = scrSettings
			m.cursor = 7
		}
	}
	return nil
}

// ─── View ─────────────────────────────────────────────────────────────────────

func (m *tuiModel) View() string {
	if m.width == 0 {
		m.width = 80
	}
	if m.width < 50 {
		m.width = 50
	}

	switch m.screen {
	case scrSettings:
		return m.viewSettings()
	case scrModelPicker:
		return m.viewModelPicker()
	case scrRunning:
		return m.viewRunning()
	case scrFilePicker:
		return m.viewFilePicker()
	case scrEval:
		return m.viewEval()
	default:
		return m.viewMenu()
	}
}

func (m *tuiModel) viewMenu() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	w := m.width
	cw := w - 4 // inside border+padding

	var s Stats
	if m.db != nil {
		s = loadStats(m.db)
	}
	n := s.Notes
	pairsTotal := n * (n - 1) / 2

	items := []string{
		"Phase 1 — Import md → sqlite",
		"Phase 2 — Tags & links (ollama)",
		"Phase 3 — Write vault",
		"All phases",
		"Settings",
		"Evaluate note uniqueness",
		"Reset database",
	}

	// ── phases ──
	var phaseLines []string
	for i, item := range items {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		line := cursor + item
		if i == 6 {
			line = tuiRed.Render(line)
		}
		phaseLines = append(phaseLines, lipgloss.NewStyle().Width(cw).Render(line))
	}

	// ── settings (two columns) ──
	half := (cw - 1) / 2
	col := func(s string) string { return lipgloss.NewStyle().Width(half).Render(s) }
	settingsLines := []string{
		tuiLabel("SRC", tuiEmpty(m.src)),
		tuiLabel("OUT", tuiEmpty(m.out)),
		tuiLabel("DB", m.dbPath),
		tuiLabel("MODEL", m.modelName),
		tuiLabel("HOST", m.host),
		fmt.Sprintf("MAXCHARS: %d", m.maxChars),
		fmt.Sprintf("WORKERS: %d", m.workers),
	}
	// split into pairs
	var settingPairs []string
	for i := 0; i < len(settingsLines); i += 2 {
		l := col(settingsLines[i])
		r := ""
		if i+1 < len(settingsLines) {
			r = col(settingsLines[i+1])
		}
		settingPairs = append(settingPairs, lipgloss.JoinHorizontal(lipgloss.Top, l, r))
	}

	// ── status ──
	statsLines := []string{
		fmt.Sprintf("Notes: %d │ With tags: %d │ Tags: %d │ Links: %d", s.Notes, s.Tagged, s.UniqTags, s.Links),
		fmt.Sprintf("Pairs: %d/%d │ Written: %d", s.PairsDone, pairsTotal, s.Written),
	}

	// ── ollama ──
	var ollamaLine string
	if !m.ollamaReady {
		ollamaLine = tuiDim.Render("… checking ollama …")
	} else if m.ollamaOK && len(m.ollamaModels) > 0 {
		ollamaLine = tuiGreen.Render("ollama ✓") + "  " + strings.Join(m.ollamaModels, ", ")
	} else {
		ollamaLine = tuiRed.Render("ollama ✗ ") + m.ollamaErr + tuiDim.Render("  (start ollama serve)")
	}
	ollamaLine = lipgloss.NewStyle().Width(cw).Render(ollamaLine)

	// ── messages ──
	var messages []string
	if m.phaseErr != nil {
		messages = append(messages, tuiRed.Render(m.phaseErr.Error()))
	}
	if m.confirmReset {
		messages = append(messages, tuiYel.Render("Delete database "+m.dbPath+" and all data? (y/N)"))
	} else if m.resetting {
		messages = append(messages, tuiDim.Render("Resetting database…"))
	} else if m.resetErr != "" {
		messages = append(messages, tuiRed.Render("Reset error: "+m.resetErr))
	}

	// ── footer ──
	footer := "↑↓ select  ·  Enter run  ·  Q quit"
	if m.confirmReset {
		footer = "y — delete all  ·  any other key — cancel"
	}

	// ── assembly ──
	sep := func(title string) string {
		return lipgloss.NewStyle().Width(cw).Border(
			lipgloss.Border{Bottom: "─"}, false, false, true, false,
		).Render(title)
	}

	var parts []string
	parts = append(parts, lipgloss.JoinVertical(lipgloss.Left, phaseLines...))
	parts = append(parts, sep(""))
	parts = append(parts, settingPairs...)
	parts = append(parts, sep("Status"))
	for _, sl := range statsLines {
		parts = append(parts, lipgloss.NewStyle().Width(cw).Render(sl))
	}
	parts = append(parts, sep(""))
	parts = append(parts, ollamaLine)
	for _, msg := range messages {
		parts = append(parts, lipgloss.NewStyle().Width(cw).Render(msg))
	}
	parts = append(parts, sep(""))
	parts = append(parts, tuiDim.Width(cw).Render(footer))

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return tuiBox.Width(w).Render(content)
}

func (m *tuiModel) viewSettings() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	if m.promptList != nil {
		return m.viewPromptSub()
	}
	w := m.width
	cw := w - 4

	keys := []string{"SRC", "OUT", "DB", "MODEL", "HOST", "MAXCHARS", "WORKERS", "PROMPTS"}
	vals := []string{m.src, m.out, m.dbPath, m.modelName, m.host,
		fmt.Sprintf("%d", m.maxChars), fmt.Sprintf("%d", m.workers), "edit…"}

	var lines []string
	for i, k := range keys {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(
			fmt.Sprintf("%s%-9s: %s", cursor, k, vals[i]),
		))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter edit  ·  ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *tuiModel) viewPromptSub() string {
	w := m.width
	cw := w - 4

	if m.promptViewing != "" {
		// show prompt text
		lines := []string{
			lipgloss.NewStyle().Bold(true).Width(cw).Render("Prompt: " + m.promptViewing),
			"",
		}
		// split into lines
		for _, line := range strings.Split(m.promptText, "\n") {
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(line))
		}
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("ESC back  ·  r reload from file"))
		content := lipgloss.JoinVertical(lipgloss.Left, lines...)
		return tuiBox.Width(w).Render(content)
	}

	// prompt list
	var lines []string
	for i, n := range m.promptList {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+n))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter view  ·  ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *tuiModel) viewModelPicker() string {
	w := m.width
	cw := w - 4

	var lines []string
	if len(m.ollamaModels) == 0 {
		lines = append(lines, tuiRed.Width(cw).Render("no models available"))
	} else {
		for i, mod := range m.ollamaModels {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+mod))
		}
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter pick  ·  ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *tuiModel) viewTextEdit() string {
	w := m.width
	cw := w - 4

	lines := []string{
		lipgloss.NewStyle().Width(cw).Render(m.textInput.View()),
		"",
		tuiDim.Width(cw).Render("Enter save  ·  ESC cancel"),
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *tuiModel) viewRunning() string {
	w := m.width
	cw := w - 4

	lines := []string{
		lipgloss.NewStyle().Width(cw).Render("Processing…"),
		lipgloss.NewStyle().Width(cw).Render("Progress shown in console"),
	}
	if m.phaseErr != nil {
		lines = append(lines, tuiRed.Width(cw).Render("Error: "+m.phaseErr.Error()))
		lines = append(lines, tuiDim.Width(cw).Render("Press any key to return"))
	} else {
		lines = append(lines, tuiDim.Width(cw).Render("Waiting for completion…"))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *tuiModel) viewFilePicker() string {
	w := m.width
	cw := w - 4

	var lines []string
	// filter line
	filterLine := m.fileFilter
	if filterLine == "" {
		filterLine = tuiDim.Render("start typing to filter…")
	} else {
		filterLine += "█"
	}
	lines = append(lines, lipgloss.NewStyle().Width(cw).Render(filterLine))
	lines = append(lines, "")

	filtered := m.filteredNotes()
	if len(filtered) == 0 {
		lines = append(lines, tuiDim.Width(cw).Render("nothing found"))
	} else {
		for i, n := range filtered {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+n.name))
		}
	}
	lines = append(lines, "")
	footer := fmt.Sprintf("found: %d  ·  ↑↓ select  ·  Enter evaluate  ·  ESC back", len(filtered))
	lines = append(lines, tuiDim.Width(cw).Render(footer))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *tuiModel) viewEval() string {
	w := m.width
	cw := w - 4

	var lines []string
	if m.evalLoading {
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render("Analyzing note…"))
		lines = append(lines, tuiDim.Width(cw).Render("Sending to ollama…"))
	} else if m.evalErr != "" {
		lines = append(lines, tuiRed.Width(cw).Render("Error: "+m.evalErr))
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("Press any key to return"))
	} else {
		// parse JSON result and format
		var res struct {
			Novelty string `json:"novelty"`
			Summary string `json:"summary"`
			NewInfo string `json:"new_info"`
		}
		if err := json.Unmarshal([]byte(m.evalResult), &res); err != nil {
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(m.evalResult))
		} else {
			nov := tuiSel.Render
			switch res.Novelty {
			case "высокая":
				nov = tuiGreen.Render
			case "средняя":
				nov = tuiYel.Render
			case "низкая":
				nov = tuiDim.Render
			}
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render("Novelty: "+nov(res.Novelty)))
			lines = append(lines, "")
			lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Summary:"))
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(res.Summary))
			lines = append(lines, "")
			lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("New information:"))
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(res.NewInfo))
		}
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("Press any key to return"))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

// ─── entry point ──────────────────────────────────────────────────────────────

func runTUI(db *sql.DB) error {
	m := newTUIModel(db)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.program = p
	_, err := p.Run()
	return err
}

// ─── ollama helpers ──────────────────────────────────────────────────────────

func tuiFetchModels(host string) ([]string, error) {
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

// ───────────────────────────── Phase 1 ─────────────────────────────

func phase1(ctx context.Context, ui *UI, db *sql.DB, src string) error {
	if src == "" {
		return errors.New("specify source folder: -src FOLDER")
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
	ui.setPhase("1/3 import md → sqlite", 0, len(files))
	ui.logf("found md files: %d", len(files))

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
	ui.logf("phase 1: added new — %d", added)
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
	Workers  int
}

func loadNote(db *sql.DB, id int64, maxChars int) (note, error) {
	n := note{ID: id}
	err := db.QueryRow(`SELECT file_name, content FROM notes WHERE id=?`, id).Scan(&n.Name, &n.Content)
	if len(n.Content) > maxChars {
		r := []rune(n.Content)
		if len(r) > maxChars {
			n.Content = string(r[:maxChars]) + "\n[…truncated…]"
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

func buildPrompt(db *sql.DB, a, b note) string {
	tmpl := loadPrompt(db, "phase2")
	if tmpl == "" {
		tmpl = "Ты помощник, который размечает личные заметки (дневник, события) для Obsidian.\nДаны две заметки: A и B.\n\n1. Для КАЖДОЙ заметки выдели от 2 до 8 коротких тегов, отражающих, о чём в ней говорится:\n   люди, места, темы, события, проекты, настроение. Теги — на языке заметки, нижний регистр,\n   одно-два слова (вместо пробела используй дефис), без символа #.\n2. Реши, есть ли между A и B реальная смысловая связь: общие люди, места, события,\n   продолжение истории, одна и та же тема. Если связи нет — related=false.\n   Если есть — related=true и в reason одна короткая фраза, почему они связаны.\n\nОтветь ТОЛЬКО JSON, без пояснений, в формате:\n{\"tags_a\":[\"...\"],\"tags_b\":[\"...\"],\"related\":true,\"reason\":\"...\"}\n\n=== Заметка A (файл: {A_NAME}) ===\n{A_CONTENT}\n\n=== Заметка B (файл: {B_NAME}) ===\n{B_CONTENT}"
	}
	return expandPrompt(tmpl, map[string]string{
		"A_NAME":    a.Name,
		"A_CONTENT": a.Content,
		"B_NAME":    b.Name,
		"B_CONTENT": b.Content,
	})
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
		return res, fmt.Errorf("invalid JSON from model: %w", err)
	}
	return res, nil
}

// savePair saves the pair result and returns how many NEW tags and links were added.
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
	return newTags, newLinks, tx.Commit()
}

// saveMu protects savePair from races during parallel links_md rebuilds.
var saveMu sync.Mutex

func phase2(ctx context.Context, ui *UI, db *sql.DB, c cfg) error {
	// check ollama availability
	hc := http.Client{Timeout: 5 * time.Second}
	if resp, err := hc.Get(strings.TrimRight(c.Host, "/") + "/api/tags"); err != nil {
		return fmt.Errorf("ollama unavailable at %s: %w", c.Host, err)
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
	workers := c.Workers
	if workers < 1 {
		workers = 1
	}
	ui.setPhase("2/3 ollama: tags & links ("+c.Model+", ×"+fmt.Sprint(workers)+")", len(done), total)
	if total == 0 {
		ui.logf("not enough notes for pair comparison")
		return nil
	}
	ui.logf("total pairs: %d, already done: %d, workers: %d", total, len(done), workers)

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
	ui.logf("remaining: %d pairs", remaining)

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

			ui.setCurrent(j.a.Name + "  ↔  " + j.b.Name)

			var res pairResult
			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				if res, lastErr = askOllama(ctx, c, buildPrompt(db, j.a, j.b)); lastErr == nil {
					break
				}
				if ctx.Err() != nil {
					return
				}
			}
			if lastErr != nil {
				ui.fail("%s ↔ %s: %v", j.a.Name, j.b.Name, lastErr)
				ui.advance(false)
				return
			}
			saveMu.Lock()
			nt, nl, err := savePair(ctx, db, j.a, j.b, res, c.Model)
			saveMu.Unlock()
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			ui.addRun(nt, nl)
			ui.advance(false)
			if res.Related {
				ui.logf(cGreen+"link"+cReset+": %s ↔ %s", j.a.Name, j.b.Name)
			}
			ui.logf("%s ↔ %s: +%d tags", j.a.Name, j.b.Name, nt)
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

func phase3(ctx context.Context, ui *UI, db *sql.DB, out, src string) error {
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
	ui.setPhase("3/3 write vault → "+out, 0, total)

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
	ui.logf("phase 3: written/updated files — %d", written)
	return nil
}

// ───────────────────────────── main ─────────────────────────────

func main() {
	db, err := openDB("notes.db")
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqlite:", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := runTUI(db); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
