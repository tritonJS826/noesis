package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"noesis/internal/core"
)

func (m *model) View() string {
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
	case scrOptionPicker:
		return m.viewOptionPicker()
	case scrWhisper:
		return m.viewWhisper()
	case scrAudioHelp:
		return m.viewAudioHelp()
	case scrRunning:
		return m.viewRunning()
	case scrImport:
		return m.viewSync()
	case scrSync:
		return m.viewSync()
	case scrFormats:
		return m.viewFormats()
	case scrData:
		return m.viewData()
	case scrPickData:
		return m.viewPickData()
	case scrComparePath:
		return m.viewComparePath()
	case scrFilePicker:
		return m.viewFilePicker()
	case scrEval:
		return m.viewEval()
	case scrProcessList:
		return m.viewProcessList()
	case scrProcessDetail:
		return m.viewProcessDetail()
	case scrFsSearch:
		return m.viewFsSearch()
	case scrExportTemplates:
		return m.viewExportTemplates()
	case scrExportTemplateDetail:
		return m.viewExportTemplateDetail()
	case scrBrowse:
		return m.viewBrowse()
	case scrBrowseDetail:
		return m.viewBrowseDetail()
	case scrExtract:
		return m.viewExtract()
	case scrRAGSearch:
		return m.viewRAGSearch()
	default:
		return m.viewMenu()
	}
}

