// Package gui implements the Fyne desktop frontend. It only drives
// internal/core; the actual work is UI-independent.
package gui

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"noesis/internal/core"
)

const maxLogLines = 200

type screen int

const (
	scrMenu screen = iota
	scrImport
	scrSync
	scrFormats
	scrData
	scrNotes
	scrFsSearch
	scrEval
	scrSettings
	scrWhisper
	scrPromptView
	scrProcessList
	scrProcessDetail
	scrRunning
	scrExportTemplates
	scrExportTemplateDetail
	scrBrowse
	scrBrowseDetail
	scrExtract
	scrRAGSearch
)

type noteAction int

const (
	noteRemove noteAction = iota
	noteRemoveLinks
	noteEval
)

// menuItems is the main menu shown on the start screen.
var menuItems = []string{
	"Synchronize",
	"Data processing — pairs",
	"Data processing — one by one",
	"Export for Obsidian",
	"Work with data",
	"Settings",
}

var importSourceNames = []string{"filesystem", "zip archive", "json array"}

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

	// persistent settings widgets (shared by the settings and import screens)
	src, out, dbEntry, model, host, transcribe *widget.Entry
	vision                                     *widget.Entry
	embedding                                  *widget.Entry
	maxChars, workers                          *widget.Entry
	audioEngine                                *widget.Select
	whisperModel                               *widget.Select
	whisperBackend                             *widget.Select
	whisperDevice                              *widget.Entry
	googleKey                                  *widget.Entry
	speechLanguage                             *widget.Select
	formatsLabel                               *widget.Label

	phaseLabel   *widget.Label
	currentLabel *widget.Label
	runLabel     *widget.Label
	statsLabel   *widget.Label
	logLabel     *widget.Label
	progress     *widget.ProgressBar
	statusPanel  fyne.CanvasObject

	// navigation
	screen     screen
	noteBack   screen
	fsBack     screen
	evalBack   screen
	promptBack screen

	// whisper.cpp model manager
	whisperModels   []core.WhisperModelInfo
	whisperBusy     bool
	whisperProgress float64
	whisperStage    string
	whisperStatus   string
	whisperErr      string

	// import
	importSource  core.ImportSource
	importFormats core.FormatSet

	// sync
	syncItems   []core.SyncItem
	syncChecked map[string]bool

	// re-extract picker
	extractFilter  string
	extractFormats core.FormatSet

	// notes (remove / evaluate)
	noteAction noteAction
	noteFilter string
	notes      []core.NoteInfo

	// database browser
	browseNotes  []core.NoteDetail
	browseFilter string
	browseCols   map[string]bool
	browseDetail []core.NoteField
	browseNoteID int64
	browsePath   string
	browseBack   screen // where "Back" from the record view returns to

	// semantic (RAG) search
	ragQuery   string
	ragResults []core.RAGResult
	ragAnswer  string
	ragErr     string
	ragBusy    bool
	ragTopK    int

	// filesystem search for compare
	fsFilter string
	fsFiles  []string

	// processes / prompts
	processType string
	processName string
	promptName  string
	promptText  string
	promptEdit  bool

	// export templates
	exportTemplates []string
	templateName    string
	templateText    string
	templateEdit    bool

	// novelty result
	evalResult  string
	evalErr     string
	evalLoading bool

	// ollama connection status shown on the start screen and in settings
	ollamaStatus string
	ollamaModels []string
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
	s := &state{db: db, dbPath: dbPath}
	s.app = app.NewWithID("com.noesis.noesis")
	s.win = s.app.NewWindow("noesis")

	s.src = widget.NewEntry()
	s.out = widget.NewEntry()
	s.dbEntry = widget.NewEntry()
	s.dbEntry.SetText(dbPath)
	s.host = widget.NewEntry()
	s.model = widget.NewEntry()
	s.vision = widget.NewEntry()
	s.embedding = widget.NewEntry()
	s.transcribe = widget.NewEntry()
	s.maxChars = widget.NewEntry()
	s.workers = widget.NewEntry()
	st := core.LoadAppSettings(db)
	s.audioEngine = widget.NewSelect(core.AudioEngineLabels(), nil)
	s.whisperModel = widget.NewSelect(core.WhisperModelNames(), nil)
	s.whisperBackend = widget.NewSelect(core.WhisperBackendLabels(), nil)
	s.whisperDevice = widget.NewEntry()
	s.googleKey = widget.NewEntry()
	s.googleKey.Password = true
	s.speechLanguage = widget.NewSelect(core.SpeechLanguageLabels(), nil)
	s.src.SetText(st.Src)
	s.out.SetText(st.Out)
	s.host.SetText(st.Host)
	s.model.SetText(st.Model)
	s.vision.SetText(st.VisionModel)
	s.embedding.SetText(core.NormalizeEmbeddingModel(st.EmbeddingModel))
	s.transcribe.SetText(st.TranscribeModel)
	s.maxChars.SetText(strconv.Itoa(st.MaxChars))
	s.workers.SetText(strconv.Itoa(st.Workers))
	s.audioEngine.SetSelected(core.AudioEngineLabel(st.AudioEngine))
	s.whisperModel.SetSelected(core.NormalizeWhisperModel(st.WhisperModel))
	s.whisperBackend.SetSelected(core.WhisperBackendLabel(st.WhisperBackend))
	s.whisperDevice.SetText(strconv.Itoa(st.WhisperDevice))
	s.googleKey.SetText(st.GoogleAPIKey)
	s.speechLanguage.SetSelected(core.SpeechLanguageLabel(st.SpeechLanguage))
	s.audioEngine.OnChanged = func(string) { s.persistSettings() }
	s.whisperModel.OnChanged = func(string) { s.persistSettings() }
	s.whisperBackend.OnChanged = func(string) { s.persistSettings() }
	s.speechLanguage.OnChanged = func(string) { s.persistSettings() }
	for _, e := range []*widget.Entry{s.src, s.out, s.host, s.model, s.vision, s.embedding, s.transcribe, s.maxChars, s.workers, s.googleKey, s.whisperDevice} {
		e.OnSubmitted = func(string) { s.persistSettings() }
	}
	s.browseCols = defaultBrowseCols()
	s.browseBack = scrBrowse
	s.ragTopK = core.NormalizeRAGTopK(st.RAGTopK)
	s.ollamaStatus = "checking ollama…"

	s.importSource = core.SourceFilesystem
	s.importFormats = make(core.FormatSet)
	for _, e := range core.DefaultImportFormats() {
		s.importFormats[e] = true
	}
	if saved, ok := core.DecodeFormatSelection(st.Formats); ok {
		s.importFormats = saved
	}
	s.formatsLabel = widget.NewLabel("")
	s.formatsLabel.Wrapping = fyne.TextWrapWord

	s.phaseLabel = widget.NewLabel("Ready")
	s.currentLabel = widget.NewLabel("")
	s.runLabel = widget.NewLabel("")
	s.statsLabel = widget.NewLabel("")
	s.progress = widget.NewProgressBar()

	s.logLabel = widget.NewLabel("")
	s.logLabel.Wrapping = fyne.TextWrapWord
	s.logLabel.TextStyle = fyne.TextStyle{Monospace: true}
	s.logLabel.Selectable = true

	logs := container.NewVScroll(s.logLabel)
	logs.SetMinSize(fyne.NewSize(720, 130))
	s.statusPanel = container.NewVBox(
		s.phaseLabel,
		s.progress,
		s.currentLabel,
		s.runLabel,
		s.statsLabel,
		logs,
	)

	s.win.Resize(fyne.NewSize(1024, 860))
	s.win.CenterOnScreen()
	s.win.SetCloseIntercept(s.close)
	s.refreshStats()
	s.render()

	go func() {
		models, err := core.FetchModels(st.Host)
		fyne.Do(func() {
			switch {
			case err != nil:
				s.ollamaStatus = "ollama ✗ " + err.Error()
			case len(models) == 0:
				s.ollamaStatus = "ollama ✓ — no models found"
			default:
				s.ollamaModels = models
				s.ollamaStatus = "ollama ✓ " + strings.Join(models, ", ")
			}
			s.render()
		})
	}()

	return s
}

