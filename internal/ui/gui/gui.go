// Package gui implements the Fyne desktop frontend. Like the TUI and CLI it
// only drives internal/core; the actual work is UI-independent.
package gui

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"noesis/internal/core"
)

const maxLogLines = 200

type state struct {
	app fyne.App
	win fyne.Window

	mu     sync.Mutex
	db     *sql.DB
	dbPath string

	running bool
	cancel  context.CancelFunc

	total, done, ran  int
	runTags, runLinks int
	logs              []string

	src, out, dbEntry, model, host *widget.Entry
	maxChars, workers              *widget.Entry

	phaseLabel   *widget.Label
	currentLabel *widget.Label
	runLabel     *widget.Label
	statsLabel   *widget.Label
	logLabel     *widget.Label
	progress     *widget.ProgressBar

	runButtons []*widget.Button
	resetBtn   *widget.Button
}

// Run opens the database and starts the desktop application. It blocks until
// the window is closed.
func Run(dbPath string) error {
	db, err := core.OpenDB(dbPath)
	if err != nil {
		return err
	}

	s := newState(db, dbPath)
	s.win.ShowAndRun()

	s.mu.Lock()
	if s.db != nil {
		_ = s.db.Close()
	}
	s.mu.Unlock()
	return nil
}

func newState(db *sql.DB, dbPath string) *state {
	cfg := core.DefaultConfig()
	s := &state{db: db, dbPath: dbPath}
	s.app = app.NewWithID("com.noesis.noesis")
	s.win = s.app.NewWindow("noesis")

	s.src = widget.NewEntry()
	s.src.SetText(".")
	s.out = widget.NewEntry()
	s.out.SetText("out")
	s.dbEntry = widget.NewEntry()
	s.dbEntry.SetText(dbPath)
	s.host = widget.NewEntry()
	s.host.SetText(cfg.Host)
	s.model = widget.NewEntry()
	s.model.SetText(cfg.Model)
	s.maxChars = widget.NewEntry()
	s.maxChars.SetText(strconv.Itoa(cfg.MaxChars))
	s.workers = widget.NewEntry()
	s.workers.SetText(strconv.Itoa(cfg.Workers))

	form := widget.NewForm(
		widget.NewFormItem("Source folder", s.folderRow(s.src, "Select source folder")),
		widget.NewFormItem("Vault folder", s.folderRow(s.out, "Select vault folder")),
		widget.NewFormItem("Database", s.dbEntry),
		widget.NewFormItem("Ollama host", s.host),
		widget.NewFormItem("Model", s.model),
		widget.NewFormItem("Max chars", s.maxChars),
		widget.NewFormItem("Workers", s.workers),
	)

	s.phaseLabel = widget.NewLabel("Ready")
	s.currentLabel = widget.NewLabel("")
	s.runLabel = widget.NewLabel("")
	s.statsLabel = widget.NewLabel("")
	s.progress = widget.NewProgressBar()

	s.logLabel = widget.NewLabel("")
	s.logLabel.Wrapping = fyne.TextWrapWord
	s.logLabel.TextStyle = fyne.TextStyle{Monospace: true}
	s.logLabel.Selectable = true

	s.runButtons = []*widget.Button{
		widget.NewButton("Phase 1 — Import", func() { s.startPhase(core.PhaseImport) }),
		widget.NewButton("Phase 2 — Analyze", func() { s.startPhase(core.PhaseAnalyze) }),
		widget.NewButton("Phase 3 — Write", func() { s.startPhase(core.PhaseWrite) }),
		widget.NewButton("All phases", func() { s.startPhase(core.PhaseAll) }),
	}
	s.resetBtn = widget.NewButton("Reset database", s.confirmReset)
	s.resetBtn.Importance = widget.DangerImportance

	var buttons []fyne.CanvasObject
	for _, b := range s.runButtons {
		buttons = append(buttons, b)
	}
	buttons = append(buttons, s.resetBtn)

	top := container.NewVBox(form, container.NewHBox(buttons...))
	bottom := container.NewVBox(
		s.phaseLabel,
		s.progress,
		s.currentLabel,
		s.runLabel,
		s.statsLabel,
	)
	logs := container.NewVScroll(s.logLabel)
	logs.SetMinSize(fyne.NewSize(700, 260))

	s.win.SetContent(container.NewBorder(top, bottom, nil, nil, logs))
	s.win.Resize(fyne.NewSize(980, 800))
	s.win.CenterOnScreen()
	s.win.SetCloseIntercept(s.close)
	s.refreshStats()

	return s
}