func (m *model) viewMenu() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	w := m.width
	cw := w - 4 // inside border+padding

	var s core.Stats
	if m.db != nil {
		s = core.LoadStats(m.db)
	}
	n := s.Notes
	pairsTotal := n * (n - 1) / 2

	items := []string{
		"Synchronize",
		"Data processing — pairs",
		"Data processing — one by one",
		"Export for Obsidian",
		"Work with data",
		"Settings",
	}

	var phaseLines []string
	for i, item := range items {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		line := cursor + item
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
		tuiLabel("AUDIO", core.AudioEngineLabel(m.audioEngine)),
		tuiLabel("WHISPER", m.whisperSummary()),
		tuiLabel("LANG", core.SpeechLanguageLabel(m.speechLanguage)),
		tuiLabel("HOST", m.host),
		fmt.Sprintf("MAXCHARS: %d", m.maxChars),
		fmt.Sprintf("WORKERS: %d", m.workers),
	}
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
	ragNotes, ragChunks, _ := core.RAGStats(m.db)
	ragLine := "RAG: not indexed"
	if ragChunks > 0 {
		ragLine = fmt.Sprintf("RAG: %d note(s) │ %d chunk(s)", ragNotes, ragChunks)
	}
	statsLines := []string{
		fmt.Sprintf("Notes: %d │ With tags: %d │ Tags: %d │ Links: %d", s.Notes, s.Tagged, s.UniqTags, s.Links),
		fmt.Sprintf("Pairs: %d/%d │ Written: %d", s.PairsDone, pairsTotal, s.Written),
		ragLine,
	}
	formatLines := formatStatsLines(core.LoadFormatStats(m.db), cw)

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
	footer := "↑↓/jk select  ·  Enter/l run  ·  Q quit"
	if m.confirmReset {
		footer = "y — delete all  ·  any other key — cancel"
	}

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
	parts = append(parts, sep("Formats"))
	if len(formatLines) == 0 {
		parts = append(parts, tuiDim.Width(cw).Render("no files imported yet"))
	}
	for _, fl := range formatLines {
		parts = append(parts, lipgloss.NewStyle().Width(cw).Render(fl))
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

func (m *model) viewImport() string {
	w := m.width
	cw := w - 4

	srcLabel := m.importSource.String()
	formatsLabel := m.formatSummary()
	items := []string{
		"Source: " + srcLabel,
		"Formats: " + formatsLabel,
		"Run import",
	}

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Import (Phase 1) — choose source & formats"))
	lines = append(lines, "")
	for i, item := range items {
		cursor := "  "
		if m.importCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+item))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓/jk select  ·  Enter/l change/run  ·  h/ESC back"))
	lines = append(lines, tuiDim.Width(cw).Render("SRC — filesystem folder, .zip archive or .json file"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) formatSummary() string {
	if m.importFormats == nil {
		return "all"
	}
	if len(m.importFormats) == 0 {
		return "none"
	}
	var exts []string
	for e := range m.importFormats {
		exts = append(exts, "."+e)
	}
	sortStrings(exts)
	return strings.Join(exts, " ")
}

func (m *model) viewSync() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	w := m.width
	cw := w - 4

	srcLabel := m.syncSource.String()
	formatsLabel := m.formatSummary()

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Synchronize — choose source & formats"))
	lines = append(lines, tuiDim.Width(cw).Render("SRC: "+tuiEmpty(m.src)))
	lines = append(lines, "")

	// Settings submenu: source type, formats, action
	settingsItems := []string{
		"Source: " + srcLabel,
		"Formats: " + formatsLabel,
	}
	if m.syncSource == core.SourceFilesystem {
		if len(m.syncItems) == 0 && m.syncChecked == nil {
			settingsItems = append(settingsItems, "Run comparison")
		} else {
			settingsItems = append(settingsItems, "Refresh comparison")
		}
	} else {
		settingsItems = append(settingsItems, "Import all")
	}

	for i, item := range settingsItems {
		cursor := "  "
		if m.syncCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+item))
	}
	lines = append(lines, "")

	// fixedAbove = title + SRC + blank + settings rows + blank. These stay
	// visible: the diff list below is windowed to whatever space is left so the
	// sync settings can always be adjusted.
	fixedAbove := len(lines)

	// Show diff results if available (folder source only).
	if len(m.syncItems) > 0 {
		lines = append(lines, tuiDim.Width(cw).Render(fmt.Sprintf("%d differences found", len(m.syncItems))))
		lines = append(lines, tuiDim.Width(cw).Render("space toggle selected  ·  a select/unselect all  ·  enter apply"))
		lines = append(lines, "")
		const listHeader, listFooter = 3, 2 // header lines above, blank+hint below

		// Rows available for the diff list after the fixed chrome.
		page := m.listRows(fixedAbove + listHeader + listFooter)
		if m.height <= 0 {
			// Size not known yet (first frame): show everything.
			page = len(m.syncItems)
		}
		if page < 1 {
			page = 1
		}

		// Diff items start after the settings rows.
		itemStart := len(settingsItems)
		sCursor := m.syncCursor - itemStart
		if sCursor < 0 {
			sCursor = 0
		}
		if sCursor >= len(m.syncItems) {
			sCursor = len(m.syncItems) - 1
		}
		start, end := listWindow(len(m.syncItems), sCursor, page)
		m.syncScroll = start

		for i := start; i < end; i++ {
			it := m.syncItems[i]
			cursor := "  "
			if sCursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			key := syncItemKey(it)
			checked := m.syncChecked[key]
			mark := "[ ]"
			if checked {
				mark = tuiSel.Render("[✓]")
			}
			prefix := "+"
			if it.Deleted {
				prefix = tuiRed.Render("−")
			}
			name := it.Name
			rs := []rune(name)
			if len(rs) > cw-12 {
				name = string(rs[:cw-15]) + "…"
			}
			line := fmt.Sprintf("%s%s %s %s", cursor, mark, prefix, name)
			lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(line))
		}
		lines = append(lines, "")
		hint := fmt.Sprintf("%s   ↑↓/jk move  ·  space/s toggle  ·  a all  ·  enter apply  ·  h/ESC back",
			scrollHint(start, end, len(m.syncItems)))
		lines = append(lines, tuiDim.MaxWidth(cw).Render(hint))
	} else if m.syncChecked != nil {
		// We've run diff but found no changes.
		lines = append(lines, tuiDim.Width(cw).Render("No differences found — everything is in sync."))
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("r refresh  ·  s run sync  ·  h/ESC back"))
	} else {
		lines = append(lines, tuiDim.Width(cw).Render("SRC can be a folder, a .zip archive or a .json file."))
		lines = append(lines, tuiDim.Width(cw).Render("↑↓/jk move  ·  enter/l select  ·  h/ESC back"))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func syncItemKey(it core.SyncItem) string {
	if it.Deleted {
		return "del:" + it.Path
	}
	return "new:" + it.Path
}

func (m *model) viewFormats() string {
	w := m.width
	cw := w - 4

	formats := core.KnownFormats()
	rows := m.listRows(5)
	start, end := listWindow(len(formats), m.fmtCursor, rows)

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).MaxWidth(cw).Render("Select formats to import"))
	scroll := scrollHint(start, end, len(formats))
	switch {
	case m.importFormats == nil:
		lines = append(lines, tuiDim.MaxWidth(cw).Render(scroll+"  ·  current: all formats"))
	case len(m.importFormats) == 0:
		lines = append(lines, tuiYel.MaxWidth(cw).Render(scroll+"  ·  current: none"))
	default:
		lines = append(lines, tuiDim.MaxWidth(cw).Render(
			fmt.Sprintf("%s  ·  %d selected", scroll, len(m.importFormats)),
		))
	}
	lines = append(lines, "")

	selected := m.importFormats
	if selected == nil {
		selected = core.FormatSet{}
		for _, f := range formats {
			selected[f.Ext] = true
		}
	}

	for i := start; i < end; i++ {
		f := formats[i]
		cursor := "  "
		mark := "[ ]"
		if selected[f.Ext] {
			mark = "[x]"
		}
		if m.fmtCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(
			fmt.Sprintf("%s%s  %-10s (%s)", cursor, mark, f.Ext, f.Kind),
		))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.MaxWidth(cw).Render("↑↓/jk move · Enter/l toggle · a all/none · h/ESC done"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewData() string {
	w := m.width
	cw := w - 4

	items := []string{
		"Browse database",
		"Compare file (filesystem search)",
		"Re-extract documents, images & transcribe audio",
		"Search RAG (semantic)",
		"★ Evaluate note uniqueness",
	}

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Work with data"))
	lines = append(lines, "")
	for i, item := range items {
		cursor := "  "
		if m.dataCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+item))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓/jk select  ·  Enter/l  ·  h/ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewExtract() string {
	w := m.width
	cw := w - 4

	formats := core.ExtractableFormats()
	rows := m.listRows(9)
	if rows < 1 {
		rows = 10
	}
	start, end := listWindow(len(formats), m.extractCursor, rows)

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Re-extract documents, images & transcribe audio"))
	lines = append(lines, tuiDim.Width(cw).Render("Markdown is regenerated for every matched file. Documents and audio use the local extractors; images are described by the vision model."))
	lines = append(lines, "")

	// Name filter.
	filterVal := m.extractFilter
	if filterVal == "" {
		if m.extractTyping {
			filterVal = "█"
		} else {
			filterVal = tuiDim.Render("(all files)")
		}
	} else if m.extractTyping {
		filterVal += "█"
	}
	lines = append(lines, lipgloss.NewStyle().Width(cw).Render("Name filter: "+filterVal))
	lines = append(lines, tuiDim.MaxWidth(cw).Render(
		scrollHint(start, end, len(formats))+"  ·  selected: "+m.extractFormatSummary()))
	lines = append(lines, "")

	selected := m.extractFormats
	if len(formats) == 0 {
		lines = append(lines, tuiDim.Width(cw).Render("no extractable formats"))
	}
	for i := start; i < end; i++ {
		f := formats[i]
		cursor := "  "
		if m.extractCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		mark := "[ ]"
		if selected == nil || selected[f.Ext] {
			mark = "[x]"
		}
		lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(
			fmt.Sprintf("%s%s  %-10s (%s)", cursor, mark, f.Ext, f.Kind)))
	}

	lines = append(lines, "")
	if m.extractTyping {
		lines = append(lines, tuiDim.Width(cw).Render("typing name filter  ·  Enter done  ·  ESC stop"))
	} else {
		lines = append(lines, tuiDim.Width(cw).Render("↑↓/jk move  ·  space/Enter toggle  ·  e or / name filter  ·  a all/none  ·  r run  ·  h/ESC back"))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

// extractFormatSummary describes the current format selection.
func (m *model) extractFormatSummary() string {
	if m.extractFormats == nil {
		return "all formats"
	}
	return fmt.Sprintf("%d of %d formats", len(m.extractFormats), len(core.ExtractableFormats()))
}

// viewRAGSearch is the semantic search screen: type a query, then search or ask.
func (m *model) viewRAGSearch() string {
	w := m.width
	cw := w - 4

	lines := m.ragHeaderLines(cw)

	// Rows available inside the box border. The header and footer are pinned;
	// the feedback block (answer / confirm / error) and the results list share
	// whatever is left. The height is unknown on the very first frame, so the
	// limits are skipped until the terminal reports its size.
	known := m.height > 0
	inner := m.height - 2
	if known && inner < 1 {
		inner = 1
	}
	const footer = 2 // blank line + hint
	body := inner - len(lines) - footer
	if !known {
		body = -1 // unlimited
	}

	hasResults := len(m.ragResults) > 0
	// Reserve the count line plus one two-line result, but only when there is
	// enough room for the feedback block as well.
	reservedResults := 0
	if hasResults && (!known || body >= 5) {
		reservedResults = 3
	}
	fbBudget := body - reservedResults
	if fbBudget < 0 {
		fbBudget = 0
	}

	var feedback []string
	switch {
	case m.ragConfirm:
		prompt := fmt.Sprintf("Model(s) not installed: %s. Download now? (y/N)", strings.Join(m.ragConfirmModels, ", "))
		for _, l := range wrapText(prompt, cw) {
			feedback = append(feedback, tuiRed.MaxWidth(cw).Render(l))
		}
		feedback = append(feedback, "")
	case m.ragBusy:
		feedback = append(feedback, tuiDim.Width(cw).Render("searching…"))
	case m.ragErr != "":
		for _, l := range wrapText("Error: "+m.ragErr, cw) {
			feedback = append(feedback, tuiRed.MaxWidth(cw).Render(l))
		}
	case m.ragAnswer != "":
		feedback = m.ragAnswerLines(cw, fbBudget)
	}
	if known && len(feedback) > fbBudget {
		feedback = feedback[:fbBudget]
	}
	lines = append(lines, feedback...)

	if hasResults {
		// Rows left for the list after the feedback that was actually shown.
		resultsBudget := body - len(feedback)
		if !known {
			resultsBudget = 1 + len(m.ragResults)*2
		}
		if resultsBudget >= 3 { // count line + at least one two-line result
			window := (resultsBudget - 1) / 2
			if window < 1 {
				window = 1
			}
			start, end := listWindow(len(m.ragResults), m.cursor, window)
			lines = append(lines, tuiDim.MaxWidth(cw).Render(fmt.Sprintf(
				"%s  ·  %d result(s)  ·  up to %d",
				scrollHint(start, end, len(m.ragResults)), len(m.ragResults), m.ragTopK)))
			for i := start; i < end; i++ {
				r := m.ragResults[i]
				cursor := "  "
				if m.cursor == i {
					cursor = tuiSel.Render("▸ ")
				}
				lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(
					fmt.Sprintf("%s[%d] %.3f  %s", cursor, i+1, r.Score, r.Path)))
				snippet := strings.Join(strings.Fields(r.Chunk), " ")
				if rs := []rune(snippet); len(rs) > cw-6 {
					snippet = string(rs[:cw-6]) + "…"
				}
				lines = append(lines, tuiDim.MaxWidth(cw).Render("     "+snippet))
			}
		}
	}

	lines = append(lines, "")
	hint := fmt.Sprintf("↑↓/jk move  ·  Enter open note  ·  a ask  ·  +/- results (%d)  ·  / new search  ·  h/ESC back", m.ragTopK)
	switch {
	case m.ragConfirm:
		hint = "y — download & search  ·  any other key — cancel"
	case m.ragTyping:
		hint = "type query  ·  Enter search  ·  Ctrl+A search + ask  ·  ESC clear"
	case m.ragAnswer != "":
		hint = fmt.Sprintf("PgUp/PgDn answer  ·  ↑↓/jk move  ·  Enter open note  ·  a ask  ·  +/- results (%d)  ·  h/ESC back", m.ragTopK)
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render(hint))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

// ragHeaderLines builds the pinned top of the semantic-search screen: the
// title, the indexing status, the query line and the spacers around them.
func (m *model) ragHeaderLines(cw int) []string {
	idxNotes, idxChunks, _ := core.RAGStats(m.db)
	model := core.NormalizeEmbeddingModel(m.embeddingModel)

	status := "Embedding model: " + model
	if idxChunks > 0 {
		status += fmt.Sprintf("  ·  indexed: %d note(s) / %d chunk(s)", idxNotes, idxChunks)
	} else {
		status += "  ·  index empty — run the \"RAG index\" process"
	}

	query := m.ragQuery
	if query == "" {
		if m.ragTyping {
			query = "█"
		} else {
			query = tuiDim.Render("press / and type a question…")
		}
	} else if m.ragTyping {
		query += "█"
	}

	return []string{
		lipgloss.NewStyle().Bold(true).Width(cw).Render("Semantic search (RAG)"),
		tuiDim.MaxWidth(cw).Render(status),
		"",
		lipgloss.NewStyle().Width(cw).Render("Query: " + query),
		"",
	}
}

// ragAnswerLines renders the answer inside the given row budget so it never
// overflows the terminal. When the text does not fit it is windowed at
// ragAnswerOffset and the header shows the position (PgUp/PgDn scroll it). An
// unknown height shows the whole answer; a zero budget shows just the header.
func (m *model) ragAnswerLines(cw, budget int) []string {
	all := wrapText(m.ragAnswer, cw)

	if m.height <= 0 {
		out := []string{lipgloss.NewStyle().Bold(true).Width(cw).Render("Answer:")}
		for _, l := range all {
			out = append(out, lipgloss.NewStyle().MaxWidth(cw).Render(l))
		}
		return out
	}

	avail := budget - 1 // the header takes one row
	if avail < 0 {
		avail = 0
	}
	total := len(all)

	start := m.ragAnswerOffset
	if max := total - avail; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > total {
		end = total
	}

	header := "Answer:"
	switch {
	case avail == 0:
		header = fmt.Sprintf("Answer:  (%d line(s) — terminal too small)", total)
	case total > avail:
		header = fmt.Sprintf("Answer:  (%d–%d of %d, PgUp/PgDn)", start+1, end, total)
	}

	out := []string{lipgloss.NewStyle().Bold(true).Width(cw).Render(header)}
	for _, l := range all[start:end] {
		out = append(out, lipgloss.NewStyle().MaxWidth(cw).Render(l))
	}
	return out
}

// ragAnswerVisible returns how many answer lines fit on screen, mirroring the
// layout in viewRAGSearch. The keys use it to clamp the answer scroll offset.
func (m *model) ragAnswerVisible() int {
	cw := m.width - 4
	total := len(wrapText(m.ragAnswer, cw))
	if m.height <= 0 {
		return total
	}
	inner := m.height - 2
	if inner < 1 {
		inner = 1
	}
	body := inner - len(m.ragHeaderLines(cw)) - 2
	if body < 0 {
		body = 0
	}
	reserved := 0
	if len(m.ragResults) > 0 && body >= 5 {
		reserved = 3
	}
	fbBudget := body - reserved
	if fbBudget < 0 {
		fbBudget = 0
	}
	if avail := fbBudget - 1; avail > 0 {
		return avail
	}
	return 0
}

func (m *model) browseHeader() string {
	var parts []string
	for i, label := range browseColLabels {
		if m.browseCols[i] {
			parts = append(parts, label)
		}
	}
	if len(parts) == 0 {
		return "File"
	}
	return strings.Join(parts, "  │  ")
}

func (m *model) browseRow(n core.NoteDetail) string {
	var parts []string
	if m.browseCols[0] {
		parts = append(parts, n.FilePath)
	}
	if m.browseCols[1] {
		parts = append(parts, n.Format())
	}
	if m.browseCols[2] {
		parts = append(parts, n.ImportedAt)
	}
	if m.browseCols[3] {
		tags := n.Tags
		if tags == "" {
			tags = "—"
		}
		parts = append(parts, tags)
	}
	if m.browseCols[4] {
		parts = append(parts, fmt.Sprintf("%d", n.Links))
	}
	if len(parts) == 0 {
		return n.FileName
	}
	return strings.Join(parts, "  │  ")
}

func (m *model) viewBrowse() string {
	w := m.width
	cw := w - 4

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Browse database"))
	lines = append(lines, "")

	filterLine := m.browseFilter
	if filterLine == "" {
		if m.filterTyping {
			filterLine = "█"
		} else {
			filterLine = tuiDim.Render("press / to search by path, format or tags…")
		}
	} else if m.filterTyping {
		filterLine += "█"
	}
	lines = append(lines, lipgloss.NewStyle().Width(cw).Render(filterLine))

	var colParts []string
	for i, label := range browseColLabels {
		mark := "[ ]"
		if m.browseCols[i] {
			mark = "[x]"
		}
		colParts = append(colParts, fmt.Sprintf("F%d %s%s", i+1, label, mark))
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render("Columns: "+strings.Join(colParts, "  ·  ")))
	lines = append(lines, "")

	filtered := m.filteredBrowse()
	if m.browseConfirm {
		lines = append(lines, tuiYel.Width(cw).Render(m.browseResult))
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("y — confirm  ·  any other key — cancel"))
	} else if m.browseErr != "" {
		lines = append(lines, tuiRed.Width(cw).Render("Error: "+m.browseErr))
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("Press any key to continue"))
	} else {
		if len(filtered) == 0 {
			lines = append(lines, tuiDim.Width(cw).Render("nothing found"))
		} else {
			lines = append(lines, lipgloss.NewStyle().Bold(true).MaxWidth(cw).Render(m.browseHeader()))
			start, end := listWindow(len(filtered), m.cursor, m.listRows(6))
			for i := start; i < end; i++ {
				cursor := "  "
				if m.cursor == i {
					cursor = tuiSel.Render("▸ ")
				}
				lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+m.browseRow(filtered[i])))
			}
		}
		if m.browseResult != "" {
			lines = append(lines, "")
			lines = append(lines, tuiGreen.Width(cw).Render(m.browseResult))
		}
	}

	lines = append(lines, "")
	hint := "↑↓/jk select  ·  PgUp/PgDn page  ·  Enter/l details  ·  Del delete  ·  / search  ·  h/ESC back  ·  F1-F5 columns"
	if m.filterTyping {
		hint = "typing filter  ·  Enter details  ·  ↑↓ select  ·  ESC stop search"
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render(fmt.Sprintf("found: %d  ·  %s", len(filtered), hint)))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

