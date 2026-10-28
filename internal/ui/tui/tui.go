// Package tui implements the Bubble Tea terminal user interface. It only
// handles presentation and user interaction; all work is done by internal/core.
package tui

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"noesis/internal/core"
	"noesis/internal/ui/consoleShared"
)

type screen int

const (
	scrMenu screen = iota
	scrImport
	scrFormats
	scrData
	scrPickData
	scrComparePath
	scrSettings
	scrModelPicker
	scrRunning
	scrFilePicker
	scrEval
	scrProcessList
	scrProcessDetail
	scrFsSearch
	scrExportTemplates
	scrExportTemplateDetail
	scrBrowse
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
type removeMsg struct {
	removed, scrubbed int
	err               error
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

	textInput        textinput.Model
	textArea         textarea.Model
	editingField     string
	editingMultiline bool

	phaseErr error

	confirmReset bool
	resetting    bool
	resetErr     string

	fileNotes   []core.NoteInfo
	evalResult  string
	evalErr     string
	evalLoading bool
	fileFilter  string

	// where to return after the eval/compare result screen
	evalReturn       screen
	evalReturnCursor int

	// import submenu
	importCursor  int
	importSource  core.ImportSource
	importFormats core.FormatSet
	fmtCursor     int

	// work-with-data submenu
	dataCursor int
	pickMode   int // 0 remove, 1 remove+links
	pickResult string
	pickErr    string

	pickConfirm   bool
	pickConfirmID int64

	// database browser
	browseNotes      []core.NoteDetail
	browseFilter     string
	browseCols       [5]bool // path, format, imported, tags, links
	browseConfirm    bool
	browseConfirmID  int64
	browseResult     string
	browseErr        string
	removeFromBrowse bool

	comparePath string

	promptViewing string
	promptText    string

	// process submenu
	processType          string   // "pair" or "single"
	processList          []string // processes of current type
	processName          string   // selected process for detail view
	processConfirmDelete bool

	// filesystem search for compare file
	fsFiles  []string // matching filesystem files
	fsFilter string   // current search filter

	// export templates
	templateList          []string
	templateName          string
	templateText          string
	templateConfirmDelete bool

	width  int
	height int
}

func newModel(db *sql.DB) *model {
	ti := textinput.New()
	ti.Prompt = ""

	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false

	cfg := core.DefaultConfig()
	formats := make(core.FormatSet)
	for _, e := range core.DefaultImportFormats() {
		formats[e] = true
	}
	return &model{
		db:            db,
		screen:        scrMenu,
		src:           ".",
		out:           "out",
		dbPath:        "notes.db",
		modelName:     cfg.Model,
		host:          cfg.Host,
		maxChars:      cfg.MaxChars,
		workers:       cfg.Workers,
		textInput:     ti,
		textArea:      ta,
		importSource:  core.SourceFilesystem,
		importFormats: formats,
		browseCols:    [5]bool{true, true, false, true, true},
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

func runPhaseCmd(m *model, phase core.Phase, promptName, templateName string) tea.Cmd {
	src, out, modelName, host, maxChars, workers := m.src, m.out, m.modelName, m.host, m.maxChars, m.workers
	return func() tea.Msg {
		m.program.ReleaseTerminal()
		defer m.program.RestoreTerminal()

		cfg := core.Config{Host: host, Model: modelName, MaxChars: maxChars, Workers: workers, PromptName: promptName, TemplateName: templateName}
		progress := consoleshared.New(m.db)
		progress.Start()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		err := core.Run(ctx, progress, m.db, cfg, phase, src, out)
		progress.Stop()
		return phaseMsg{err}
	}
}

func runImportCmd(m *model) tea.Cmd {
	src := m.src
	opts := core.ImportOptions{Source: m.importSource, Formats: m.importFormats}
	return func() tea.Msg {
		m.program.ReleaseTerminal()
		defer m.program.RestoreTerminal()

		progress := consoleshared.New(m.db)
		progress.Start()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		err := core.Import(ctx, progress, m.db, src, opts)
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

func compareFileCmd(db *sql.DB, path, host, model string, maxChars int) tea.Cmd {
	return func() tea.Msg {
		result, err := core.CompareFile(context.Background(), db, path, host, model, maxChars)
		return evalMsg{result: result, err: err}
	}
}

func removeNoteCmd(db *sql.DB, id int64, mode int) tea.Cmd {
	return func() tea.Msg {
		var removed, scrubbed int
		var err error
		if mode == 0 {
			removed, err = core.RemoveNoteByID(db, id)
		} else {
			removed, scrubbed, err = core.RemoveNoteAndLinksByID(db, id)
		}
		return removeMsg{removed: removed, scrubbed: scrubbed, err: err}
	}
}

// ─── Init / Update ────────────────────────────────────────────────────────────

func (m *model) Init() tea.Cmd {
	return fetchOllamaCmd(m.host)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Track the terminal size even while a text field is being edited.
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height
		return m, nil
	}

	// if we're editing a multi-line value (prompt / export template) — delegate
	// to the textarea. Enter inserts a newline; Ctrl+S saves.
	if m.editingField != "" && m.editingMultiline {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "ctrl+s":
				m.saveField()
				m.editingField = ""
				m.editingMultiline = false
				return m, fetchOllamaCmd(m.host)
			case "esc":
				m.editingField = ""
				m.editingMultiline = false
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.textArea, cmd = m.textArea.Update(msg)
		return m, cmd
	}

	// if we're editing a single-line field — delegate to textinput
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
			m.ollamaModels = nil
			m.ollamaReady = false
		}
		return m, fetchOllamaCmd(m.host)

	case evalMsg:
		m.evalLoading = false
		if msg.err != nil {
			m.evalErr = msg.err.Error()
			m.evalResult = ""
		} else {
			m.evalResult = msg.result
			m.evalErr = ""
		}
		m.screen = scrEval
		return m, nil

	case removeMsg:
		if m.removeFromBrowse {
			m.removeFromBrowse = false
			if msg.err != nil {
				m.browseErr = msg.err.Error()
				m.browseResult = ""
			} else {
				m.browseResult = fmtRemoveResult(msg.removed, msg.scrubbed)
				m.browseErr = ""
			}
			if notes, err := core.ListNotesDetailed(m.db); err == nil {
				m.browseNotes = notes
			}
			if m.cursor >= len(m.filteredBrowse()) && m.cursor > 0 {
				m.cursor--
			}
			m.screen = scrBrowse
			return m, nil
		}
		if msg.err != nil {
			m.pickErr = msg.err.Error()
			m.pickResult = ""
		} else {
			m.pickResult = fmtRemoveResult(msg.removed, msg.scrubbed)
			m.pickErr = ""
		}
		// reload the list so the removed file disappears
		if notes, err := core.ListNotes(m.db); err == nil {
			m.fileNotes = notes
		}
		m.screen = scrPickData
		return m, nil

	case fsSearchMsg:
		m.fsFiles = msg.files
		return m, nil
	}
	return m, nil
}

func fmtRemoveResult(removed, scrubbed int) string {
	if scrubbed > 0 {
		return fmt.Sprintf("Removed %d file(s), scrubbed links from %d other file(s)", removed, scrubbed)
	}
	return fmt.Sprintf("Removed %d file(s)", removed)
}