func (s *state) close() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.win.Close()
}

// folderRow builds an entry with a cross-platform "Browse…" button.
func (s *state) folderRow(entry *widget.Entry, title string) fyne.CanvasObject {
	browse := widget.NewButton("Browse…", func() {
		s.browseFolder(entry, title)
	})
	return container.NewBorder(nil, nil, nil, browse, entry)
}

func (s *state) browseFolder(entry *widget.Entry, title string) {
	d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		if uri != nil {
			entry.SetText(uri.Path())
		}
	}, s.win)
	d.SetTitleText(title)

	// Start in the currently entered folder when it exists.
	if path := strings.TrimSpace(entry.Text); path != "" {
		if abs, err := filepath.Abs(path); err == nil {
			if uri, err := storage.ListerForURI(storage.NewFileURI(abs)); err == nil {
				d.SetLocation(uri)
			}
		}
	}

	d.Show()
}

func (s *state) setRunning(running bool) {
	for _, b := range s.runButtons {
		if running {
			b.Disable()
		} else {
			b.Enable()
		}
	}
	if running {
		s.resetBtn.Disable()
	} else {
		s.resetBtn.Enable()
	}
}

func (s *state) readConfig() (core.Config, string, string, string, error) {
	maxChars, err := strconv.Atoi(strings.TrimSpace(s.maxChars.Text))
	if err != nil || maxChars < 1 {
		return core.Config{}, "", "", "", fmt.Errorf("max chars must be a positive integer")
	}
	workers, err := strconv.Atoi(strings.TrimSpace(s.workers.Text))
	if err != nil || workers < 1 {
		workers = 1
	}
	if workers > 100 {
		workers = 100
	}

	cfg := core.Config{
		Host:     strings.TrimSpace(s.host.Text),
		Model:    strings.TrimSpace(s.model.Text),
		MaxChars: maxChars,
		Workers:  workers,
	}
	return cfg, strings.TrimSpace(s.src.Text), strings.TrimSpace(s.out.Text), strings.TrimSpace(s.dbEntry.Text), nil
}

// useDB switches to the database at path if needed.
func (s *state) useDB(path string) error {
	if path == "" {
		path = "notes.db"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil && path == s.dbPath {
		return nil
	}
	db, err := core.OpenDB(path)
	if err != nil {
		return err
	}
	if s.db != nil {
		_ = s.db.Close()
	}
	s.db = db
	s.dbPath = path
	return nil
}

func (s *state) startPhase(phase core.Phase) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	cfg, src, out, dbPath, err := s.readConfig()
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	if err := s.useDB(dbPath); err != nil {
		dialog.ShowError(err, s.win)
		return
	}

	s.mu.Lock()
	s.running = true
	s.total, s.done, s.ran = 0, 0, 0
	s.runTags, s.runLinks = 0, 0
	s.logs = nil
	db := s.db
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()

	s.setRunning(true)
	s.phaseLabel.SetText("Starting…")
	s.currentLabel.SetText("")
	s.runLabel.SetText("")
	s.logLabel.SetText("")
	s.progress.SetValue(0)
	s.refreshStats()

	go func() {
		defer cancel()
		err := core.Run(ctx, &guiReporter{s: s}, db, cfg, phase, src, out)

		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.mu.Unlock()

		fyne.Do(func() {
			s.setRunning(false)
			s.currentLabel.SetText("")
			s.refreshStats()
			if err != nil {
				s.phaseLabel.SetText("Stopped with error")
				dialog.ShowError(err, s.win)
			} else {
				s.phaseLabel.SetText("Done")
			}
		})
	}()
}