// viewBrowseDetail shows every notes-table column of the selected record.
func (m *model) viewBrowseDetail() string {
	w := m.width
	cw := w - 4

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).MaxWidth(cw).Render("Record: "+m.browseDetailPath))
	lines = append(lines, "")

	content := m.browseDetailContent()
	page := m.browseDetailPage()
	if max := m.browseDetailMaxOffset(); m.browseDetailOffset > max {
		m.browseDetailOffset = max
	}
	start := m.browseDetailOffset
	if start > len(content) {
		start = len(content)
	}
	end := start + page
	if end > len(content) {
		end = len(content)
	}
	for _, l := range content[start:end] {
		if m.browseDetailConfirm {
			lines = append(lines, tuiYel.MaxWidth(cw).Render(l.text))
			continue
		}
		style := lipgloss.NewStyle().MaxWidth(cw)
		if l.header {
			style = style.Bold(true)
		}
		lines = append(lines, style.Render(l.text))
	}

	lines = append(lines, "")
	hint := "↑↓/jk scroll  ·  PgUp/PgDn page  ·  g/G top/bottom  ·  d/Delete remove  ·  h/ESC back"
	if m.browseDetailConfirm {
		hint = "y — confirm  ·  any other key — cancel"
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render(
		fmt.Sprintf("%s  ·  %s", scrollHint(start, end, len(content)), hint),
	))
	body := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(body)
}