// ─── navigation ─────────────────────────────────────────────────────────────

func (s *state) render() {
	if s.win == nil {
		return
	}
	header := container.NewHBox()
	if s.screen != scrMenu {
		header.Add(widget.NewButton("← Back", s.goBack))
	}
	header.Add(widget.NewLabelWithStyle(s.screenTitle(), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))

	s.win.SetContent(container.NewBorder(header, s.statusPanel, nil, nil, s.body()))
}

func (s *state) screenTitle() string {
	switch s.screen {
	case scrImport:
		return "Synchronize"
	case scrSync:
		return "Synchronize with folder"
	case scrFormats:
		return "Select formats to import"
	case scrData:
		return "Work with data"
	case scrNotes:
		switch s.noteAction {
		case noteEval:
			return "Evaluate note uniqueness"
		case noteRemoveLinks:
			return "Remove file + Obsidian links — search by path"
		default:
			return "Remove file — search by path"
		}
	case scrFsSearch:
		return "Compare file — search filesystem"
	case scrEval:
		return "Novelty"
	case scrSettings:
		return "Settings"
	case scrWhisper:
		return "Whisper models"
	case scrPromptView:
		if s.promptEdit {
			return "Edit prompt: " + s.promptName
		}
		return "Prompt: " + s.promptName
	case scrProcessList:
		if s.processType == "single" {
			return "One-by-one processes"
		}
		return "Pair processes"
	case scrProcessDetail:
		return "Process: " + s.processName
	case scrExportTemplates:
		return "Export for Obsidian — choose a template"
	case scrExportTemplateDetail:
		if s.templateEdit {
			return "Edit export template: " + s.templateName
		}
		return "Export template: " + s.templateName
	case scrBrowse:
		return "Browse database"
	case scrBrowseDetail:
		return "Note record: " + s.browsePath
	case scrExtract:
		return "Re-extract documents, images & transcribe audio"
	case scrRAGSearch:
		return "Semantic search (RAG)"
	case scrRunning:
		return "Processing…"
	default:
		return "noesis"
	}
}

