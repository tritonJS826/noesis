package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"noesis/internal/core"
)

func vimNav(key string) string {
	switch key {
	case "j":
		return "down"
	case "k":
		return "up"
	case "h":
		return "esc"
	case "l":
		return "enter"
	}
	return key
}

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch m.screen {
	case scrMenu:
		return m.menuKeys(msg)
	case scrImport:
		return m.syncKeys(msg)
	case scrSync:
		return m.syncKeys(msg)
	case scrFormats:
		return m.formatsKeys(msg)
	case scrData:
		return m.dataKeys(msg)
	case scrPickData:
		return m.pickDataKeys(msg)
	case scrComparePath:
		return m.comparePathKeys(msg)
	case scrSettings:
		return m.settingsKeys(msg)
	case scrModelPicker:
		return m.modelPickerKeys(msg)
	case scrOptionPicker:
		return m.optionPickerKeys(msg)
	case scrWhisper:
		return m.whisperKeys(msg)
	case scrAudioHelp:
		return m.audioHelpKeys(msg)
	case scrFilePicker:
		return m.filePickerKeys(msg)
	case scrEval:
		return m.evalKeys(msg)
	case scrProcessList:
		return m.processListKeys(msg)
	case scrProcessDetail:
		return m.processDetailKeys(msg)
	case scrFsSearch:
		return m.fsSearchKeys(msg)
	case scrExportTemplates:
		return m.exportTemplatesKeys(msg)
	case scrExportTemplateDetail:
		return m.exportTemplateDetailKeys(msg)
	case scrBrowse:
		return m.browseKeys(msg)
	case scrBrowseDetail:
		return m.browseDetailKeys(msg)
	case scrExtract:
		return m.extractKeys(msg)
	case scrRAGSearch:
		return m.ragSearchKeys(msg)
	case scrRunning:
	}
	return nil
}

// ─── main menu ───────────────────────────────────────────────────────────────

const menuItems = 5 // 0..5

func (m *model) menuKeys(msg tea.KeyMsg) tea.Cmd {
	switch vimNav(msg.String()) {
	case "q", "ctrl+c":
		return tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.resetErr = ""
	case "down":
		if m.cursor < menuItems {
			m.cursor++
		}
		m.resetErr = ""
	case "enter":
		switch m.cursor {
		case 0: // synchronize
			m.screen = scrSync
			m.syncCursor = 0
			m.syncItems = nil
			m.syncChecked = nil
			m.syncSource = m.importSource // keep the last used source type
			return m.runSyncDiff()
		case 1: // data processing — pairs → open pair list
			m.openProcessList("pair")
		case 2: // data processing — one by one → open single list
			m.openProcessList("single")
		case 3: // export for obsidian → choose a template
			m.openExportTemplates()
		case 4: // work with data
			m.screen = scrData
			m.dataCursor = 0
			m.cursor = 0
		case 5: // settings
			m.screen = scrSettings
			m.cursor = 0
		}
	}
	return nil
}

// startPhase validates required fields and starts a phase from the menu.
func (m *model) startPhase(phase core.Phase, needSrc, needOut bool, promptName, templateName string) tea.Cmd {
	var missing []string
	if needSrc && m.src == "" {
		missing = append(missing, "SRC")
	}
	if needOut && m.out == "" {
		missing = append(missing, "OUT")
	}
	if len(missing) > 0 {
		m.phaseErr = fmt.Errorf("Not set: %s", strings.Join(missing, ", "))
		return nil
	}
	m.screen = scrRunning
	m.phaseErr = nil
	return runPhaseCmd(m, phase, promptName, templateName)
}

// ─── import submenu ─────────────────────────────────────────────────────────

func (m *model) importKeys(msg tea.KeyMsg) tea.Cmd {
	switch vimNav(msg.String()) {
	case "up":
		if m.importCursor > 0 {
			m.importCursor--
		}
	case "down":
		if m.importCursor < 2 {
			m.importCursor++
		}
	case "enter":
		switch m.importCursor {
		case 0: // cycle source
			m.importSource = (m.importSource + 1) % 3
		case 1: // choose formats
			m.screen = scrFormats
			m.fmtCursor = 0
		case 2: // run import
			if m.src == "" {
				m.phaseErr = fmt.Errorf("Not set: SRC")
				return nil
			}
			m.screen = scrRunning
			m.phaseErr = nil
			return runImportCmd(m)
		}
	case "esc", "q":
		m.screen = scrMenu
		m.cursor = 0
	}
	return nil
}

// ─── sync ───────────────────────────────────────────────────────────────────

func (m *model) syncKeys(msg tea.KeyMsg) tea.Cmd {
	const settingsRows = 3 // source, formats, action
	switch vimNav(msg.String()) {
	case "up":
		if m.syncCursor > 0 {
			m.syncCursor--
		}
	case "down":
		maxCursor := settingsRows - 1
		if len(m.syncItems) > 0 {
			maxCursor = settingsRows + len(m.syncItems) - 1
		}
		if m.syncCursor < maxCursor {
			m.syncCursor++
		}
	case "enter":
		switch {
		case m.syncCursor == 0:
			// Cycle source type
			m.syncSource = (m.syncSource + 1) % 3
			m.importSource = m.syncSource
			m.syncItems = nil
			m.syncChecked = nil
		case m.syncCursor == 1:
			// Choose formats
			m.screen = scrFormats
			m.fmtCursor = 0
		case m.syncCursor == 2:
			// Action button
			if m.syncSource == core.SourceFilesystem {
				if m.src == "" {
					m.phaseErr = fmt.Errorf("Not set: SRC")
					return nil
				}
				return m.runSyncDiff()
			} else {
				// ZIP or JSON — import directly
				if m.src == "" {
					m.phaseErr = fmt.Errorf("Not set: SRC")
					return nil
				}
				m.screen = scrRunning
				m.phaseErr = nil
				return runImportCmd(m)
			}
		default:
			// Apply sync changes
			return m.applySync()
		}
	case "s", " ":
		// Toggle current item
		if m.syncCursor >= settingsRows && len(m.syncItems) > 0 {
			it := m.syncItems[m.syncCursor-settingsRows]
			key := syncItemKey(it)
			if m.syncChecked == nil {
				m.syncChecked = map[string]bool{}
			}
			m.syncChecked[key] = !m.syncChecked[key]
		}
	case "a", "A":
		// toggle all
		if m.syncChecked == nil {
			m.syncChecked = map[string]bool{}
		}
		all := true
		for _, it := range m.syncItems {
			if !m.syncChecked[syncItemKey(it)] {
				all = false
				break
			}
		}
		for _, it := range m.syncItems {
			m.syncChecked[syncItemKey(it)] = !all
		}
	case "r", "R":
		if m.syncSource == core.SourceFilesystem {
			return m.runSyncDiff()
		}
	case "esc", "h", "q":
		m.syncItems = nil
		m.syncChecked = nil
		m.screen = scrMenu
		m.cursor = 0
	}
	return nil
}