func (m *model) viewPickData() string {
	w := m.width
	cw := w - 4

	var lines []string
	title := "Remove file — search by path"
	if m.pickMode == 1 {
		title = "Remove file + obsidian links — search by path"
	}
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render(title))
	lines = append(lines, "")

	// filter line
	filterLine := m.fileFilter
	if filterLine == "" {
		filterLine = tuiDim.Render("start typing to filter by path…")
	} else {
		filterLine += "█"
	}
	lines = append(lines, lipgloss.NewStyle().Width(cw).Render(filterLine))
	lines = append(lines, "")

	filtered := m.filteredNotes()
	if m.pickConfirm {
		lines = append(lines, tuiYel.Width(cw).Render(m.pickResult))
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("y — confirm  ·  any other key — cancel"))
	} else if m.pickErr != "" {
		lines = append(lines, tuiRed.Width(cw).Render("Error: "+m.pickErr))
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("Press any key to continue"))
	} else if m.pickResult != "" {
		lines = append(lines, tuiGreen.Width(cw).Render(m.pickResult))
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("Press any key to continue"))
	} else if len(filtered) == 0 {
		lines = append(lines, tuiDim.Width(cw).Render("nothing found"))
	} else {
		start, end := listWindow(len(filtered), m.cursor, m.listRows(6))
		for i := start; i < end; i++ {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+filtered[i].Path))
		}
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.MaxWidth(cw).Render(fmt.Sprintf("found: %d  ·  ↑↓ select  ·  Enter remove  ·  ESC back", len(filtered))))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewComparePath() string {
	w := m.width
	cw := w - 4

	filterLine := m.comparePath
	if filterLine == "" {
		filterLine = tuiDim.Render("type the file path to compare…")
	} else {
		filterLine += "█"
	}

	lines := []string{
		lipgloss.NewStyle().Bold(true).Width(cw).Render("Compare file (without adding to db)"),
		"",
		lipgloss.NewStyle().Width(cw).Render(filterLine),
		"",
		tuiDim.Width(cw).Render("Enter evaluate  ·  ESC back"),
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewSettings() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	if m.promptViewing != "" {
		return m.viewPromptSub()
	}
	if m.confirmReset {
		return m.viewResetConfirm()
	}
	if m.confirmClearRAG {
		return m.viewClearRAGConfirm()
	}
	w := m.width
	cw := w - 4

	keys := []string{"SRC", "OUT", "DB", "MODEL", "VISION MODEL", "EMBEDDING MODEL", "AUDIO", "WHISPER", "WHISPER GPU", "VULKAN DEVICE", "GOOGLE KEY", "LANG", "HOST", "MAXCHARS", "WORKERS", "BINARY STORAGE", "EVAL PROMPT", "Reset embeddings", "Reset database"}
	googleKey := "(not set)"
	if strings.TrimSpace(m.googleAPIKey) != "" {
		googleKey = "(set)"
	}
	vision := m.visionModel
	if strings.TrimSpace(vision) == "" {
		vision = "(= MODEL)"
	}
	vals := []string{m.src, m.out, m.dbPath, m.modelName, vision, core.NormalizeEmbeddingModel(m.embeddingModel),
		core.AudioEngineLabel(m.audioEngine), m.whisperSummary(),
		core.WhisperBackendLabel(m.whisperBackend), fmt.Sprintf("%d", m.whisperDevice), googleKey,
		core.SpeechLanguageLabel(m.speechLanguage), m.host,
		fmt.Sprintf("%d", m.maxChars), fmt.Sprintf("%d", m.workers),
		core.BinaryStorageLabel(m.binaryStorage), "edit…", "clear…", ""}

	var lines []string
	for i, k := range keys {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		style := lipgloss.NewStyle().Width(cw)
		line := fmt.Sprintf("%s%-12s: %s", cursor, k, vals[i])
		if i == setReset || i == setClearRAG {
			if m.cursor == i {
				line = cursor + tuiRed.Render(k)
			} else {
				line = style.Render("  " + tuiRed.Render(k))
			}
		} else {
			line = style.Render(line)
		}
		lines = append(lines, line)
	}
	if sug := m.embeddingSuggestion(); sug != "" {
		lines = append(lines, "")
		lines = append(lines, tuiYel.Width(cw).Render("EMBEDDING MODEL is "+sug))
	}
	switch {
	case m.clearingRAG:
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("Clearing RAG embeddings…"))
	case m.clearRAGErr != "":
		lines = append(lines, "")
		lines = append(lines, tuiRed.Width(cw).Render("Reset embeddings error: "+m.clearRAGErr))
	case m.clearRAGStatus != "":
		lines = append(lines, "")
		lines = append(lines, tuiGreen.Width(cw).Render(m.clearRAGStatus))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓/jk select  ·  Enter/l edit  ·  ? audio setup help  ·  h/ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

// embeddingSuggestion returns a hint when the selected embedding model is not
// installed on the ollama host, or "" when it is (or when the model list is
// unknown).
func (m *model) embeddingSuggestion() string {
	if !m.ollamaReady || !m.ollamaOK {
		return ""
	}
	return core.EmbeddingModelSuggestion(m.ollamaModels, m.embeddingModel)
}

func (m *model) viewResetConfirm() string {
	w := m.width
	cw := w - 4
	lines := []string{
		tuiYel.Width(cw).Render("Delete database " + m.dbPath + " and all data? (y/N)"),
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewClearRAGConfirm() string {
	w := m.width
	cw := w - 4
	lines := []string{
		tuiYel.Width(cw).Render("Delete all RAG embeddings? Notes are kept; run \"RAG index\" to rebuild. (y/N)"),
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewPromptSub() string {
	w := m.width
	cw := w - 4

	lines := []string{
		lipgloss.NewStyle().Bold(true).MaxWidth(cw).Render("Prompt: " + m.promptViewing),
		"",
		tuiDim.MaxWidth(cw).Render("Variables:"),
	}
	for _, l := range wrapText(m.promptVarsHint(m.promptViewing), cw) {
		lines = append(lines, tuiDim.MaxWidth(cw).Render(l))
	}
	lines = append(lines, "")
	for _, line := range strings.Split(m.promptText, "\n") {
		lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(line))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.MaxWidth(cw).Render("ESC back  ·  e edit  ·  r reload from file"))
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewModelPicker() string {
	w := m.width
	cw := w - 4

	title := "Choose model (ollama)"
	if m.modelPickerTarget == "embedding" {
		title = "Choose embedding model (ollama)"
	}

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render(title))
	lines = append(lines, "")
	start, end := listWindow(len(m.ollamaModels), m.cursor, m.listRows(3))
	if len(m.ollamaModels) == 0 {
		lines = append(lines, tuiRed.Width(cw).Render("no models available"))
	} else {
		for i := start; i < end; i++ {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+m.ollamaModels[i]))
		}
	}
	lines = append(lines, "")
	hint := "↑↓/jk select  ·  Enter/l pick  ·  h/ESC back"
	if len(m.ollamaModels) > 0 {
		hint += "  ·  " + scrollHint(start, end, len(m.ollamaModels))
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render(hint))
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewTextEdit() string {
	w := m.width
	cw := w - 4

	var lines []string
	if m.editingMultiline {
		var hint string
		switch {
		case strings.HasPrefix(m.editingField, "Prompt: "):
			hint = m.promptVarsHint(strings.TrimPrefix(m.editingField, "Prompt: "))
		case strings.HasPrefix(m.editingField, "Template: "):
			hint = m.exportVarsHint()
		}
		if hint != "" {
			lines = append(lines, tuiDim.Width(cw).Render("Variables:"))
			for _, l := range wrapText(hint, cw) {
				lines = append(lines, tuiDim.Width(cw).Render(l))
			}
			lines = append(lines, "")
		}
		lines = append(lines,
			m.textArea.View(),
			"",
			tuiDim.Width(cw).Render("Ctrl+S save  ·  Enter new line  ·  ESC cancel"),
		)
	} else {
		lines = []string{
			lipgloss.NewStyle().Width(cw).Render(m.textInput.View()),
			"",
			tuiDim.Width(cw).Render("Enter save  ·  ESC cancel"),
		}
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewRunning() string {
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

func (m *model) viewFilePicker() string {
	w := m.width
	cw := w - 4

	var lines []string
	filterLine := m.fileFilter
	if filterLine == "" {
		if m.filterTyping {
			filterLine = "█"
		} else {
			filterLine = tuiDim.Render("press / to search notes…")
		}
	} else if m.filterTyping {
		filterLine += "█"
	}
	lines = append(lines, lipgloss.NewStyle().Width(cw).Render(filterLine))
	lines = append(lines, "")

	filtered := m.filteredNotes()
	start, end := listWindow(len(filtered), m.cursor, m.listRows(4))
	if len(filtered) == 0 {
		lines = append(lines, tuiDim.Width(cw).Render("nothing found"))
	} else {
		for i := start; i < end; i++ {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+filtered[i].Name))
		}
	}
	lines = append(lines, "")
	hint := "↑↓/jk select  ·  Enter/l evaluate  ·  / search  ·  h/ESC back"
	if m.filterTyping {
		hint = "typing filter  ·  Enter evaluate  ·  ↑↓ select  ·  ESC stop search"
	}
	footer := fmt.Sprintf("found: %d  ·  %s  ·  %s",
		len(filtered), hint, scrollHint(start, end, len(filtered)))
	lines = append(lines, tuiDim.MaxWidth(cw).Render(footer))
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewEval() string {
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

func (m *model) viewProcessList() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	w := m.width
	cw := w - 4

	label := "Pair processes"
	if m.processType == "single" {
		label = "One-by-one processes"
	}

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render(label))
	lines = append(lines, "")

	start, end := listWindow(len(m.processList), m.cursor, m.listRows(9))
	for i := start; i < end; i++ {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+m.processList[i]))
	}

	// "Add new" row stays visible below the scrolling process list.
	addCursor := "  "
	if m.cursor == len(m.processList) {
		addCursor = tuiSel.Render("▸ ")
	}
	lines = append(lines, "")
	lines = append(lines, tuiGreen.MaxWidth(cw).Render(addCursor+"+ Add new "+m.processType+" process"))

	lines = append(lines, "")
	lines = append(lines, tuiDim.MaxWidth(cw).Render(
		fmt.Sprintf("↑↓/jk select  ·  Enter/l open  ·  h/ESC back  ·  %s", scrollHint(start, end, len(m.processList))),
	))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewProcessDetail() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	if m.promptViewing != "" {
		// viewing/editing a prompt
		return m.viewProcessPrompt()
	}
	w := m.width
	cw := w - 4

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Process: "+m.processName))
	lines = append(lines, "")

	items := []string{
		"Run",
		"Edit prompt",
		"Rename",
		"Delete",
		"Back",
	}

	for i, item := range items {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		style := lipgloss.NewStyle().Width(cw)
		if m.processConfirmDelete && i == 3 {
			style = style.Foreground(lipgloss.Color("#ff0000"))
			item = "Delete — are you sure? (y/N)"
		}
		lines = append(lines, style.Render(cursor+item))
	}

	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓/jk select  ·  Enter/l  ·  h/ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewProcessPrompt() string {
	w := m.width
	cw := w - 4

	lines := []string{
		lipgloss.NewStyle().Bold(true).Width(cw).Render("Prompt: " + m.promptViewing),
		"",
		tuiDim.Width(cw).Render("Variables:"),
	}
	for _, l := range wrapText(m.promptVarsHint(m.promptViewing), cw) {
		lines = append(lines, tuiDim.Width(cw).Render(l))
	}
	lines = append(lines, "")
	for _, line := range strings.Split(m.promptText, "\n") {
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(line))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("ESC back  ·  e edit  ·  r reload from file"))
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewFsSearch() string {
	w := m.width
	cw := w - 4

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Compare file — search filesystem"))

	filterLine := m.fsFilter
	if filterLine == "" {
		if m.filterTyping {
			filterLine = "█"
		} else {
			filterLine = tuiDim.Render("press / to search files…")
		}
	} else if m.filterTyping {
		filterLine += "█"
	}
	lines = append(lines, lipgloss.NewStyle().Width(cw).Render(filterLine))
	lines = append(lines, "")

	start, end := listWindow(len(m.fsFiles), m.cursor, m.listRows(5))
	if len(m.fsFiles) == 0 && m.fsFilter != "" {
		lines = append(lines, tuiDim.Width(cw).Render("no files found"))
	} else {
		for i := start; i < end; i++ {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			// Show relative path
			display := m.fsFiles[i]
			if cwd, err := os.Getwd(); err == nil {
				display = strings.TrimPrefix(display, cwd+string(os.PathSeparator))
			}
			lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+display))
		}
	}
	lines = append(lines, "")
	hint := "↑↓/jk select  ·  Enter/l compare  ·  / search  ·  h/ESC back"
	if m.filterTyping {
		hint = "typing filter  ·  Enter compare  ·  ↑↓ select  ·  ESC stop search"
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render(fmt.Sprintf(
		"found: %d  ·  %s  ·  %s",
		len(m.fsFiles), hint, scrollHint(start, end, len(m.fsFiles)),
	)))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

// promptVarsHint lists the ${...} variables available for a prompt.
func (m *model) promptVarsHint(name string) string {
	ptype := core.LoadPromptType(m.db, name)
	var named []string
	switch {
	case ptype == "pair":
		named = []string{"${A_NAME}", "${A_CONTENT}", "${B_NAME}", "${B_CONTENT}"}
	default:
		named = []string{"${FILE_NAME}", "${CONTENT}", "${LINKED_NOTES}"}
	}
	parts := append([]string{}, named...)
	cols := core.NotesColumns(m.db)
	for _, c := range cols {
		parts = append(parts, "${db."+c+"}")
	}
	if ptype == "pair" {
		for _, c := range cols {
			parts = append(parts, "${db_b."+c+"}")
		}
	}
	return strings.Join(parts, "  ")
}

// exportVarsHint lists the ${...} variables available for export templates.
func (m *model) exportVarsHint() string {
	named := []string{
		"${frontmatter_block}", "${frontmatter}", "${body}", "${related}",
		"${links}", "${tags}", "${tags_yaml}", "${tags_inline}",
		"${name}", "${title}", "${path}",
	}
	parts := append([]string{}, named...)
	for _, c := range core.NotesColumns(m.db) {
		parts = append(parts, "${db."+c+"}")
	}
	return strings.Join(parts, "  ")
}

// formatStatsLines packs the per-format import/extraction counters into lines
// no wider than width, e.g. "3/20 .mp3 extracted  ·  89 .md ready".
func formatStatsLines(stats []core.FormatStat, width int) []string {
	if len(stats) == 0 {
		return nil
	}
	entries := make([]string, len(stats))
	for i, st := range stats {
		entries[i] = st.Label()
	}
	var lines []string
	line := ""
	for _, e := range entries {
		const sep = "  ·  "
		switch {
		case line == "":
			line = e
		case len([]rune(line))+len([]rune(sep))+len([]rune(e)) > width:
			lines = append(lines, line)
			line = e
		default:
			line += sep + e
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// wrapText splits s into lines no wider than width runes.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			if len([]rune(line))+1+len([]rune(w)) > width {
				out = append(out, line)
				line = w
			} else {
				line += " " + w
			}
		}
		out = append(out, line)
	}
	return out
}

func (m *model) viewExportTemplates() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	w := m.width
	cw := w - 4

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Export for Obsidian — choose a template"))
	lines = append(lines, "")

	start, end := listWindow(len(m.templateList), m.cursor, m.listRows(6))
	for i := start; i < end; i++ {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+m.templateList[i]))
	}

	addCursor := "  "
	if m.cursor == len(m.templateList) {
		addCursor = tuiSel.Render("▸ ")
	}
	lines = append(lines, "")
	lines = append(lines, tuiGreen.MaxWidth(cw).Render(addCursor+"+ Add new export template"))
	lines = append(lines, "")
	lines = append(lines, tuiDim.MaxWidth(cw).Render(fmt.Sprintf(
		"↑↓/jk select  ·  Enter/l open  ·  h/ESC back  ·  %s",
		scrollHint(start, end, len(m.templateList)),
	)))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewExportTemplateDetail() string {
	if m.editingField != "" {
		return m.viewTextEdit()
	}
	w := m.width
	cw := w - 4

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Export template: "+m.templateName))
	lines = append(lines, "")

	items := []string{
		"Export with this template",
		"Edit template",
		"Rename",
		"Delete",
		"Back",
	}
	for i, item := range items {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		style := lipgloss.NewStyle().Width(cw)
		if m.templateConfirmDelete && i == 3 {
			style = style.Foreground(lipgloss.Color("#ff0000"))
			item = "Delete — are you sure? (y/N)"
		}
		lines = append(lines, style.Render(cursor+item))
	}

	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("Variables:"))
	for _, l := range wrapText(m.exportVarsHint(), cw) {
		lines = append(lines, tuiDim.Width(cw).Render(l))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓/jk select  ·  Enter/l  ·  h/ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