func (s *state) confirmReset() {
	path := strings.TrimSpace(s.dbEntry.Text)
	if path == "" {
		path = "notes.db"
	}
	message := "Delete database " + path + " and all data?"
	dialog.ShowConfirm("Reset database", message, func(ok bool) {
		if !ok {
			return
		}
		s.mu.Lock()
		db := s.db
		s.mu.Unlock()

		newDB, err := core.ResetDB(db, path)
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.mu.Lock()
		s.db = newDB
		s.dbPath = path
		s.mu.Unlock()

		s.logs = nil
		s.logLabel.SetText("")
		s.phaseLabel.SetText("Ready")
		s.currentLabel.SetText("")
		s.runLabel.SetText("")
		s.progress.SetValue(0)
		s.refreshStats()
	}, s.win)
}

func (s *state) refreshStats() {
	s.mu.Lock()
	db := s.db
	s.mu.Unlock()
	if db == nil {
		return
	}
	st := core.LoadStats(db)
	pairs := st.Notes * (st.Notes - 1) / 2
	s.statsLabel.SetText(fmt.Sprintf(
		"Notes: %d  |  With tags: %d  |  Tags: %d  |  Links: %d  |  Pairs: %d/%d  |  Written: %d",
		st.Notes, st.Tagged, st.UniqTags, st.Links, st.PairsDone, pairs, st.Written,
	))
}

// guiReporter translates core progress events into Fyne UI updates.
type guiReporter struct {
	s *state
}

func (r *guiReporter) SetPhase(name string, done, total int) {
	r.s.mu.Lock()
	r.s.total, r.s.done, r.s.ran = total, done, 0
	r.s.runTags, r.s.runLinks = 0, 0
	r.s.mu.Unlock()

	fyne.Do(func() {
		r.s.phaseLabel.SetText(name)
		r.s.currentLabel.SetText("")
		r.s.runLabel.SetText("")
		r.s.progress.SetValue(ratio(done, total))
		r.s.refreshStats()
	})
}

func (r *guiReporter) SetCurrent(current string) {
	fyne.Do(func() {
		r.s.currentLabel.SetText("Now: " + current)
	})
}

func (r *guiReporter) AddRun(tags, links int) {
	r.s.mu.Lock()
	r.s.runTags += tags
	r.s.runLinks += links
	ran, runTags, runLinks := r.s.ran, r.s.runTags, r.s.runLinks
	r.s.mu.Unlock()

	fyne.Do(func() {
		r.s.runLabel.SetText(fmt.Sprintf("This run: %d processed, +%d tags, +%d links", ran, runTags, runLinks))
	})
}

func (r *guiReporter) Advance(skipped bool) {
	r.s.mu.Lock()
	r.s.done++
	if !skipped {
		r.s.ran++
	}
	done, total, ran := r.s.done, r.s.total, r.s.ran
	runTags, runLinks := r.s.runTags, r.s.runLinks
	r.s.mu.Unlock()

	fyne.Do(func() {
		r.s.progress.SetValue(ratio(done, total))
		r.s.runLabel.SetText(fmt.Sprintf("This run: %d processed, +%d tags, +%d links", ran, runTags, runLinks))
	})
}

func (r *guiReporter) Logf(format string, a ...any) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, a...)

	r.s.mu.Lock()
	r.s.logs = append(r.s.logs, line)
	if len(r.s.logs) > maxLogLines {
		r.s.logs = r.s.logs[len(r.s.logs)-maxLogLines:]
	}
	text := strings.Join(r.s.logs, "\n")
	r.s.mu.Unlock()

	fyne.Do(func() {
		r.s.logLabel.SetText(text)
	})
}

func (r *guiReporter) Fail(format string, a ...any) {
	r.Logf("error: "+format, a...)
}

func ratio(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	if done > total {
		done = total
	}
	return float64(done) / float64(total)
}
