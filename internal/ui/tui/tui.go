// Package tui implements the Bubble Tea terminal user interface. It only
// handles presentation and user interaction; all work is done by internal/core.
package tui

import (
	"context"
	"database/sql"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"noesis/internal/core"
	"noesis/internal/ui/consoleShared"
)

type screen int

const (
	scrMenu screen = iota
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

type model struct {
	db      *sql.DB
	program *tea.Program
	screen  screen
	cursor  int

	src, out, dbPath, modelName, host string
	maxChars, workers                 int

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

	fileNotes   []core.NoteInfo
	evalResult  string
	evalErr     string
	evalLoading bool
	fileFilter  string

	promptList    []string
	promptViewing string
	promptText    string

	width int
}

func newModel(db *sql.DB) *model {
	ti := textinput.New()
	ti.Prompt = ""

	cfg := core.DefaultConfig()
	return &model{
		db:        db,
		screen:    scrMenu,
		src:       ".",
		out:       "out",
		dbPath:    "notes.db",
		modelName: cfg.Model,
		host:      cfg.Host,
		maxChars:  cfg.MaxChars,
		workers:   cfg.Workers,
		textInput: ti,
	}
}

// Run starts the TUI event loop.
func Run(db *sql.DB) error {
	m := newModel(db)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.program = p
	_, err := p.Run()
	return err
}

// ─── commands ─────────────────────────────────────────────────────────────────

func fetchOllamaCmd(host string) tea.Cmd {
	return func() tea.Msg {
		models, err := core.FetchModels(host)
		return ollamaMsg{models, err}
	}
}

func runPhaseCmd(m *model) tea.Cmd {
	phases := [...]core.Phase{core.PhaseImport, core.PhaseAnalyze, core.PhaseWrite, core.PhaseAll}
	phase := phases[m.cursor]
	src, out, modelName, host, maxChars, workers := m.src, m.out, m.modelName, m.host, m.maxChars, m.workers
	return func() tea.Msg {
		m.program.ReleaseTerminal()
		defer m.program.RestoreTerminal()

		cfg := core.Config{Host: host, Model: modelName, MaxChars: maxChars, Workers: workers}
		progress := consoleshared.New(m.db)
		progress.Start()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		err := core.Run(ctx, progress, m.db, cfg, phase, src, out)
		progress.Stop()
		return phaseMsg{err}
	}
}

func resetDBCmd(db *sql.DB, path string) tea.Cmd {
	return func() tea.Msg {
		newDB, err := core.ResetDB(db, path)
		return resetMsg{err, newDB}
	}
}

func evalNoteCmd(db *sql.DB, noteID int64, host, model string, maxChars int, linkedIDs []int64) tea.Cmd {
	return func() tea.Msg {
		result, err := core.EvalNote(context.Background(), db, noteID, host, model, maxChars, linkedIDs)
		return evalMsg{result: result, err: err}
	}
}

// ─── Init / Update ────────────────────────────────────────────────────────────

func (m *model) Init() tea.Cmd {
	return fetchOllamaCmd(m.host)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
