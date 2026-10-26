package tui

import (
	"encoding/json"
	"fmt"
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
		"Data processing — one by one (tags)",
		"Write vault",
		"All phases",
		"Work with data",
		"Evaluate note uniqueness",
		"Settings",
		"Reset database",
	}

	var phaseLines []string
	for i, item := range items {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		line := cursor + item
		if i == 8 {
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
	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Select formats to import"))
	switch {
	case m.importFormats == nil:
		lines = append(lines, tuiDim.Width(cw).Render("current: all formats  ·  a — select none"))
	case len(m.importFormats) == 0:
		lines = append(lines, tuiYel.Width(cw).Render("current: none  ·  a — select all"))
	default:
		lines = append(lines, tuiDim.Width(cw).Render("a — select all / none"))
	}
	lines = append(lines, "")

	selected := m.importFormats
	if selected == nil {
		selected = core.FormatSet{}
		for _, f := range formats {
			selected[f.Ext] = true
		}
	}
	for i, f := range formats {
		cursor := "  "
		mark := "[ ]"
		if selected[f.Ext] {
			mark = "[x]"
		}
		if m.fmtCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(
			fmt.Sprintf("%s%s  %-10s (%s)", cursor, mark, f.Ext, f.Kind),
		))
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("↑↓ select  ·  Enter/space toggle  ·  a toggle all  ·  ESC done"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewData() string {
	w := m.width
	cw := w - 4

	items := []string{
		"Remove file",
		"Remove file + obsidian links",
		"Compare file (without adding to db)",
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
		for i, n := range filtered {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+n.Path))
		}
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render(fmt.Sprintf("found: %d  ·  ↑↓ select  ·  Enter remove  ·  ESC back", len(filtered))))

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

func (m *model) viewPromptSub() string {
	w := m.width
	cw := w - 4

	if m.promptViewing != "" {
		lines := []string{
			lipgloss.NewStyle().Bold(true).Width(cw).Render("Prompt: " + m.promptViewing),
			"",
		}
		for _, line := range strings.Split(m.promptText, "\n") {
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(line))
		}
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("ESC back  ·  r reload from file"))
		content := lipgloss.JoinVertical(lipgloss.Left, lines...)
		return tuiBox.Width(w).Render(content)
	}

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

func (m *model) viewModelPicker() string {
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

func (m *model) viewTextEdit() string {
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
	if len(filtered) == 0 {
		lines = append(lines, tuiDim.Width(cw).Render("nothing found"))
	} else {
		for i, n := range filtered {
			cursor := "  "
			if m.cursor == i {
				cursor = tuiSel.Render("▸ ")
			}
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(cursor+n.Name))
		}
	}
	lines = append(lines, "")
	footer := fmt.Sprintf("found: %d  ·  ↑↓ select  ·  Enter evaluate  ·  ESC back", len(filtered))
	lines = append(lines, tuiDim.Width(cw).Render(footer))
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

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}