func (s *state) goBack() {
	switch s.screen {
	case scrImport, scrSync, scrData, scrSettings, scrProcessList:
		s.noteFilter = ""
		s.screen = scrMenu
	case scrWhisper:
		s.screen = scrSettings
	case scrFormats:
		s.screen = scrSync
	case scrNotes:
		s.screen = s.noteBack
	case scrFsSearch:
		s.screen = s.fsBack
	case scrEval:
		s.screen = s.evalBack
	case scrPromptView:
		s.screen = s.promptBack
	case scrProcessDetail:
		s.openProcessList(s.processType)
		return
	case scrExportTemplates:
		s.screen = scrMenu
	case scrExportTemplateDetail:
		s.openExportTemplates()
		return
	case scrBrowse:
		s.screen = scrData
	case scrBrowseDetail:
		s.screen = s.browseBack
	case scrExtract:
		s.screen = scrData
	case scrRAGSearch:
		s.screen = scrData
	case scrRunning:
		return
	default:
		s.screen = scrMenu
	}
	s.render()
}

func (s *state) body() fyne.CanvasObject {
	switch s.screen {
	case scrImport:
		return s.syncScreen()
	case scrSync:
		return s.syncScreen()
	case scrFormats:
		return s.formatsScreen()
	case scrData:
		return s.dataScreen()
	case scrNotes:
		return s.notesScreen()
	case scrFsSearch:
		return s.fsSearchScreen()
	case scrEval:
		return s.evalScreen()
	case scrSettings:
		return s.settingsScreen()
	case scrWhisper:
		return s.whisperScreen()
	case scrPromptView:
		return s.promptScreen()
	case scrProcessList:
		return s.processListScreen()
	case scrProcessDetail:
		return s.processDetailScreen()
	case scrExportTemplates:
		return s.exportTemplatesScreen()
	case scrExportTemplateDetail:
		return s.exportTemplateDetailScreen()
	case scrBrowse:
		return s.browseScreen()
	case scrBrowseDetail:
		return s.browseDetailScreen()
	case scrExtract:
		return s.extractScreen()
	case scrRAGSearch:
		return s.ragSearchScreen()
	case scrRunning:
		return s.runningScreen()
	default:
		return s.menuScreen()
	}
}

func (s *state) activateMenu(id int) {
	switch id {
	case 0:
		s.screen = scrSync
	case 1:
		s.openProcessList("pair")
		return
	case 2:
		s.openProcessList("single")
		return
	case 3:
		s.openExportTemplates()
		return
	case 4:
		s.screen = scrData
	case 5:
		s.screen = scrSettings
	}
	s.render()
}

// ─── actions ────────────────────────────────────────────────────────────────

func (s *state) openNotes(action noteAction, back screen) {
	db := s.currentDB()
	if db == nil {
		return
	}
	notes, err := core.ListNotes(db)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	s.notes = notes
	s.noteFilter = ""
	s.noteAction = action
	s.noteBack = back
	s.screen = scrNotes
	s.render()
}

func (s *state) reloadNotes() {
	db := s.currentDB()
	if db == nil {
		return
	}
	if notes, err := core.ListNotes(db); err == nil {
		s.notes = notes
	}
}

