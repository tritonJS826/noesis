package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"noesis/internal/core"
)

// whisperProgressMsg reports a download progress update from a background
// command.
type whisperProgressMsg struct {
	stage string
	done  int64
	total int64
}

// whisperDoneMsg finishes a runtime/model download.
type whisperDoneMsg struct {
	model string
	err   error
}

func (m *model) whisperSummary() string {
	name := core.NormalizeWhisperModel(m.whisperModel)
	if !core.WhisperModelInstalled(name) {
		return name + " (download)"
	}
	if !core.WhisperRuntimeInstalled() {
		return name + " ✓ (runtime missing)"
	}
	return name + " ✓"
}

func (m *model) openWhisperScreen() {
	m.refreshWhisperModels()
	m.whisperStatus = ""
	m.whisperErr = ""
	m.whisperBusy = false
	m.whisperStage = ""
	m.whisperDone, m.whisperTotal = 0, 0
	for i, info := range m.whisperModels {
		if info.Name == m.whisperModel {
			m.whisperCursor = i
			break
		}
	}
	m.screen = scrWhisper
}

func (m *model) refreshWhisperModels() {
	m.whisperModels = core.WhisperModels()
	if m.whisperCursor < 0 {
		m.whisperCursor = 0
	}
	if m.whisperCursor >= len(m.whisperModels) && len(m.whisperModels) > 0 {
		m.whisperCursor = len(m.whisperModels) - 1
	}
}

func (m *model) whisperKeys(msg tea.KeyMsg) tea.Cmd {
	if m.whisperBusy {
		switch vimNav(msg.String()) {
		case "esc":
			m.screen = scrSettings
			m.cursor = 5
		case "?":
			m.screen = scrAudioHelp
		}
		return nil
	}
	switch vimNav(msg.String()) {
	case "up":
		if m.whisperCursor > 0 {
			m.whisperCursor--
		}
	case "down":
		if m.whisperCursor < len(m.whisperModels)-1 {
			m.whisperCursor++
		}
	case "enter":
		if m.whisperCursor >= 0 && m.whisperCursor < len(m.whisperModels) {
			info := m.whisperModels[m.whisperCursor]
			if info.Downloaded {
				m.whisperModel = info.Name
				m.persistSettings()
				m.whisperStatus = fmt.Sprintf("Using %s.", info.Name)
				return nil
			}
			return m.startWhisperDownload(info.Name)
		}
	case "d":
		if m.whisperCursor >= 0 && m.whisperCursor < len(m.whisperModels) {
			return m.startWhisperDownload(m.whisperModels[m.whisperCursor].Name)
		}
	case "x":
		if m.whisperCursor >= 0 && m.whisperCursor < len(m.whisperModels) {
			info := m.whisperModels[m.whisperCursor]
			if info.Downloaded {
				if err := core.DeleteWhisperModel(info.Name); err != nil {
					m.whisperErr = err.Error()
				} else {
					if m.whisperModel == info.Name {
						m.whisperModel = core.DefaultWhisperModel
						m.persistSettings()
					}
					m.whisperStatus = fmt.Sprintf("Deleted %s.", info.Name)
					m.whisperErr = ""
					m.refreshWhisperModels()
				}
			} else {
				m.whisperStatus = "That model is not downloaded."
			}
		}
	case "r":
		if core.WhisperRuntimeInstalled() {
			m.whisperStatus = "whisper.cpp runtime is already installed."
			return nil
		}
		if !core.WhisperRuntimeAutoInstallSupported() {
			m.whisperErr = ""
			m.whisperStatus = "Automatic install is not available on this platform. Follow the instructions above."
			return nil
		}
		return m.startWhisperDownload("")
	case "?":
		m.screen = scrAudioHelp
	case "esc", "q":
		m.screen = scrSettings
		m.cursor = 5
	}
	return nil
}

func (m *model) startWhisperDownload(modelName string) tea.Cmd {
	if m.whisperBusy {
		return nil
	}
	p := m.program
	m.whisperBusy = true
	m.whisperErr = ""
	m.whisperStatus = ""
	m.whisperDone, m.whisperTotal = 0, 0
	if modelName == "" {
		m.whisperStage = "runtime"
	} else {
		m.whisperStage = "model " + modelName
	}
	return func() tea.Msg {
		ctx := context.Background()
		sendProgress := func(stage string, done, total int64) {
			if p != nil {
				p.Send(whisperProgressMsg{stage: stage, done: done, total: total})
			}
		}
		if !core.WhisperRuntimeInstalled() && core.WhisperRuntimeAutoInstallSupported() {
			sendProgress("runtime", 0, 0)
			lastPct := -1
			if err := core.EnsureWhisperRuntime(ctx, func(done, total int64) {
				pct := -1
				if total > 0 {
					pct = int(done * 100 / total)
				}
				if pct == lastPct && pct >= 0 {
					return
				}
				lastPct = pct
				sendProgress("runtime", done, total)
			}); err != nil {
				return whisperDoneMsg{model: modelName, err: err}
			}
		}
		if modelName != "" {
			sendProgress("model "+modelName, 0, 0)
			lastPct := -1
			if err := core.DownloadWhisperModel(ctx, modelName, func(done, total int64) {
				pct := -1
				if total > 0 {
					pct = int(done * 100 / total)
				}
				if pct == lastPct && pct >= 0 {
					return
				}
				lastPct = pct
				sendProgress("model "+modelName, done, total)
			}); err != nil {
				return whisperDoneMsg{model: modelName, err: err}
			}
		}
		return whisperDoneMsg{model: modelName}
	}
}

