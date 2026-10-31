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
	scrSync
	scrFormats
	scrData
	scrPickData
	scrComparePath
	scrSettings
	scrModelPicker
	scrOptionPicker
	scrWhisper
	scrAudioHelp
	scrRunning
	scrFilePicker
	scrEval
	scrProcessList
	scrProcessDetail
	scrFsSearch
	scrExportTemplates
	scrExportTemplateDetail
	scrBrowse
	scrBrowseDetail
	scrExtract
	scrRAGSearch
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
type clearRAGMsg struct {
	notes, chunks int
	err           error
}
type evalMsg struct {
	result string
	err    error
}
type removeMsg struct {
	removed, scrubbed int
	err               error
}
type ragMsg struct {
	results []core.RAGResult
	answer  string
	err     error
}

// ragModelsMsg reports which ollama models needed for a search are missing.
type ragModelsMsg struct {
	missing []string
	ask     bool
	err     error
}

// ragPulledMsg reports the result of downloading the missing models.
type ragPulledMsg struct{ err error }

type model struct {
	db      *sql.DB
	program *tea.Program
	screen  screen
	cursor  int

	src, out, dbPath, modelName, host, transcribeModel     string
	visionModel                                            string
	embeddingModel                                         string
	audioEngine, whisperModel, googleAPIKey, binaryStorage string
	speechLanguage                                         string
	whisperBackend                                         string
	whisperDevice                                          int
	maxChars, workers                                      int
	skipDirs                                               string

	ollamaModels []string
	ollamaOK     bool
	ollamaErr    string
	ollamaReady  bool

	textInput        textinput.Model
	textArea         textarea.Model
	editingField     string
	editingMultiline bool

	// generic settings picker (audio engine / speech language)
	pickerKind   string
	pickerValues []string
	pickerLabels []string
	pickerCursor int

	// which settings row the ollama model picker writes to ("model" or
	// "embedding")
	modelPickerTarget string

	// whisper.cpp model manager
	whisperModels []core.WhisperModelInfo
	whisperCursor int
	whisperStatus string
	whisperBusy   bool
	whisperStage  string
	whisperDone   int64
	whisperTotal  int64
	whisperErr    string

	phaseErr error

	confirmReset bool
	resetting    bool
	resetErr     string

	confirmClearRAG bool
	clearingRAG     bool
	clearRAGErr     string
	clearRAGStatus  string

	fileNotes   []core.NoteInfo
	evalResult  string
	evalErr     string
	evalLoading bool
	fileFilter  string

	// where to return after the eval/compare result screen
	evalReturn       screen
	evalReturnCursor int

	// where to return when leaving the note picker (eval entry point)
	pickerBack       screen
	pickerBackCursor int

	// import submenu
	importCursor  int
	importSource  core.ImportSource
	importFormats core.FormatSet
	fmtCursor     int

	// sync
	syncSource  core.ImportSource // source type for sync: folder, zip, json
	syncCursor  int               // cursor for the sync settings submenu (0=source, 1=formats, 2=action)
	syncItems   []core.SyncItem
	syncScroll  int
	syncChecked map[string]bool // key: "del:<path>" or "new:<path>"
	dataCursor  int
	pickMode    int // 0 remove, 1 remove+links
	pickResult  string
	pickErr     string

	// re-extract picker
	extractFilter  string
	extractFormats core.FormatSet
	extractCursor  int
	extractTyping  bool

	// semantic (RAG) search
	ragQuery         string
	ragResults       []core.RAGResult
	ragAnswer        string
	ragErr           string
	ragBusy          bool
	ragTyping        bool
	ragAsk           bool
	ragTopK          int
	ragAnswerOffset  int
	ragConfirm       bool
	ragConfirmModels []string

	pickConfirm   bool
	pickConfirmID int64

	// database browser
	browseNotes       []core.NoteDetail
	browseFilter      string
	browseCols        [5]bool // path, format, imported, tags, links
	browseConfirm     bool
	browseConfirmID   int64
	browseBulkConfirm bool
	browseSelected    map[int64]bool
	browseResult      string
	browseErr         string
	removeFromBrowse  bool

	browseDetail        []core.NoteField
	browseDetailID      int64
	browseDetailPath    string
	browseDetailConfirm bool
	browseDetailOffset  int
	removeFromDetail    bool
	// browseBack is the screen ESC returns to from the note detail view.
	browseBack screen

	// filterTyping is true while a filter/search field is capturing text.
	// Outside that mode h/j/k/l work as Vim navigation keys.
	filterTyping bool

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

	formats := make(core.FormatSet)
	for _, e := range core.DefaultImportFormats() {
		formats[e] = true
	}
	st := core.LoadAppSettings(db)
	if saved, ok := core.DecodeFormatSelection(st.Formats); ok {
		formats = saved
	}
	return &model{
		db:              db,
		screen:          scrMenu,
		src:             st.Src,
		out:             st.Out,
		dbPath:          "notes.db",
		modelName:       st.Model,
		host:            st.Host,
		transcribeModel: st.TranscribeModel,
		visionModel:     st.VisionModel,
		embeddingModel:  st.EmbeddingModel,
		audioEngine:     st.AudioEngine,
		whisperModel:    st.WhisperModel,
		whisperBackend:  st.WhisperBackend,
		whisperDevice:   st.WhisperDevice,
		googleAPIKey:    st.GoogleAPIKey,
		binaryStorage:   st.BinaryStorage,
		speechLanguage:  st.SpeechLanguage,
		maxChars:        st.MaxChars,
		workers:         st.Workers,
		skipDirs:        st.SkipDirs,
		textInput:       ti,
		textArea:        ta,
		importSource:    core.SourceFilesystem,
		importFormats:   formats,
		syncSource:      core.SourceFilesystem,
		browseCols:      [5]bool{true, true, false, true, true},
		browseBack:      scrBrowse,
		browseSelected:  map[int64]bool{},
		ragTopK:         core.NormalizeRAGTopK(st.RAGTopK),
	}
}