// openBrowse loads the database contents into the browse screen.
func (s *state) openBrowse() {
	db := s.currentDB()
	if db == nil {
		return
	}
	notes, err := core.ListNotesDetailed(db)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	s.browseNotes = notes
	s.browseFilter = ""
	s.screen = scrBrowse
	s.render()
}

func (s *state) reloadBrowse() {
	db := s.currentDB()
	if db == nil {
		return
	}
	if notes, err := core.ListNotesDetailed(db); err == nil {
		s.browseNotes = notes
	}
}

func (s *state) openBrowseDetail(n core.NoteDetail) {
	db := s.currentDB()
	if db == nil {
		return
	}
	fields, err := core.NoteRecord(db, n.ID)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	s.browseDetail = fields
	s.browseNoteID = n.ID
	s.browsePath = n.FilePath
	s.screen = scrBrowseDetail
	s.render()
}

func (s *state) removeBrowseNote() {
	id := s.browseNoteID
	message := fmt.Sprintf("Remove %q from the database?", s.browsePath)
	dialog.ShowConfirm("Remove from database", message, func(ok bool) {
		if !ok {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		removed, err := core.RemoveNoteByID(db, id)
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.reloadBrowse()
		s.refreshStats()
		s.browseDetail = nil
		s.browseNoteID = 0
		s.browsePath = ""
		s.screen = scrBrowse
		s.phaseLabel.SetText(fmt.Sprintf("Removed %d note(s)", removed))
		s.render()
	}, s.win)
}

func (s *state) pickNote(n core.NoteInfo) {
	if s.noteAction == noteEval {
		s.startEvalNote(n)
		return
	}

	withLinks := s.noteAction == noteRemoveLinks
	label := "Remove file"
	if withLinks {
		label = "Remove file + links"
	}
	message := fmt.Sprintf("Remove %q?", n.Path)
	dialog.ShowConfirm(label, message, func(ok bool) {
		if !ok {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		var err error
		var removed, scrubbed int
		if withLinks {
			removed, scrubbed, err = core.RemoveNoteAndLinksByID(db, n.ID)
		} else {
			removed, err = core.RemoveNoteByID(db, n.ID)
		}
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.reloadNotes()
		s.refreshStats()
		s.phaseLabel.SetText(fmt.Sprintf("Removed %d note(s), scrubbed links in %d other file(s)", removed, scrubbed))
		s.render()
	}, s.win)
}

func (s *state) startEvalNote(n core.NoteInfo) {
	cfg, _, _, _, err := s.readConfig()
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	db := s.currentDB()
	if db == nil {
		return
	}

	s.evalLoading = true
	s.evalResult = ""
	s.evalErr = ""
	s.evalBack = scrNotes
	s.screen = scrEval
	s.render()

	go func() {
		linked, _ := core.LinkedNoteIDs(db, n.ID)
		result, err := core.EvalNote(context.Background(), db, n.ID, cfg.Host, cfg.Model, cfg.MaxChars, linked)
		fyne.Do(func() {
			s.evalLoading = false
			if err != nil {
				s.evalErr = err.Error()
				s.evalResult = ""
			} else {
				s.evalResult = result
				s.evalErr = ""
			}
			s.render()
		})
	}()
}

func (s *state) startCompare(path string) {
	cfg, _, _, _, err := s.readConfig()
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	db := s.currentDB()
	if db == nil {
		return
	}

	s.evalLoading = true
	s.evalResult = ""
	s.evalErr = ""
	s.evalBack = scrFsSearch
	s.screen = scrEval
	s.render()

	go func() {
		audio := core.AudioOptions{
			Engine:         cfg.AudioEngine,
			WhisperModel:   cfg.WhisperModel,
			WhisperBackend: cfg.WhisperBackend,
			WhisperDevice:  cfg.WhisperDevice,
			GoogleAPIKey:   cfg.GoogleAPIKey,
			SpeechLanguage: cfg.SpeechLanguage,
			OllamaHost:     cfg.Host,
			OllamaModel:    cfg.TranscribeModel,
		}
		result, err := core.CompareFileWithAudio(context.Background(), db, path, cfg.Host, cfg.Model, audio, cfg.MaxChars)
		fyne.Do(func() {
			s.evalLoading = false
			if err != nil {
				s.evalErr = err.Error()
				s.evalResult = ""
			} else {
				s.evalResult = result
				s.evalErr = ""
			}
			s.render()
		})
	}()
}

func (s *state) openFsSearch() {
	s.fsFilter = ""
	s.fsFiles = nil
	s.fsBack = scrData
	s.screen = scrFsSearch
	s.render()
}

func (s *state) searchFiles(filter string, done func([]string)) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	go func() {
		files, err := core.FindFiles(cwd, filter)
		fyne.Do(func() {
			if filter != s.fsFilter {
				return
			}
			if err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			done(files)
		})
	}()
}

// openRAGSearch opens the semantic search screen with a clean slate.
func (s *state) openRAGSearch() {
	s.ragQuery = ""
	s.ragResults = nil
	s.ragAnswer = ""
	s.ragErr = ""
	s.ragBusy = false
	s.ragTopK = core.NormalizeRAGTopK(s.ragTopK)
	s.screen = scrRAGSearch
	s.render()
}

// runRAGSearch embeds the query and ranks chunks (and, when ask is true, asks
// the chat model to answer from the retrieved context). Work happens off the UI
// goroutine; results are applied with fyne.Do.
func (s *state) runRAGSearch(ask bool) {
	if s.ragBusy {
		return
	}
	query := strings.TrimSpace(s.ragQuery)
	if query == "" {
		return
	}
	db := s.currentDB()
	if db == nil {
		return
	}
	cfg, _, _, _, err := s.readConfigRaw()
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}

	s.ragBusy = true
	s.ragErr = ""
	s.ragAnswer = ""
	s.render()

	topK := s.ragTopK
	go func() {
		ctx := context.Background()
		r := &guiReporter{s: s}
		fail := func(err error) {
			fyne.Do(func() {
				s.ragBusy = false
				s.ragErr = err.Error()
				s.ragResults = nil
				s.ragAnswer = ""
				s.render()
			})
		}
		if err := core.EnsureOllamaModel(ctx, r, cfg.Host, core.NormalizeEmbeddingModel(cfg.EmbeddingModel)); err != nil {
			fail(err)
			return
		}
		if ask {
			if err := core.EnsureOllamaModel(ctx, r, cfg.Host, cfg.Model); err != nil {
				fail(err)
				return
			}
		}
		var results []core.RAGResult
		var answer string
		var err error
		if ask {
			answer, results, err = core.RAGAnswer(ctx, db, cfg, query, topK)
		} else {
			results, err = core.RAGSearch(ctx, db, cfg, query, topK)
		}
		fyne.Do(func() {
			s.ragBusy = false
			if err != nil {
				s.ragErr = err.Error()
				s.ragResults = nil
				s.ragAnswer = ""
			} else {
				s.ragResults = results
				s.ragAnswer = answer
			}
			s.render()
		})
	}()
}

