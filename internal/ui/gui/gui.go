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
	scrFormats
	scrData
	scrNotes
	scrFsSearch
	scrEval
	scrSettings
	scrPromptView
	scrProcessList
	scrProcessDetail
	scrRunning
	scrExportTemplates
	scrExportTemplateDetail
	scrBrowse
)

type noteAction int

const (
	noteRemove noteAction = iota
	noteRemoveLinks
	noteEval
)

// menuItems is the main menu shown on the start screen.
var menuItems = []string{
	"Import",
	"Data processing — pairs",
	"Data processing — one by one",
	"Export for Obsidian",
	"Work with data",
	"Evaluate note uniqueness",
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
	src, out, dbEntry, model, host *widget.Entry
	maxChars, workers              *widget.Entry
	formatsLabel                   *widget.Label

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

	// import
	importSource  core.ImportSource
	importFormats core.FormatSet

	// notes (remove / evaluate)
	noteAction noteAction
	noteFilter string
	notes      []core.NoteInfo

	// database browser
	browseNotes  []core.NoteDetail
	browseFilter string
	browseCols   map[string]bool

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
	s.browseCols = defaultBrowseCols()
	s.ollamaStatus = "checking ollama…"

	s.importSource = core.SourceFilesystem
	s.importFormats = make(core.FormatSet)
	for _, e := range core.DefaultImportFormats() {
		s.importFormats[e] = true
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
		models, err := core.FetchModels(cfg.Host)
		fyne.Do(func() {
			switch {
			case err != nil:
				s.ollamaStatus = "ollama ✗ " + err.Error()
			case len(models) == 0:
				s.ollamaStatus = "ollama ✓ — no models found"
			default:
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
		return "Import — choose source & formats"
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
	case scrRunning:
		return "Processing…"
	default:
		return "noesis"
	}
}

func (s *state) goBack() {
	switch s.screen {
	case scrImport, scrData, scrSettings, scrProcessList:
		s.noteFilter = ""
		s.screen = scrMenu
	case scrFormats:
		s.screen = scrImport
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
		return s.importScreen()
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
	case scrRunning:
		return s.runningScreen()
	default:
		return s.menuScreen()
	}
}

func (s *state) activateMenu(id int) {
	switch id {
	case 0:
		s.screen = scrImport
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
		s.openNotes(noteEval, scrMenu)
		return
	case 6:
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

func (s *state) removeBrowseNote(n core.NoteDetail) {
	message := fmt.Sprintf("Remove %q from the database?", n.FilePath)
	dialog.ShowConfirm("Remove file", message, func(ok bool) {
		if !ok {
			return
		}
		db := s.currentDB()
		if db == nil {
			return
		}
		removed, err := core.RemoveNoteByID(db, n.ID)
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.reloadBrowse()
		s.refreshStats()
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
		result, err := core.CompareFile(context.Background(), db, path, cfg.Host, cfg.Model, cfg.MaxChars)
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
			d := dialog.NewCustom("Choose model", "Cancel", body, s.win)
			list.OnSelected = func(id widget.ListItemID) {
				s.model.SetText(models[id])
				d.Hide()
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

	_, src, _, dbPath, err := s.readConfig()
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

	opts := core.ImportOptions{Source: s.importSource, Formats: s.importFormats}
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
	if out == "" && (phase == core.PhaseWrite || phase == core.PhaseExportProcess) {
		dialog.ShowError(fmt.Errorf("not set: OUT"), s.win)
		return
	}
	cfg.PromptName = promptName
	cfg.TemplateName = templateName
	if err := s.useDB(dbPath); err != nil {
		dialog.ShowError(err, s.win)
		return
	}
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
			s.phaseLabel.SetText("Done")
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
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.win.Close()
}

// readConfig gathers the settings form into a core Config.
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

func ratio(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	if done > total {
		done = total
	}
	return float64(done) / float64(total)
}
