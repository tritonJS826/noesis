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
	case scrRunning:
		return m.viewRunning()
	case scrImport:
		return m.viewImport()
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
		"Import",
		"Data processing — pairs",
		"Data processing — one by one",
		"Export for Obsidian",
		"All phases",
		"Work with data",
		"Evaluate note uniqueness",
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
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter change/run  ·  ESC back"))
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
	lines = append(lines, tuiDim.MaxWidth(cw).Render("↑↓ move · Enter toggle · a all/none · ESC done"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewData() string {
	w := m.width
	cw := w - 4

	items := []string{
		"Remove file",
		"Remove file + obsidian links",
		"Compare file (filesystem search)",
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
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter  ·  ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
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
	w := m.width
	cw := w - 4

	keys := []string{"SRC", "OUT", "DB", "MODEL", "HOST", "MAXCHARS", "WORKERS", "EVAL PROMPT", "Reset database"}
	vals := []string{m.src, m.out, m.dbPath, m.modelName, m.host,
		fmt.Sprintf("%d", m.maxChars), fmt.Sprintf("%d", m.workers), "edit…", ""}

	var lines []string
	for i, k := range keys {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		style := lipgloss.NewStyle().Width(cw)
		line := fmt.Sprintf("%s%-9s: %s", cursor, k, vals[i])
		if i == 8 {
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
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter edit  ·  ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
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

	var lines []string
	start, end := listWindow(len(m.ollamaModels), m.cursor, m.listRows(2))
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
	hint := "↑↓ select  ·  Enter pick  ·  ESC back"
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
		filterLine = tuiDim.Render("start typing to filter…")
	} else {
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
	footer := fmt.Sprintf("found: %d  ·  ↑↓ select  ·  Enter evaluate  ·  ESC back  ·  %s",
		len(filtered), scrollHint(start, end, len(filtered)))
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

	start, end := listWindow(len(m.processList), m.cursor, m.listRows(6))
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
		fmt.Sprintf("↑↓ select  ·  Enter open  ·  ESC back  ·  %s", scrollHint(start, end, len(m.processList))),
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
		"Export for " + m.processName,
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
		if m.processConfirmDelete && i == 4 {
			style = style.Foreground(lipgloss.Color("#ff0000"))
			item = "Delete — are you sure? (y/N)"
		}
		lines = append(lines, style.Render(cursor+item))
	}

	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter  ·  ESC back"))

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
		filterLine = tuiDim.Render("type to search files…")
	} else {
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
	lines = append(lines, tuiDim.MaxWidth(cw).Render(fmt.Sprintf(
		"found: %d  ·  ↑↓ select  ·  Enter compare  ·  ESC back  ·  %s",
		len(m.fsFiles), scrollHint(start, end, len(m.fsFiles)),
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
	case name == "eval":
		named = []string{"${FILE_NAME}", "${CONTENT}", "${LINKED_NOTES}"}
	default:
		named = []string{"${FILE_NAME}", "${CONTENT}"}
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
		"↑↓ select  ·  Enter open  ·  ESC back  ·  %s",
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
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter  ·  ESC back"))

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
