package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"noesis/internal/core"
)

func (m *model) audioHelpKeys(msg tea.KeyMsg) tea.Cmd {
	if key := vimNav(msg.String()); key == "esc" || key == "q" {
		m.screen = scrSettings
	}
	return nil
}

func (m *model) viewAudioHelp() string {
	w := m.width
	cw := w - 4

	var lines []string
	add := func(text string) {
		for _, line := range wrapText(text, cw) {
			lines = append(lines, lipgloss.NewStyle().MaxWidth(cw).Render(line))
		}
	}
	addDim := func(text string) {
		for _, line := range wrapText(text, cw) {
			lines = append(lines, tuiDim.MaxWidth(cw).Render(line))
		}
	}
	addBold := func(text string) {
		for _, line := range wrapText(text, cw) {
			lines = append(lines, lipgloss.NewStyle().Bold(true).MaxWidth(cw).Render(line))
		}
	}

	addBold("Audio setup")
	lines = append(lines, "")

	addBold("Local Whisper")
	add("1. Open Settings → WHISPER, choose a model and press Enter to download and select it.")
	add(core.LocalWhisperSetupNote())
	add("Guide: " + core.WhisperCPPBuildURL)
	add("Releases: " + core.WhisperCPPReleasesURL)
	add("Homebrew (macOS): " + core.WhisperCPPHomebrewURL)
	lines = append(lines, "")
	if core.FFmpegAvailable() {
		addDim("ffmpeg: installed (used to convert some audio formats).")
	} else {
		addDim("ffmpeg: not found — some audio formats need it. " + core.FFmpegDownloadURL)
		addDim(core.FFmpegInstallExample())
	}
	lines = append(lines, "")
	lines = append(lines, tuiDim.Width(cw).Render("ESC back"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return tuiBox.Width(w).Render(content)
}