func (m *model) runSyncDiff() tea.Cmd {
	return func() tea.Msg {
		items, err := core.SyncDiff(m.db, m.src, m.importFormats)
		return syncDiffMsg{items, err}
	}
}

type syncDiffMsg struct {
	items []core.SyncItem
	err   error
}

type syncApplyMsg struct {
	removed  int
	imported int
	err      error
}

func (m *model) applySync() tea.Cmd {
	if len(m.syncItems) == 0 {
		return nil
	}
	var removeIDs []int64
	var newPaths []string
	for _, it := range m.syncItems {
		key := syncItemKey(it)
		if !m.syncChecked[key] {
			continue
		}
		if it.Deleted {
			if id := core.SyncNoteIDByPath(m.db, it.Path); id != 0 {
				removeIDs = append(removeIDs, id)
			}
		}
		if it.NewFile {
			newPaths = append(newPaths, it.Abs)
		}
	}
	if len(removeIDs) == 0 && len(newPaths) == 0 {
		return nil
	}
	m.screen = scrRunning
	m.phaseErr = nil
	return func() tea.Msg {
		m.program.ReleaseTerminal()
		defer m.program.RestoreTerminal()

		opts := core.ImportOptions{
			Source:        core.SourceFilesystem,
			Formats:       m.importFormats,
			BinaryStorage: m.binaryStorage,
		}
		removed, imported, err := core.SyncApply(m.db, m.src, removeIDs, newPaths, opts)
		return syncApplyMsg{removed, imported, err}
	}
}

// ─── format selection ───────────────────────────────────────────────────────

func (m *model) formatsKeys(msg tea.KeyMsg) tea.Cmd {
	formats := core.KnownFormats()
	page := m.listRows(5)
	if page < 1 {
		page = 10
	}
	switch vimNav(msg.String()) {
	case "up":
		if m.fmtCursor > 0 {
			m.fmtCursor--
		}
	case "down":
		if m.fmtCursor < len(formats)-1 {
			m.fmtCursor++
		}
	case "pgup":
		m.fmtCursor -= page
		if m.fmtCursor < 0 {
			m.fmtCursor = 0
		}
	case "pgdown":
		m.fmtCursor += page
		if m.fmtCursor >= len(formats) {
			m.fmtCursor = len(formats) - 1
		}
	case "home":
		m.fmtCursor = 0
	case "end":
		m.fmtCursor = len(formats) - 1
	case "enter", " ":
		if m.fmtCursor < len(formats) {
			m.toggleFormat(formats[m.fmtCursor].Ext)
		}
	case "a", "A":
		// toggle between "all formats" (nil) and "none"
		if m.importFormats == nil {
			m.importFormats = core.FormatSet{}
		} else {
			m.importFormats = nil
		}
		m.persistSettings()
	case "esc", "q":
		m.screen = scrSync
		m.syncCursor = 1
	}
	return nil
}

func (m *model) toggleFormat(ext string) {
	if m.importFormats == nil {
		m.importFormats = core.FormatSet{}
	}
	if m.importFormats[ext] {
		delete(m.importFormats, ext)
	} else {
		m.importFormats[ext] = true
	}
	m.persistSettings()
}

// ─── work with data submenu ────────────────────────────────────────────────

const dataItems = 4 // 0..4

func (m *model) dataKeys(msg tea.KeyMsg) tea.Cmd {
	switch vimNav(msg.String()) {
	case "up":
		if m.dataCursor > 0 {
			m.dataCursor--
		}
	case "down":
		if m.dataCursor < dataItems {
			m.dataCursor++
		}
	case "enter":
		switch m.dataCursor {
		case 0: // browse database
			m.openBrowse()
		case 1: // compare file — file system search
			m.screen = scrFsSearch
			m.fsFilter = ""
			m.fsFiles = nil
			m.cursor = 0
			m.filterTyping = false
		case 2: // re-extract documents, images & transcribe audio
			m.openExtract()
		case 3: // semantic (RAG) search
			m.openRAGSearch()
		case 4: // evaluate note uniqueness — pick a note.
			m.openFilePickerFrom(scrData, 4)
		}
	case "esc", "q":
		m.screen = scrMenu
		m.cursor = 4
	}
	return nil
}

// ─── semantic (RAG) search ──────────────────────────────────────────────────

func (m *model) openRAGSearch() {
	m.ragQuery = ""
	m.ragResults = nil
	m.ragAnswer = ""
	m.ragErr = ""
	m.ragBusy = false
	m.ragTyping = true
	m.ragAsk = false
	m.ragConfirm = false
	m.ragConfirmModels = nil
	if m.ragTopK < 1 {
		m.ragTopK = 5
	}
	m.cursor = 0
	m.screen = scrRAGSearch
}