// persistSettings stores non-default UI settings in the current database so
// they are restored on the next launch.
func (m *model) persistSettings() {
	if m.db == nil {
		return
	}
	_ = core.AppSettings{
		Src:             m.src,
		Out:             m.out,
		Host:            m.host,
		Model:           m.modelName,
		VisionModel:     m.visionModel,
		EmbeddingModel:  m.embeddingModel,
		TranscribeModel: m.transcribeModel,
		AudioEngine:     m.audioEngine,
		WhisperModel:    m.whisperModel,
		WhisperBackend:  m.whisperBackend,
		WhisperDevice:   m.whisperDevice,
		GoogleAPIKey:    m.googleAPIKey,
		BinaryStorage:   m.binaryStorage,
		SpeechLanguage:  m.speechLanguage,
		MaxChars:        m.maxChars,
		Workers:         m.workers,
		SkipDirs:        m.skipDirs,
		RAGTopK:         m.ragTopK,
		Formats:         core.EncodeFormatSelection(m.importFormats),
	}.Save(m.db)
}

// audioOptions builds the selected audio backend configuration.
func (m *model) audioOptions() core.AudioOptions {
	return core.AudioOptions{
		Engine:         m.audioEngine,
		WhisperModel:   m.whisperModel,
		WhisperBackend: m.whisperBackend,
		WhisperDevice:  m.whisperDevice,
		GoogleAPIKey:   m.googleAPIKey,
		SpeechLanguage: m.speechLanguage,
		OllamaHost:     m.host,
		OllamaModel:    m.transcribeModel,
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
	src, out, modelName, host, transcribeModel := m.src, m.out, m.modelName, m.host, m.transcribeModel
	visionModel, embeddingModel := m.visionModel, m.embeddingModel
	extractFilter, extractFormats := m.extractFilter, m.extractFormats
	audioEngine, whisperModel, googleAPIKey, speechLanguage := m.audioEngine, m.whisperModel, m.googleAPIKey, m.speechLanguage
	whisperBackend, whisperDevice := m.whisperBackend, m.whisperDevice
	maxChars, workers := m.maxChars, m.workers
	skipDirs := core.SkipDirPatterns(m.skipDirs)
	return func() tea.Msg {
		m.program.ReleaseTerminal()
		defer m.program.RestoreTerminal()

		cfg := core.Config{
			Host:            host,
			Model:           modelName,
			VisionModel:     visionModel,
			EmbeddingModel:  embeddingModel,
			TranscribeModel: transcribeModel,
			AudioEngine:     audioEngine,
			WhisperModel:    whisperModel,
			WhisperBackend:  whisperBackend,
			WhisperDevice:   whisperDevice,
			BinaryStorage:   m.binaryStorage,
			GoogleAPIKey:    googleAPIKey,
			SpeechLanguage:  speechLanguage,
			MaxChars:        maxChars,
			Workers:         workers,
			PromptName:      promptName,
			TemplateName:    templateName,
			ExtractName:     extractFilter,
			ExtractFormats:  extractFormats,
			SkipDirs:        skipDirs,
		}
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
	opts := core.ImportOptions{
		Source:        m.importSource,
		Formats:       m.importFormats,
		BinaryStorage: m.binaryStorage,
		SkipDirs:      core.SkipDirPatterns(m.skipDirs),
	}
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

func clearRAGCmd(db *sql.DB) tea.Cmd {
	return func() tea.Msg {
		notes, chunks, err := core.ClearRAGIndex(db)
		return clearRAGMsg{notes: notes, chunks: chunks, err: err}
	}
}

func evalNoteCmd(db *sql.DB, noteID int64, host, model string, maxChars int, linkedIDs []int64) tea.Cmd {
	return func() tea.Msg {
		result, err := core.EvalNote(context.Background(), db, noteID, host, model, maxChars, linkedIDs)
		return evalMsg{result: result, err: err}
	}
}

func compareFileCmd(db *sql.DB, path, host, model string, audio core.AudioOptions, maxChars int) tea.Cmd {
	return func() tea.Msg {
		result, err := core.CompareFileWithAudio(context.Background(), db, path, host, model, audio, maxChars)
		return evalMsg{result: result, err: err}
	}
}

// ragConfig builds the minimal config needed for embedding/search.
func (m *model) ragConfig() core.Config {
	return core.Config{
		Host:           m.host,
		Model:          m.modelName,
		EmbeddingModel: m.embeddingModel,
		MaxChars:       m.maxChars,
		Workers:        m.workers,
	}
}

func ragSearchCmd(db *sql.DB, c core.Config, query string, topK int, ask bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if ask {
			answer, results, err := core.RAGAnswer(ctx, db, c, query, topK)
			return ragMsg{results: results, answer: answer, err: err}
		}
		results, err := core.RAGSearch(ctx, db, c, query, topK)
		return ragMsg{results: results, err: err}
	}
}

// ragPreflightCmd checks which ollama models a search needs before running it,
// so the TUI can ask the user to download any that are missing.
func ragPreflightCmd(c core.Config, ask bool) tea.Cmd {
	return func() tea.Msg {
		models, err := core.FetchModels(c.Host)
		if err != nil {
			return ragModelsMsg{err: fmt.Errorf("ollama unavailable at %s: %w", c.Host, err)}
		}
		var missing []string
		if emb := core.NormalizeEmbeddingModel(c.EmbeddingModel); !core.ModelInstalled(models, emb) {
			missing = append(missing, emb)
		}
		if ask && !core.ModelInstalled(models, c.Model) {
			missing = append(missing, c.Model)
		}
		return ragModelsMsg{missing: missing, ask: ask}
	}
}

// ragPullCmd downloads the missing models on the terminal (leaving the TUI's
// alternate screen), then reports back so the search can start.
func ragPullCmd(m *model, models []string) tea.Cmd {
	c := m.ragConfig()
	return func() tea.Msg {
		m.program.ReleaseTerminal()
		defer m.program.RestoreTerminal()

		progress := consoleshared.New(m.db)
		progress.AssumeYes = true // already approved with the y/N prompt
		progress.Start()
		defer progress.Stop()

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		for _, model := range models {
			if err := core.EnsureOllamaModel(ctx, progress, c.Host, model); err != nil {
				return ragPulledMsg{err: err}
			}
		}
		return ragPulledMsg{}
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

// removeNotesCmd deletes several records at once (bulk removal from the
// browser). mode 0 keeps links in other notes, mode 1 scrubs them.
func removeNotesCmd(db *sql.DB, ids []int64, mode int) tea.Cmd {
	return func() tea.Msg {
		var removed, scrubbed int
		var err error
		if mode == 0 {
			removed, err = core.RemoveNotesByIDs(db, ids)
		} else {
			removed, scrubbed, err = core.RemoveNotesAndLinksByIDs(db, ids)
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
				m.textInput.EchoMode = textinput.EchoNormal
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

	case whisperProgressMsg:
		m.whisperStage = msg.stage
		m.whisperDone = msg.done
		m.whisperTotal = msg.total
		return m, nil

	case whisperDoneMsg:
		m.whisperBusy = false
		m.whisperDone, m.whisperTotal = 0, 0
		if msg.err != nil {
			m.whisperErr = msg.err.Error()
			m.whisperStatus = ""
		} else {
			m.whisperErr = ""
			m.refreshWhisperModels()
			if msg.model == "" {
				m.whisperStatus = "whisper.cpp runtime installed."
			} else {
				m.whisperModel = core.NormalizeWhisperModel(msg.model)
				m.persistSettings()
				if core.WhisperRuntimeInstalledFor(m.whisperBackend) {
					m.whisperStatus = fmt.Sprintf("%s is downloaded and selected (%s).", m.whisperModel, core.WhisperBackendLabel(m.whisperBackend))
				} else {
					m.whisperStatus = fmt.Sprintf("%s is downloaded. Install whisper-cli to use it — see the instructions above.", m.whisperModel)
				}
			}
		}
		return m, nil

	case phaseMsg:
		m.phaseErr = msg.err
		m.screen = scrMenu
		return m, nil

	case syncDiffMsg:
		m.syncItems = msg.items
		m.syncCursor = 0
		m.syncChecked = nil
		if msg.err != nil {
			m.phaseErr = msg.err
			m.screen = scrSync
		}
		return m, nil

	case syncApplyMsg:
		m.phaseErr = msg.err
		m.syncItems = nil
		m.syncChecked = nil
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
			m.persistSettings()
		}
		return m, fetchOllamaCmd(m.host)

	case clearRAGMsg:
		m.clearingRAG = false
		if msg.err != nil {
			m.clearRAGErr = msg.err.Error()
			m.clearRAGStatus = ""
		} else {
			m.clearRAGErr = ""
			m.clearRAGStatus = fmt.Sprintf("Cleared %d note(s) / %d chunk(s) from the RAG index.", msg.notes, msg.chunks)
		}
		return m, nil

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

	case ragMsg:
		m.ragBusy = false
		m.ragAnswerOffset = 0
		if msg.err != nil {
			m.ragErr = msg.err.Error()
			m.ragResults = nil
			m.ragAnswer = ""
		} else {
			m.ragErr = ""
			m.ragResults = msg.results
			m.ragAnswer = msg.answer
		}
		return m, nil

	case ragModelsMsg:
		if msg.err != nil {
			m.ragBusy = false
			m.ragErr = msg.err.Error()
			return m, nil
		}
		if len(msg.missing) == 0 {
			return m, ragSearchCmd(m.db, m.ragConfig(), m.ragQuery, m.ragTopK, msg.ask)
		}
		m.ragBusy = false
		m.ragConfirm = true
		m.ragConfirmModels = msg.missing
		return m, nil

	case ragPulledMsg:
		if msg.err != nil {
			m.ragBusy = false
			m.ragErr = msg.err.Error()
			return m, nil
		}
		return m, tea.Batch(
			ragSearchCmd(m.db, m.ragConfig(), m.ragQuery, m.ragTopK, m.ragAsk),
			fetchOllamaCmd(m.host),
		)

	case removeMsg:
		if m.removeFromDetail {
			m.removeFromDetail = false
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
			m.browseDetail = nil
			m.browseDetailID = 0
			m.browseDetailPath = ""
			m.screen = scrBrowse
			return m, nil
		}
		if m.removeFromBrowse {
			m.removeFromBrowse = false
			m.browseSelected = map[int64]bool{}
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