func (s *state) openProcessList(ptype string) {
	s.processType = ptype
	s.processName = ""
	s.screen = scrProcessList
	s.render()
}

func (s *state) openPrompt(name string, back screen) {
	db := s.currentDB()
	if db == nil {
		return
	}
	s.promptName = name
	s.promptText = core.LoadPrompt(db, name)
	s.promptEdit = false
	s.promptBack = back
	s.screen = scrPromptView
	s.render()
}

func (s *state) addProcess() {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("process name")
	label := "Add pair process"
	if s.processType == "single" {
		label = "Add one-by-one process"
	}
	dialog.ShowCustomConfirm(label, "Create", "Cancel", nameEntry, func(ok bool) {
		if !ok {
			return
		}
		name := strings.TrimSpace(nameEntry.Text)
		if name == "" {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		if err := core.SavePromptWithType(db, name, "", s.processType); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.render()
	}, s.win)
}

func (s *state) deleteProcess() {
	name := s.processName
	dialog.ShowConfirm("Remove process", "Delete process "+name+" and all its data?", func(ok bool) {
		if !ok {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		if err := core.DeletePrompt(db, name); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.openProcessList(s.processType)
	}, s.win)
}

func (s *state) savePrompt() {
	db := s.currentDB()
	if db == nil {
		return
	}
	if err := core.SavePrompt(db, s.promptName, s.promptText); err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	s.promptEdit = false
	s.render()
}

func (s *state) reloadPrompt() {
	db := s.currentDB()
	if db == nil {
		return
	}
	text, err := core.ReloadPromptFromFile(db, s.promptName)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	s.promptText = text
	s.render()
}

// ─── export templates ───────────────────────────────────────────────────────

func (s *state) openExportTemplates() {
	db := s.currentDB()
	if db == nil {
		return
	}
	names, err := core.ListExportTemplates(db)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	if len(names) == 0 {
		names = []string{core.DefaultExportTemplate}
	}
	s.exportTemplates = names
	s.screen = scrExportTemplates
	s.render()
}

func (s *state) openExportTemplateDetail(name string) {
	db := s.currentDB()
	if db == nil {
		return
	}
	s.templateName = name
	s.templateText = core.LoadExportTemplate(db, name)
	s.templateEdit = false
	s.screen = scrExportTemplateDetail
	s.render()
}

func (s *state) addExportTemplate() {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("template name")
	dialog.ShowCustomConfirm("Add export template", "Create", "Cancel", nameEntry, func(ok bool) {
		if !ok {
			return
		}
		name := strings.TrimSpace(nameEntry.Text)
		if name == "" {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		if core.ExportTemplateExists(db, name) {
			dialog.ShowError(fmt.Errorf("template %q already exists", name), s.win)
			return
		}
		if err := core.SaveExportTemplate(db, name, core.DefaultExportTemplateBody); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.openExportTemplateDetail(name)
	}, s.win)
}

func (s *state) renameExportTemplate() {
	old := s.templateName
	entry := widget.NewEntry()
	entry.SetText(old)
	dialog.ShowCustomConfirm("Rename export template", "Rename", "Cancel", entry, func(ok bool) {
		if !ok {
			return
		}
		name := strings.TrimSpace(entry.Text)
		if name == "" || name == old {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		if err := core.RenameExportTemplate(db, old, name); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.openExportTemplateDetail(name)
	}, s.win)
}

func (s *state) deleteExportTemplate() {
	name := s.templateName
	dialog.ShowConfirm("Remove export template", "Delete template "+name+"?", func(ok bool) {
		if !ok {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		if err := core.DeleteExportTemplate(db, name); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.openExportTemplates()
	}, s.win)
}

// ─── process rename ─────────────────────────────────────────────────────────

func (s *state) renameProcess() {
	old := s.processName
	entry := widget.NewEntry()
	entry.SetText(old)
	dialog.ShowCustomConfirm("Rename process", "Rename", "Cancel", entry, func(ok bool) {
		if !ok {
			return
		}
		name := strings.TrimSpace(entry.Text)
		if name == "" || name == old {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		if err := core.RenamePrompt(db, old, name); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.processName = name
		s.render()
	}, s.win)
}

func (s *state) pickModel() {
	s.pickOllamaModel("Choose model", s.model)
}

// pickOllamaModel loads the models installed on the ollama host and lets the
// user pick one into the given entry. It is shared by the MODEL and EMBEDDING
// MODEL settings.
func (s *state) pickOllamaModel(title string, target *widget.Entry) {
	host := strings.TrimSpace(s.host.Text)
	go func() {
		models, err := core.FetchModels(host)
		fyne.Do(func() {
			if err != nil {
				s.ollamaStatus = "ollama ✗ " + err.Error()
				dialog.ShowError(err, s.win)
				return
			}
			if len(models) == 0 {
				s.ollamaStatus = "ollama ✓ — no models found"
				dialog.ShowInformation("Models", "No models found on "+host, s.win)
				return
			}
			s.ollamaStatus = "ollama ✓ " + strings.Join(models, ", ")
			list := widget.NewList(
				func() int { return len(models) },
				func() fyne.CanvasObject { return widget.NewLabel("") },
				func(id widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(models[id]) },
			)
			body := container.NewGridWrap(fyne.NewSize(440, 320), list)
			d := dialog.NewCustom(title, "Cancel", body, s.win)
			s.ollamaModels = models
			list.OnSelected = func(id widget.ListItemID) {
				target.SetText(models[id])
				s.persistSettings()
				d.Hide()
				s.render()
			}
			d.Show()
		})
	}()
}

// startImport runs the import with the chosen source and formats.
func (s *state) startImport() {
	if s.isRunning() {
		return
	}

	cfg, src, _, dbPath, err := s.readConfig()
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	if src == "" {
		dialog.ShowError(fmt.Errorf("not set: SRC"), s.win)
		return
	}
	if err := s.useDB(dbPath); err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	s.persistSettings()

	opts := core.ImportOptions{
		Source:        s.importSource,
		Formats:       s.importFormats,
		BinaryStorage: cfg.BinaryStorage,
	}
	db := s.currentDB()
	if db == nil {
		return
	}

	s.beginRun("Importing…")
	go func() {
		ctx, cancel := s.runContext()
		defer cancel()
		err := core.Import(ctx, &guiReporter{s: s}, db, src, opts)
		s.finishRun(err)
	}()
}

func (s *state) runSyncDiff() {
	db := s.currentDB()
	if db == nil {
		return
	}
	formats := s.importFormats
	if formats == nil {
		formats = make(core.FormatSet)
		for _, e := range core.DefaultImportFormats() {
			formats[e] = true
		}
	}
	src := strings.TrimSpace(s.src.Text)
	go func() {
		items, err := core.SyncDiff(db, src, formats)
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, s.win)
			} else {
				s.mu.Lock()
				s.syncItems = items
				s.syncChecked = nil
				s.mu.Unlock()
			}
			s.render()
		})
	}()
}

func (s *state) applySync() {
	db := s.currentDB()
	if db == nil {
		return
	}
	var removeIDs []int64
	var newPaths []string
	for _, it := range s.syncItems {
		key := syncItemGUIKey(it)
		if !s.syncChecked[key] {
			continue
		}
		if it.Deleted {
			if id := core.SyncNoteIDByPath(db, it.Path); id != 0 {
				removeIDs = append(removeIDs, id)
			}
		}
		if it.NewFile {
			newPaths = append(newPaths, it.Abs)
		}
	}
	if len(removeIDs) == 0 && len(newPaths) == 0 {
		return
	}
	cfg, _, _, _, err := s.readConfigRaw()
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	opts := core.ImportOptions{
		Source:        core.SourceFilesystem,
		Formats:       s.importFormats,
		BinaryStorage: cfg.BinaryStorage,
	}
	s.beginRun("Syncing…")
	src := strings.TrimSpace(s.src.Text)
	go func() {
		_, cancel := s.runContext()
		defer cancel()
		removed, imported, err := core.SyncApply(db, src, removeIDs, newPaths, opts)
		s.mu.Lock()
		s.syncItems = nil
		s.syncChecked = nil
		s.mu.Unlock()
		s.finishRunMessage(err, fmt.Sprintf("Sync done — removed %d, imported %d", removed, imported))
	}()
}

func (s *state) startPhase(phase core.Phase, promptName, templateName string) {
	if s.isRunning() {
		return
	}

	cfg, src, out, dbPath, err := s.readConfig()
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	if src == "" && phase == core.PhaseImport {
		dialog.ShowError(fmt.Errorf("not set: SRC"), s.win)
		return
	}
	if out == "" && phase == core.PhaseWrite {
		dialog.ShowError(fmt.Errorf("not set: OUT"), s.win)
		return
	}
	cfg.PromptName = promptName
	cfg.TemplateName = templateName
	if phase == core.PhaseExtract {
		cfg.ExtractName = s.extractFilter
		cfg.ExtractFormats = s.extractFormats
	}
	if err := s.useDB(dbPath); err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	s.persistSettings()
	db := s.currentDB()
	if db == nil {
		return
	}

	s.beginRun("Starting…")
	go func() {
		ctx, cancel := s.runContext()
		defer cancel()
		err := core.Run(ctx, &guiReporter{s: s}, db, cfg, phase, src, out)
		s.finishRun(err)
	}()
}

func (s *state) beginRun(label string) {
	s.mu.Lock()
	s.running = true
	s.total, s.done, s.ran = 0, 0, 0
	s.runTags, s.runLinks = 0, 0
	s.logs = nil
	s.mu.Unlock()

	s.phaseLabel.SetText(label)
	s.currentLabel.SetText("")
	s.runLabel.SetText("")
	s.logLabel.SetText("")
	s.progress.SetValue(0)
	s.refreshStats()

	s.screen = scrRunning
	s.render()
}

func (s *state) runContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	return ctx, cancel
}

func (s *state) finishRun(err error) {
	s.finishRunMessage(err, "Done")
}

// finishRunMessage is finishRun with a caller-provided success label, used by
// sync which reports how many notes were removed/imported.
func (s *state) finishRunMessage(err error, label string) {
	s.mu.Lock()
	s.running = false
	s.cancel = nil
	s.mu.Unlock()

	fyne.Do(func() {
		s.currentLabel.SetText("")
		s.refreshStats()
		if err != nil {
			s.phaseLabel.SetText("Stopped with error")
			dialog.ShowError(err, s.win)
		} else {
			s.phaseLabel.SetText(label)
		}
		s.screen = scrMenu
		s.render()
	})
}

func (s *state) stopRun() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		s.phaseLabel.SetText("Stopping…")
	}
}