func (m *model) ragSearchKeys(msg tea.KeyMsg) tea.Cmd {
	if m.ragBusy {
		return nil
	}
	key := msg.String()

	if m.ragConfirm {
		m.ragConfirm = false
		if strings.EqualFold(key, "y") {
			m.ragBusy = true
			models := m.ragConfirmModels
			m.ragConfirmModels = nil
			return ragPullCmd(m, models)
		}
		m.ragConfirmModels = nil
		return nil
	}

	if m.ragTyping {
		switch key {
		case "esc":
			if m.ragQuery != "" {
				m.ragQuery = ""
				m.ragResults = nil
				m.ragAnswer = ""
				m.ragErr = ""
				return nil
			}
			m.ragTyping = false
		case "enter":
			return m.runRAGSearch(false)
		case "ctrl+a":
			return m.runRAGSearch(true)
		case "backspace":
			r := []rune(m.ragQuery)
			if len(r) > 0 {
				m.ragQuery = string(r[:len(r)-1])
			}
		default:
			if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
				m.ragQuery += string(msg.Runes[0])
			}
		}
		return nil
	}

	switch vimNav(key) {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.ragResults)-1 {
			m.cursor++
		}
	case "enter":
		if len(m.ragResults) > 0 && m.cursor < len(m.ragResults) {
			n := m.ragResults[m.cursor]
			m.openBrowseDetail(core.NoteDetail{ID: n.NoteID, FileName: n.Name, FilePath: n.Path})
		}
	case "a", "A":
		return m.runRAGSearch(true)
	case "/", "i":
		m.ragTyping = true
		m.ragQuery = ""
		m.ragResults = nil
		m.ragAnswer = ""
		m.ragErr = ""
	case "esc", "h", "q":
		m.screen = scrData
		m.dataCursor = 3
	}
	return nil
}

// runRAGSearch starts a semantic search (and, when ask is true, an LLM answer
// grounded in the retrieved chunks). It first checks that the required models
// are installed so they can be downloaded on demand.
func (m *model) runRAGSearch(ask bool) tea.Cmd {
	if strings.TrimSpace(m.ragQuery) == "" {
		m.ragErr = "enter a search query"
		return nil
	}
	m.ragBusy = true
	m.ragAsk = ask
	m.ragErr = ""
	m.ragAnswer = ""
	m.ragTyping = false
	m.ragConfirm = false
	m.ragConfirmModels = nil
	m.cursor = 0
	return ragPreflightCmd(m.ragConfig(), ask)
}

// ─── re-extract picker ──────────────────────────────────────────────────────

// openExtract opens the re-extraction picker seeded with "all formats".
func (m *model) openExtract() {
	m.extractFilter = ""
	m.extractFormats = nil
	m.extractCursor = 0
	m.extractTyping = false
	m.screen = scrExtract
}

func (m *model) extractKeys(msg tea.KeyMsg) tea.Cmd {
	formats := core.ExtractableFormats()
	page := m.listRows(9)
	if page < 1 {
		page = 10
	}

	if m.extractTyping {
		switch msg.String() {
		case "esc":
			m.extractTyping = false
		case "enter":
			m.extractTyping = false
		case "backspace":
			r := []rune(m.extractFilter)
			if len(r) > 0 {
				m.extractFilter = string(r[:len(r)-1])
			}
		default:
			if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
				m.extractFilter += string(msg.Runes[0])
			}
		}
		return nil
	}

	switch vimNav(msg.String()) {
	case "up":
		if m.extractCursor > 0 {
			m.extractCursor--
		}
	case "down":
		if m.extractCursor < len(formats)-1 {
			m.extractCursor++
		}
	case "pgup":
		m.extractCursor -= page
		if m.extractCursor < 0 {
			m.extractCursor = 0
		}
	case "pgdown":
		m.extractCursor += page
		if m.extractCursor >= len(formats) {
			m.extractCursor = len(formats) - 1
		}
	case "home", "g":
		m.extractCursor = 0
	case "end", "G":
		if len(formats) > 0 {
			m.extractCursor = len(formats) - 1
		}
	case "enter", " ":
		if len(formats) > 0 {
			m.toggleExtractFormat(formats[m.extractCursor].Ext)
		}
	case "/", "e":
		m.extractTyping = true
	case "a", "A":
		if m.extractFormats == nil {
			m.extractFormats = core.FormatSet{}
		} else {
			m.extractFormats = nil
		}
	case "r", "R":
		return m.runExtract()
	case "esc", "h", "q":
		m.screen = scrData
		m.dataCursor = 2
	}
	return nil
}

func (m *model) toggleExtractFormat(ext string) {
	if m.extractFormats == nil {
		m.extractFormats = core.FormatSet{}
		for _, f := range core.ExtractableFormats() {
			m.extractFormats[f.Ext] = true
		}
	}
	if m.extractFormats[ext] {
		delete(m.extractFormats, ext)
	} else {
		m.extractFormats[ext] = true
	}
}

func (m *model) runExtract() tea.Cmd {
	m.screen = scrRunning
	m.phaseErr = nil
	return runPhaseCmd(m, core.PhaseExtract, "", "")
}

func (m *model) openPickData(mode int) {
	notes, err := core.ListNotes(m.db)
	if err != nil {
		return
	}
	m.fileNotes = notes
	m.pickMode = mode
	m.cursor = 0
	m.fileFilter = ""
	m.pickResult = ""
	m.pickErr = ""
	m.filterTyping = false
	m.screen = scrPickData
}

// ─── database browser ──────────────────────────────────────────────────────

var browseColLabels = [5]string{"Path", "Format", "Imported", "Tags", "Links"}

func (m *model) openBrowse() {
	notes, err := core.ListNotesDetailed(m.db)
	if err != nil {
		m.phaseErr = err
		return
	}
	m.browseNotes = notes
	m.browseFilter = ""
	m.browseConfirm = false
	m.browseResult = ""
	m.browseErr = ""
	m.cursor = 0
	m.filterTyping = false
	m.screen = scrBrowse
}

func (m *model) filteredBrowse() []core.NoteDetail {
	filter := strings.ToLower(strings.TrimSpace(m.browseFilter))
	if filter == "" {
		return m.browseNotes
	}
	var out []core.NoteDetail
	for _, n := range m.browseNotes {
		if strings.Contains(strings.ToLower(n.FilePath), filter) ||
			strings.Contains(strings.ToLower(n.FileName), filter) ||
			strings.Contains(strings.ToLower(n.MimeType), filter) ||
			strings.Contains(strings.ToLower(n.Tags), filter) {
			out = append(out, n)
		}
	}
	return out
}

