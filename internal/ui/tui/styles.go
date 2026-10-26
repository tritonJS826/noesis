package tui

import "github.com/charmbracelet/lipgloss"

var (
	tuiBorder = lipgloss.Border{
		Top: "─", Bottom: "─", Left: "│", Right: "│",
		TopLeft: "┌", TopRight: "┐", BottomLeft: "└", BottomRight: "┘",
	}
	tuiBox = lipgloss.NewStyle().Border(tuiBorder).Padding(0, 1)

	tuiSel   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	tuiDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiRed   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiGreen = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	tuiYel   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

func tuiLabel(k, v string) string { return k + ": " + v }

func tuiEmpty(v string) string {
	if v == "" {
		return tuiDim.Render("(not set)")
	}
	return v
}