func (s *state) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *state) currentDB() *sql.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

func (s *state) close() {
	s.persistSettings()
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.win.Close()
}

// readConfig gathers the settings form into a core Config and persists the
// non-default values for the next launch.
func (s *state) readConfig() (core.Config, string, string, string, error) {
	cfg, src, out, dbPath, err := s.readConfigRaw()
	if err != nil {
		return cfg, src, out, dbPath, err
	}
	s.saveSettings(cfg, src, out)
	return cfg, src, out, dbPath, nil
}

func (s *state) readConfigRaw() (core.Config, string, string, string, error) {
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

	whisperDevice, err := strconv.Atoi(strings.TrimSpace(s.whisperDevice.Text))
	if err != nil || whisperDevice < 0 {
		whisperDevice = core.DefaultWhisperDevice
	}

	cfg := core.Config{
		Host:            strings.TrimSpace(s.host.Text),
		Model:           strings.TrimSpace(s.model.Text),
		VisionModel:     strings.TrimSpace(s.vision.Text),
		EmbeddingModel:  core.NormalizeEmbeddingModel(s.embedding.Text),
		TranscribeModel: strings.TrimSpace(s.transcribe.Text),
		AudioEngine:     core.NormalizeAudioEngine(s.audioEngine.Selected),
		WhisperModel:    core.NormalizeWhisperModel(s.whisperModel.Selected),
		WhisperBackend:  core.NormalizeWhisperBackend(s.whisperBackend.Selected),
		WhisperDevice:   whisperDevice,
		GoogleAPIKey:    strings.TrimSpace(s.googleKey.Text),
		SpeechLanguage:  core.NormalizeSpeechLanguage(s.speechLanguage.Selected),
		BinaryStorage:   core.BinaryStorageLinks,
		MaxChars:        maxChars,
		Workers:         workers,
	}
	return cfg, strings.TrimSpace(s.src.Text), strings.TrimSpace(s.out.Text), strings.TrimSpace(s.dbEntry.Text), nil
}