func (m *model) openBrowseDetail(n core.NoteDetail) {
	fields, err := core.NoteRecord(m.db, n.ID)
	if err != nil {
		m.browseErr = err.Error()
		return
	}
	m.browseDetail = fields
	m.browseDetailID = n.ID
	m.browseDetailPath = n.FilePath
	m.browseDetailConfirm = false
	m.browseDetailOffset = 0
	m.filterTyping = false
	m.screen = scrBrowseDetail
}

func (m *model) browseKeys(msg tea.KeyMsg) tea.Cmd {
	if m.browseConfirm {
		switch msg.String() {
		case "y", "Y":
			m.browseConfirm = false
			m.removeFromBrowse = true
			m.browseResult = ""
			return removeNoteCmd(m.db, m.browseConfirmID, 0)
		default:
			m.browseConfirm = false
			m.browseResult = ""
		}
		return nil
	}

	// A previous removal error is dismissed by the next key press.
	m.browseErr = ""

	key := msg.String()
	if m.filterTyping {
		switch key {
		case "esc":
			m.filterTyping = false
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.filteredBrowse())-1 {
				m.cursor++
			}
		case "enter":
			filtered := m.filteredBrowse()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				m.openBrowseDetail(filtered[m.cursor])
			}
		case "backspace":
			r := []rune(m.browseFilter)
			if len(r) > 0 {
				m.browseFilter = string(r[:len(r)-1])
			}
			m.cursor = 0
			m.browseResult = ""
		default:
			if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
				m.browseFilter += string(msg.Runes[0])
				m.cursor = 0
				m.browseResult = ""
			}
		}
		return nil
	}

	switch vimNav(key) {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.browseResult = ""
	case "down":
		if m.cursor < len(m.filteredBrowse())-1 {
			m.cursor++
		}
		m.browseResult = ""
	case "pgup":
		page := m.listRows(6)
		if page < 1 {
			page = 10
		}
		m.cursor -= page
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.browseResult = ""
	case "pgdown":
		page := m.listRows(6)
		if page < 1 {
			page = 10
		}
		m.cursor += page
		if max := len(m.filteredBrowse()) - 1; m.cursor > max {
			m.cursor = max
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.browseResult = ""
	case "home", "g":
		m.cursor = 0
		m.browseResult = ""
	case "end", "G":
		if max := len(m.filteredBrowse()) - 1; max >= 0 {
			m.cursor = max
		}
		m.browseResult = ""
	case "enter":
		filtered := m.filteredBrowse()
		if len(filtered) > 0 && m.cursor < len(filtered) {
			m.openBrowseDetail(filtered[m.cursor])
		}
	case "delete":
		filtered := m.filteredBrowse()
		if len(filtered) > 0 && m.cursor < len(filtered) {
			n := filtered[m.cursor]
			m.browseConfirmID = n.ID
			m.browseConfirm = true
			m.browseResult = fmt.Sprintf("Remove %q — are you sure? (y/N)", n.FilePath)
		}
	case "f1", "f2", "f3", "f4", "f5":
		idx := int(key[1] - '1')
		m.browseCols[idx] = !m.browseCols[idx]
		m.browseResult = ""
	case "esc", "q":
		m.screen = scrData
		m.dataCursor = 0
	case "backspace":
		r := []rune(m.browseFilter)
		if len(r) > 0 {
			m.browseFilter = string(r[:len(r)-1])
		}
		m.cursor = 0
		m.browseResult = ""
	default:
		if key == "/" {
			m.filterTyping = true
			return nil
		}
		if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
			m.filterTyping = true
			m.browseFilter += string(msg.Runes[0])
			m.cursor = 0
			m.browseResult = ""
		}
	}
	return nil
}

func (m *model) browseDetailKeys(msg tea.KeyMsg) tea.Cmd {
	if m.browseDetailConfirm {
		switch msg.String() {
		case "y", "Y":
			m.browseDetailConfirm = false
			m.removeFromDetail = true
			m.browseResult = ""
			return removeNoteCmd(m.db, m.browseDetailID, 0)
		default:
			m.browseDetailConfirm = false
		}
		return nil
	}

	switch vimNav(msg.String()) {
	case "up":
		m.scrollBrowseDetail(-1)
	case "down":
		m.scrollBrowseDetail(1)
	case "pgup":
		m.scrollBrowseDetail(-m.browseDetailPage())
	case "pgdown":
		m.scrollBrowseDetail(m.browseDetailPage())
	case "home", "g":
		m.browseDetailOffset = 0
	case "end", "G":
		m.browseDetailOffset = m.browseDetailMaxOffset()
	case "d", "delete":
		m.browseDetailConfirm = true
	case "esc", "q":
		m.screen = scrBrowse
		m.browseDetail = nil
		m.browseDetailOffset = 0
	}
	return nil
}

// ─── pick-data (search by path for remove) ─────────────────────────────────

func (m *model) pickDataKeys(msg tea.KeyMsg) tea.Cmd {
	// a completed/errored remove shows a message; any key returns to the list
	if !m.pickConfirm && (m.pickResult != "" || m.pickErr != "") {
		switch msg.String() {
		case "up", "down":
			m.pickResult = ""
			m.pickErr = ""
			// fall through to navigation below
		default:
			m.pickResult = ""
			m.pickErr = ""
			return nil
		}
	}

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
			id := filtered[m.cursor].ID
			name := filtered[m.cursor].Name
			label := "file"
			if m.pickMode == 1 {
				label = "file + links"
			}
			m.pickResult = fmt.Sprintf("Remove %s %q — are you sure? (y/N)", label, name)
			m.pickErr = ""
			m.pickConfirmID = id
			m.pickConfirm = true
		}
	case "y", "Y":
		if m.pickConfirm {
			m.pickConfirm = false
			id := m.pickConfirmID
			m.pickResult = ""
			return removeNoteCmd(m.db, id, m.pickMode)
		}
	case "esc", "q":
		if m.pickConfirm {
			m.pickConfirm = false
			m.pickResult = ""
			m.pickErr = ""
			return nil
		}
		m.screen = scrData
		m.dataCursor = 1 + m.pickMode
	case "backspace":
		r := []rune(m.fileFilter)
		if len(r) > 0 {
			m.fileFilter = string(r[:len(r)-1])
		}
		m.cursor = 0
	default:
		if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
			m.fileFilter += string(msg.Runes[0])
			m.cursor = 0
		}
	}
	return nil
}