func (m *model) viewOptionPicker() string {
	w := m.width
	cw := w - 4
	title := "Choose audio backend"
	if m.pickerKind == "language" {
		title = "Choose speech language (Google)"
	} else if m.pickerKind == "binary" {
		title = "Binary storage mode"
	}
	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render(title))
	lines = append(lines, "")
	start, end := listWindow(len(m.pickerLabels), m.pickerCursor, m.listRows(2))
	for i := start; i < end; i++ {
		cursor := "  "
		if m.pickerCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(cursor+m.pickerLabels[i]))
	}
	lines = append(lines, "")
	if m.pickerKind == "language" {
		for _, l := range wrapText("Google needs a specific language. Local Whisper always auto-detects.", cw) {
			lines = append(lines, tuiDim.MaxWidth(cw).Render(l))
		}
		lines = append(lines, "")
	}
	if m.pickerKind == "binary" {
		for _, l := range wrapText("Links: binaries stay on disk and only paths are stored. Database (base64): files are embedded in sqlite.", cw) {
			lines = append(lines, tuiDim.MaxWidth(cw).Render(l))
		}
		lines = append(lines, "")
	}
	hint := "↑↓/jk select  ·  Enter/l pick  ·  h/ESC back"
	if m.pickerKind == "audio" {
		hint += "  ·  ? setup help"
	}
	if len(m.pickerLabels) > 0 {
		hint += "  ·  " + scrollHint(start, end, len(m.pickerLabels))
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render(hint))
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func (m *model) viewWhisper() string {
	w := m.width
	cw := w - 4

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Width(cw).Render("Whisper models"))
	lines = append(lines, "")
	if path := core.WhisperRuntimePath(); path != "" {
		lines = append(lines, tuiGreen.MaxWidth(cw).Render("Runtime: installed"))
		lines = append(lines, tuiDim.MaxWidth(cw).Render(path))
	} else {
		lines = append(lines, tuiRed.MaxWidth(cw).Render("Runtime: not installed"))
		for _, l := range wrapText(core.LocalWhisperSetupNote(), cw) {
			lines = append(lines, tuiDim.MaxWidth(cw).Render(l))
		}
		lines = append(lines, tuiDim.MaxWidth(cw).Render("Guide: "+core.WhisperCPPBuildURL))
	}
	lines = append(lines, "")

	start, end := listWindow(len(m.whisperModels), m.whisperCursor, m.listRows(6))
	for i := start; i < end; i++ {
		info := m.whisperModels[i]
		cursor := "  "
		if m.whisperCursor == i {
			cursor = tuiSel.Render("▸ ")
		}
		state := "not downloaded"
		if info.Downloaded {
			state = "downloaded"
		}
		selected := ""
		if info.Name == m.whisperModel {
			selected = "  · active"
		}
		line := fmt.Sprintf("%s%-16s %-10s %s%s", cursor, info.Name, formatSize(info.Size), state, selected)
		style := lipgloss.NewStyle().MaxWidth(cw)
		if info.Downloaded {
			style = tuiGreen.MaxWidth(cw)
		}
		lines = append(lines, style.Render(line))
	}

	if m.whisperBusy {
		lines = append(lines, "")
		label := m.whisperStage
		if label == "" {
			label = "download"
		}
		line := "Downloading " + label + "…"
		if m.whisperTotal > 0 {
			pct := float64(m.whisperDone) * 100 / float64(m.whisperTotal)
			line += fmt.Sprintf(" %.0f%% (%s / %s)", pct, formatSize(m.whisperDone), formatSize(m.whisperTotal))
		} else if m.whisperDone > 0 {
			line += " " + formatSize(m.whisperDone)
		}
		lines = append(lines, lipgloss.NewStyle().Width(cw).Render(line))
		lines = append(lines, tuiDim.MaxWidth(cw).Render(progressBar(m.whisperDone, m.whisperTotal, cw)))
	}
	if m.whisperErr != "" {
		lines = append(lines, "")
		for _, l := range wrapText(m.whisperErr, cw) {
			lines = append(lines, tuiRed.MaxWidth(cw).Render(l))
		}
	}
	if m.whisperStatus != "" {
		lines = append(lines, "")
		for _, l := range wrapText(m.whisperStatus, cw) {
			lines = append(lines, tuiGreen.MaxWidth(cw).Render(l))
		}
	}

	lines = append(lines, "")
	hint := "↑↓/jk select  ·  Enter download/use  ·  d download  ·  x delete"
	extra := "h/ESC back  ·  ? setup help"
	if !core.WhisperRuntimeInstalled() && core.WhisperRuntimeAutoInstallSupported() {
		extra = "r install runtime  ·  " + extra
	}
	if len(m.whisperModels) > 0 {
		extra += "  ·  " + scrollHint(start, end, len(m.whisperModels))
	}
	lines = append(lines, tuiDim.MaxWidth(cw).Render(hint))
	lines = append(lines, tuiDim.MaxWidth(cw).Render(extra))
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}

func formatSize(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func progressBar(done, total int64, width int) string {
	if width < 10 {
		width = 10
	}
	if total <= 0 {
		return strings.Repeat("░", width)
	}
	ratio := float64(done) / float64(total)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
