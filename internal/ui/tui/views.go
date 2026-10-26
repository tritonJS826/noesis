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
		"Phase 1 — Import md → sqlite",
		"Phase 2 — Tags & links (ollama)",
		"Phase 3 — Write vault",
		"All phases",
		"Settings",
		"Evaluate note uniqueness",
		"Reset database",
	}

	// ── phases ──
	var phaseLines []string
	for i, item := range items {
		cursor := "  "
		if m.cursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		line := cursor + item
		if i == 6 {
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
	// split into pairs
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

	// ── assembly ──
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
		// show prompt text
		lines := []string{
			lipgloss.NewStyle().Bold(true).Width(cw).Render("Prompt: " + m.promptViewing),
			"",
		}
		// split into lines
		for _, line := range strings.Split(m.promptText, "\n") {
			lines = append(lines, lipgloss.NewStyle().Width(cw).Render(line))
		}
		lines = append(lines, "")
		lines = append(lines, tuiDim.Width(cw).Render("ESC back  ·  r reload from file"))
		content := lipgloss.JoinVertical(lipgloss.Left, lines...)
		return tuiBox.Width(w).Render(content)
	}

	// prompt list
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
	// filter line
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
		// parse JSON result and format
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