// ─── compare file path input ───────────────────────────────────────────────

func (m *model) comparePathKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		if strings.TrimSpace(m.comparePath) != "" {
			m.evalResult = ""
			m.evalErr = ""
			m.evalLoading = true
			m.evalReturn = scrComparePath
			m.evalReturnCursor = 0
			m.screen = scrEval
			return compareFileCmd(m.db, m.comparePath, m.host, m.modelName, m.audioOptions(), m.maxChars)
		}
	case "esc", "q":
		m.screen = scrData
		m.dataCursor = 2
	case "backspace":
		r := []rune(m.comparePath)
		if len(r) > 0 {
			m.comparePath = string(r[:len(r)-1])
		}
	default:
		if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
			m.comparePath += string(msg.Runes[0])
		}
	}
	return nil
}

// ─── settings ──────────────────────────────────────────────────────────────

// Settings row indices (see viewSettings for the matching order).
const (
	setSrc = iota
	setOut
	setDB
	setModel
	setVision
	setEmbedding
	setAudio
	setWhisper
	setWhisperGPU
	setWhisperDevice
	setGoogle
	setLang
	setHost
	setMaxChars
	setWorkers
	setBinary
	setEvalPrompt
	setReset
)

func (m *model) settingsKeys(msg tea.KeyMsg) tea.Cmd {
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
	if m.promptViewing != "" {
		return m.promptKeys(msg)
	}
	max := setReset
	switch vimNav(msg.String()) {
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
		m.cursor = 5
	case "?":
		m.screen = scrAudioHelp
	case "enter":
		if m.cursor == setReset {
			// Reset database
			m.confirmReset = true
		} else {
			m.startEdit()
		}
	}
	return nil
}

// openModelPicker opens the installed-models list for a settings row. target is
// "model" (MODEL) or "embedding" (EMBEDDING MODEL).
func (m *model) openModelPicker(target string) {
	m.modelPickerTarget = target
	m.screen = scrModelPicker
	m.cursor = 0
	current := m.modelName
	if target == "embedding" {
		current = m.embeddingModel
	}
	for i, mod := range m.ollamaModels {
		if mod == current {
			m.cursor = i
			break
		}
	}
}

// backFromModelPicker returns to the settings row that opened the picker.
func (m *model) backFromModelPicker() {
	m.screen = scrSettings
	if m.modelPickerTarget == "embedding" {
		m.cursor = setEmbedding
	} else {
		m.cursor = setModel
	}
}

func (m *model) modelPickerKeys(msg tea.KeyMsg) tea.Cmd {
	switch vimNav(msg.String()) {
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
			picked := m.ollamaModels[m.cursor]
			if m.modelPickerTarget == "embedding" {
				m.embeddingModel = core.NormalizeEmbeddingModel(picked)
			} else {
				m.modelName = picked
			}
			m.persistSettings()
		}
		m.backFromModelPicker()
	case "esc":
		m.backFromModelPicker()
	}
	return nil
}

func (m *model) openOptionPicker(kind string, values, labels []string, current string) {
	m.pickerKind = kind
	m.pickerValues = values
	m.pickerLabels = labels
	m.pickerCursor = 0
	for i, v := range values {
		if strings.EqualFold(v, current) {
			m.pickerCursor = i
			break
		}
	}
	m.screen = scrOptionPicker
}

func (m *model) optionPickerKeys(msg tea.KeyMsg) tea.Cmd {
	switch vimNav(msg.String()) {
	case "up":
		if m.pickerCursor > 0 {
			m.pickerCursor--
		}
	case "down":
		if m.pickerCursor < len(m.pickerValues)-1 {
			m.pickerCursor++
		}
	case "enter":
		if len(m.pickerValues) > 0 && m.pickerCursor < len(m.pickerValues) {
			value := m.pickerValues[m.pickerCursor]
			switch m.pickerKind {
			case "audio":
				m.audioEngine = core.NormalizeAudioEngine(value)
			case "language":
				m.speechLanguage = core.NormalizeSpeechLanguage(value)
			case "binary":
				m.binaryStorage = core.NormalizeBinaryStorage(value)
			case "whisper_gpu":
				m.whisperBackend = core.NormalizeWhisperBackend(value)
			}
			m.persistSettings()
		}
		m.screen = scrSettings
		switch m.pickerKind {
		case "audio":
			m.cursor = setAudio
		case "binary":
			m.cursor = setBinary
		case "whisper_gpu":
			m.cursor = setWhisperGPU
		default:
			m.cursor = setLang
		}
	case "esc":
		m.screen = scrSettings
		switch m.pickerKind {
		case "audio":
			m.cursor = setAudio
		case "binary":
			m.cursor = setBinary
		case "whisper_gpu":
			m.cursor = setWhisperGPU
		default:
			m.cursor = setLang
		}
	case "?":
		if m.pickerKind == "audio" {
			m.screen = scrAudioHelp
		}
	}
	return nil
}

func (m *model) openFilePickerFrom(back screen, cursor int) {
	notes, err := core.ListNotes(m.db)
	if err != nil {
		return
	}
	m.fileNotes = notes
	m.cursor = 0
	m.fileFilter = ""
	m.filterTyping = false
	m.pickerBack = back
	m.pickerBackCursor = cursor
	m.screen = scrFilePicker
}

func (m *model) filePickerKeys(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.filterTyping {
		switch key {
		case "esc":
			m.filterTyping = false
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
			return m.evalSelectedFile()
		case "backspace":
			r := []rune(m.fileFilter)
			if len(r) > 0 {
				m.fileFilter = string(r[:len(r)-1])
			}
			m.cursor = 0
		default:
			if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
				m.fileFilter += string(msg.Runes[0])
				m.cursor = 0
			}
		}
		return nil
	}

	switch vimNav(key) {
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
		return m.evalSelectedFile()
	case "esc", "q":
		m.screen = m.pickerBack
		m.cursor = m.pickerBackCursor
	case "backspace":
		r := []rune(m.fileFilter)
		if len(r) > 0 {
			m.fileFilter = string(r[:len(r)-1])
		}
		m.cursor = 0
	default:
		if key == "/" {
			m.filterTyping = true
			return nil
		}
		if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
			m.filterTyping = true
			m.fileFilter += string(msg.Runes[0])
			m.cursor = 0
		}
	}
	return nil
}