func (s *state) saveSettings(cfg core.Config, src, out string) {
	db := s.currentDB()
	if db == nil {
		return
	}
	_ = core.AppSettings{
		Src:             src,
		Out:             out,
		Host:            cfg.Host,
		Model:           cfg.Model,
		VisionModel:     cfg.VisionModel,
		EmbeddingModel:  cfg.EmbeddingModel,
		TranscribeModel: cfg.TranscribeModel,
		AudioEngine:     cfg.AudioEngine,
		WhisperModel:    cfg.WhisperModel,
		WhisperBackend:  cfg.WhisperBackend,
		WhisperDevice:   cfg.WhisperDevice,
		GoogleAPIKey:    cfg.GoogleAPIKey,
		SpeechLanguage:  cfg.SpeechLanguage,
		MaxChars:        cfg.MaxChars,
		Workers:         cfg.Workers,
		RAGTopK:         s.ragTopK,
		Formats:         core.EncodeFormatSelection(s.importFormats),
	}.Save(db)
}

// persistSettings stores the current form values without validating the whole
// config; it is used for Enter presses and when the window closes.
func (s *state) persistSettings() {
	cfg, src, out, _, err := s.readConfigRaw()
	if err != nil {
		return
	}
	s.saveSettings(cfg, src, out)
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
		db := s.currentDB()

		newDB, err := core.ResetDB(db, path)
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.mu.Lock()
		s.db = newDB
		s.dbPath = path
		s.mu.Unlock()
		s.persistSettings()

		s.logs = nil
		s.logLabel.SetText("")
		s.phaseLabel.SetText("Ready")
		s.currentLabel.SetText("")
		s.runLabel.SetText("")
		s.progress.SetValue(0)
		s.refreshStats()
		s.render()
	}, s.win)
}

func (s *state) refreshStats() {
	db := s.currentDB()
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

// ─── reporter ───────────────────────────────────────────────────────────────

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

// ApproveModelPull asks the user (through a dialog) whether a missing ollama
// model may be downloaded. It blocks the worker goroutine until the dialog is
// answered.
func (r *guiReporter) ApproveModelPull(model string) bool {
	done := make(chan bool, 1)
	fyne.Do(func() {
		dialog.ShowConfirm(
			"Download model",
			fmt.Sprintf("Model %q is not installed. Download it now? This can take a while and may use several GB.", model),
			func(ok bool) { done <- ok },
			r.s.win,
		)
	})
	return <-done
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