func (m *model) evalSelectedFile() tea.Cmd {
	filtered := m.filteredNotes()
	if len(filtered) == 0 || m.cursor >= len(filtered) {
		return nil
	}
	id := filtered[m.cursor].ID
	linked, _ := core.LinkedNoteIDs(m.db, id)
	m.evalResult = ""
	m.evalErr = ""
	m.evalLoading = true
	m.evalReturn = scrFilePicker
	m.evalReturnCursor = m.cursor
	m.filterTyping = false
	m.screen = scrEval
	return evalNoteCmd(m.db, id, m.host, m.modelName, m.maxChars, linked)
}

func (m *model) filteredNotes() []core.NoteInfo {
	if m.fileFilter == "" {
		return m.fileNotes
	}
	lower := strings.ToLower(m.fileFilter)
	var out []core.NoteInfo
	for _, n := range m.fileNotes {
		if strings.Contains(strings.ToLower(n.Name), lower) ||
			strings.Contains(strings.ToLower(n.Path), lower) {
			out = append(out, n)
		}
	}
	return out
}

func (m *model) evalKeys(msg tea.KeyMsg) tea.Cmd {
	if !m.evalLoading {
		m.screen = m.evalReturn
		m.cursor = m.evalReturnCursor
	}
	return nil
}

// ─── field editing ───────────────────────────────────────────────────────────

// startMultilineEdit opens the textarea editor for a multi-line value (a prompt
// or an export template). Enter inserts a newline and Ctrl+S saves.
func (m *model) startMultilineEdit(field, value string) {
	width := m.width - 8
	if width < 20 {
		width = 20
	}
	height := m.height - 16
	if height < 4 {
		height = 4
	}
	m.textArea.SetWidth(width)
	m.textArea.SetHeight(height)
	m.textArea.SetValue(value)
	m.textArea.Focus()
	m.editingField = field
	m.editingMultiline = true
}

func (m *model) startEdit() {
	m.editingMultiline = false
	m.textInput.EchoMode = textinput.EchoNormal
	switch m.cursor {
	case setSrc:
		m.textInput.SetValue(m.src)
		m.textInput.Focus()
		m.editingField = "SRC — path to notes / archive / json"
	case setOut:
		m.textInput.SetValue(m.out)
		m.textInput.Focus()
		m.editingField = "OUT — vault folder"
	case setDB:
		m.textInput.SetValue(m.dbPath)
		m.textInput.Focus()
		m.editingField = "DB — database file"
	case setModel:
		if m.ollamaOK && len(m.ollamaModels) > 0 {
			m.openModelPicker("model")
			return
		}
		m.textInput.SetValue(m.modelName)
		m.textInput.Focus()
		m.editingField = "MODEL — ollama model"
	case setVision:
		m.textInput.SetValue(m.visionModel)
		m.textInput.Focus()
		m.editingField = "VISION MODEL — describes images (empty = MODEL)"
	case setEmbedding:
		if m.ollamaOK && len(m.ollamaModels) > 0 {
			m.openModelPicker("embedding")
			return
		}
		m.textInput.SetValue(m.embeddingModel)
		m.textInput.Focus()
		m.editingField = "EMBEDDING MODEL — ollama model for RAG"
	case setAudio:
		m.openOptionPicker("audio", core.AudioEngineIDs(), core.AudioEngineLabels(), m.audioEngine)
		return
	case setWhisper:
		m.openWhisperScreen()
		return
	case setWhisperGPU:
		m.openOptionPicker("whisper_gpu", core.WhisperBackendIDs(), core.WhisperBackendLabels(), m.whisperBackend)
		return
	case setWhisperDevice:
		m.textInput.SetValue(fmt.Sprintf("%d", m.whisperDevice))
		m.textInput.Focus()
		m.editingField = "VULKAN DEVICE — device index (0 = default)"
	case setGoogle:
		m.textInput.SetValue(m.googleAPIKey)
		m.textInput.EchoMode = textinput.EchoPassword
		m.textInput.Focus()
		m.editingField = "GOOGLE API KEY — stored in the database"
	case setLang:
		m.openOptionPicker("language", core.SpeechLanguageCodes(), core.SpeechLanguageLabels(), m.speechLanguage)
		return
	case setHost:
		m.textInput.SetValue(m.host)
		m.textInput.Focus()
		m.editingField = "HOST — ollama host"
	case setMaxChars:
		m.textInput.SetValue(fmt.Sprintf("%d", m.maxChars))
		m.textInput.Focus()
		m.editingField = "MAXCHARS — max chars"
	case setWorkers:
		m.textInput.SetValue(fmt.Sprintf("%d", m.workers))
		m.textInput.Focus()
		m.editingField = "WORKERS — concurrency (1–100)"
	case setBinary:
		m.openOptionPicker("binary", core.BinaryStorageIDs(), core.BinaryStorageLabels(), m.binaryStorage)
		return
	case setEvalPrompt:
		m.openEvalPrompt()
		return
	}
}

func (m *model) saveField() {
	defer func() { m.textInput.EchoMode = textinput.EchoNormal }()
	val := m.textInput.Value()
	if m.editingMultiline {
		val = m.textArea.Value()
	}

	// Handle new export template creation (before the generic "New " case).
	if strings.HasPrefix(m.editingField, "New export template: ") {
		name := strings.TrimSpace(val)
		if name == "" {
			m.openExportTemplates()
			return
		}
		if core.ExportTemplateExists(m.db, name) {
			m.phaseErr = fmt.Errorf("template %q already exists", name)
			m.openExportTemplates()
			return
		}
		_ = core.SaveExportTemplate(m.db, name, core.DefaultExportTemplateBody)
		m.openExportTemplateDetail(name)
		return
	}

	// Handle process rename.
	if strings.HasPrefix(m.editingField, "Rename process: ") {
		oldName := strings.TrimPrefix(m.editingField, "Rename process: ")
		newName := strings.TrimSpace(val)
		if newName != "" && newName != oldName {
			if err := core.RenamePrompt(m.db, oldName, newName); err != nil {
				m.phaseErr = err
				return
			}
			m.processName = newName
			if m.promptViewing != "" {
				m.promptViewing = newName
			}
			m.promptText = core.LoadPrompt(m.db, newName)
		}
		return
	}

	// Handle export template rename.
	if strings.HasPrefix(m.editingField, "Rename template: ") {
		oldName := strings.TrimPrefix(m.editingField, "Rename template: ")
		newName := strings.TrimSpace(val)
		if newName != "" && newName != oldName {
			if err := core.RenameExportTemplate(m.db, oldName, newName); err != nil {
				m.phaseErr = err
				return
			}
			m.openExportTemplateDetail(newName)
		}
		return
	}

	// Handle new process creation
	if strings.HasPrefix(m.editingField, "New ") {
		ptype := m.processType
		if ptype == "" {
			if strings.Contains(m.editingField, "pair") {
				ptype = "pair"
			} else {
				ptype = "single"
			}
		}
		_ = core.SavePromptWithType(m.db, val, "", ptype)
		m.openProcessList(ptype)
		return
	}

	// Handle prompt editing
	if strings.HasPrefix(m.editingField, "Prompt: ") {
		name := strings.TrimPrefix(m.editingField, "Prompt: ")
		_ = core.SavePrompt(m.db, name, val)
		m.promptViewing = name
		m.promptText = val
		// stay on the process detail view
		return
	}

	// Handle export template body editing
	if strings.HasPrefix(m.editingField, "Template: ") {
		name := strings.TrimPrefix(m.editingField, "Template: ")
		_ = core.SaveExportTemplate(m.db, name, val)
		m.templateName = name
		m.templateText = val
		return
	}

	switch m.cursor {
	case setSrc:
		m.src = val
	case setOut:
		m.out = val
	case setDB:
		m.dbPath = val
	case setModel:
		m.modelName = val
	case setVision:
		m.visionModel = val
	case setEmbedding:
		m.embeddingModel = core.NormalizeEmbeddingModel(val)
	case setGoogle:
		m.googleAPIKey = val
	case setHost:
		m.host = val
	case setMaxChars:
		if n, _ := fmt.Sscanf(val, "%d", &m.maxChars); n != 1 || m.maxChars < 1 {
			m.maxChars = core.DefaultAppSettings().MaxChars
		}
	case setWorkers:
		if n, _ := fmt.Sscanf(val, "%d", &m.workers); n != 1 || m.workers < 1 {
			m.workers = core.DefaultAppSettings().Workers
		}
		if m.workers > 100 {
			m.workers = 100
		}
	case setWhisperDevice:
		if n, _ := fmt.Sscanf(val, "%d", &m.whisperDevice); n != 1 || m.whisperDevice < 0 {
			m.whisperDevice = core.DefaultWhisperDevice
		}
	}
	m.persistSettings()
}

func (m *model) openEvalPrompt() {
	m.promptViewing = "eval"
	m.promptText = core.LoadPrompt(m.db, m.promptViewing)
	m.cursor = 0
}

func (m *model) promptKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "r", "R":
		if text, err := core.ReloadPromptFromFile(m.db, m.promptViewing); err == nil {
			m.promptText = text
		}
	case "e", "E":
		m.startMultilineEdit("Prompt: "+m.promptViewing, m.promptText)
	case "esc":
		m.promptViewing = ""
		m.promptText = ""
		m.cursor = setEvalPrompt
	}
	return nil
}

// ─── process submenu ────────────────────────────────────────────────────────

func (m *model) openProcessList(ptype string) {
	names, _ := core.ListPromptsByType(m.db, ptype)
	if len(names) == 0 {
		// fallback to defaults
		if ptype == "pair" {
			names = []string{core.DefaultPairPrompt}
		} else {
			names = []string{"tag_single"}
		}
	}
	m.processType = ptype
	m.processList = names
	m.processName = ""
	m.cursor = 0
	m.screen = scrProcessList
}

func (m *model) processListKeys(msg tea.KeyMsg) tea.Cmd {
	if m.editingField != "" {
		// editing a new process prompt
		switch msg.String() {
		case "enter":
			// save the new prompt
			name := strings.TrimPrefix(m.editingField, "New "+m.processType+" process: ")
			val := m.textInput.Value()
			_ = core.SavePromptWithType(m.db, name, val, m.processType)
			m.editingField = ""
			m.openProcessList(m.processType)
			return nil
		case "esc":
			m.editingField = ""
			m.openProcessList(m.processType)
			return nil
		}
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return cmd
	}

	// Both menus end with an "Add new" row.
	max := len(m.processList) // last item is "add new"
	switch vimNav(msg.String()) {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < max {
			m.cursor++
		}
	case "enter":
		switch {
		case m.cursor < len(m.processList):
			// open process detail
			m.processName = m.processList[m.cursor]
			m.cursor = 0
			m.processConfirmDelete = false
			m.screen = scrProcessDetail
		default:
			// add new process
			m.textInput.SetValue("")
			m.textInput.Focus()
			m.editingMultiline = false
			m.editingField = "New " + m.processType + " process: "
		}
	case "esc":
		m.screen = scrMenu
		m.cursor = 1
		if m.processType == "single" {
			m.cursor = 2
		}
		m.processType = ""
		m.processList = nil
	}
	return nil
}

// processDetailKeys handles the per-process actions screen.
func (m *model) processDetailKeys(msg tea.KeyMsg) tea.Cmd {
	if m.promptViewing != "" {
		return m.promptDetailKeys(msg)
	}
	if m.processConfirmDelete {
		switch msg.String() {
		case "y", "Y":
			_ = core.DeletePrompt(m.db, m.processName)
			m.processConfirmDelete = false
			m.openProcessList(m.processType)
			return nil
		default:
			m.processConfirmDelete = false
		}
		return nil
	}
	switch vimNav(msg.String()) {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < 4 {
			m.cursor++
		}
	case "enter":
		switch m.cursor {
		case 0: // run
			var phase core.Phase
			if m.processType == "pair" {
				phase = core.PhaseAnalyze
			} else {
				phase = core.PhaseTagSingle
			}
			return m.startPhase(phase, false, false, m.processName, "")
		case 1: // edit prompt
			m.promptViewing = m.processName
			m.promptText = core.LoadPrompt(m.db, m.promptViewing)
			m.cursor = 0
		case 2: // rename
			m.textInput.SetValue(m.processName)
			m.textInput.Focus()
			m.editingMultiline = false
			m.editingField = "Rename process: " + m.processName
		case 3: // delete
			m.processConfirmDelete = true
		case 4: // back
			m.openProcessList(m.processType)
		}
	case "esc":
		m.openProcessList(m.processType)
	}
	return nil
}

func (m *model) promptDetailKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "e", "E":
		m.startMultilineEdit("Prompt: "+m.promptViewing, m.promptText)
		return nil
	case "r", "R":
		if text, err := core.ReloadPromptFromFile(m.db, m.promptViewing); err == nil {
			m.promptText = text
		}
	case "esc":
		m.promptViewing = ""
	}
	return nil
}

// ─── export templates ───────────────────────────────────────────────────────

func (m *model) openExportTemplates() {
	names, _ := core.ListExportTemplates(m.db)
	if len(names) == 0 {
		names = []string{core.DefaultExportTemplate}
	}
	m.templateList = names
	m.templateName = ""
	m.cursor = 0
	m.screen = scrExportTemplates
}

func (m *model) exportTemplatesKeys(msg tea.KeyMsg) tea.Cmd {
	max := len(m.templateList) // last item is "add new"
	switch vimNav(msg.String()) {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < max {
			m.cursor++
		}
	case "enter":
		if m.cursor < len(m.templateList) {
			m.openExportTemplateDetail(m.templateList[m.cursor])
		} else {
			m.textInput.SetValue("")
			m.textInput.Focus()
			m.editingMultiline = false
			m.editingField = "New export template: "
		}
	case "esc":
		m.screen = scrMenu
		m.cursor = 3
		m.templateList = nil
	}
	return nil
}

func (m *model) openExportTemplateDetail(name string) {
	m.templateName = name
	m.templateText = core.LoadExportTemplate(m.db, name)
	m.templateConfirmDelete = false
	m.cursor = 0
	m.screen = scrExportTemplateDetail
}

func (m *model) exportTemplateDetailKeys(msg tea.KeyMsg) tea.Cmd {
	if m.templateConfirmDelete {
		switch msg.String() {
		case "y", "Y":
			_ = core.DeleteExportTemplate(m.db, m.templateName)
			m.templateConfirmDelete = false
			m.openExportTemplates()
		default:
			m.templateConfirmDelete = false
		}
		return nil
	}
	switch vimNav(msg.String()) {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < 4 {
			m.cursor++
		}
	case "enter":
		switch m.cursor {
		case 0: // export with this template
			return m.startPhase(core.PhaseWrite, false, true, "", m.templateName)
		case 1: // edit template body
			m.startMultilineEdit("Template: "+m.templateName, m.templateText)
		case 2: // rename
			m.textInput.SetValue(m.templateName)
			m.textInput.Focus()
			m.editingMultiline = false
			m.editingField = "Rename template: " + m.templateName
		case 3: // delete
			m.templateConfirmDelete = true
		case 4: // back
			m.openExportTemplates()
		}
	case "esc":
		m.openExportTemplates()
	}
	return nil
}

// ─── filesystem search for compare ──────────────────────────────────────────

type fsSearchMsg struct {
	files  []string
	err    error
	filter string
}

func (m *model) openFsSearch() {
	m.fsFilter = ""
	m.fsFiles = nil
	m.cursor = 0
	m.filterTyping = false
	m.screen = scrFsSearch
}

func (m *model) fsSearchKeys(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.filterTyping {
		switch key {
		case "esc":
			m.filterTyping = false
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.fsFiles)-1 {
				m.cursor++
			}
		case "enter":
			return m.compareSelectedFile()
		case "backspace":
			r := []rune(m.fsFilter)
			if len(r) > 0 {
				m.fsFilter = string(r[:len(r)-1])
			}
			m.cursor = 0
			return fsSearchCmd(m.fsFilter)
		default:
			if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
				m.fsFilter += string(msg.Runes[0])
				m.cursor = 0
				return fsSearchCmd(m.fsFilter)
			}
		}
		return nil
	}

	switch vimNav(key) {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.fsFiles)-1 {
			m.cursor++
		}
	case "enter":
		return m.compareSelectedFile()
	case "esc", "q":
		m.screen = scrData
		m.dataCursor = 1
	case "backspace":
		r := []rune(m.fsFilter)
		if len(r) > 0 {
			m.fsFilter = string(r[:len(r)-1])
		}
		m.cursor = 0
		return fsSearchCmd(m.fsFilter)
	default:
		if key == "/" {
			m.filterTyping = true
			return nil
		}
		if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
			m.filterTyping = true
			m.fsFilter += string(msg.Runes[0])
			m.cursor = 0
			return fsSearchCmd(m.fsFilter)
		}
	}
	return nil
}

func (m *model) compareSelectedFile() tea.Cmd {
	if len(m.fsFiles) == 0 || m.cursor >= len(m.fsFiles) {
		return nil
	}
	path := m.fsFiles[m.cursor]
	m.evalResult = ""
	m.evalErr = ""
	m.evalLoading = true
	m.evalReturn = scrFsSearch
	m.evalReturnCursor = m.cursor
	m.filterTyping = false
	m.screen = scrEval
	return compareFileCmd(m.db, path, m.host, m.modelName, m.audioOptions(), m.maxChars)
}

func fsSearchCmd(filter string) tea.Cmd {
	return func() tea.Msg {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		files, err := core.FindFiles(cwd, filter)
		return fsSearchMsg{files: files, err: err, filter: filter}
	}
}
