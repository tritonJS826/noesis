package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"noesis/internal/core"
)

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch m.screen {
	case scrMenu:
		return m.menuKeys(msg)
	case scrSettings:
		return m.settingsKeys(msg)
	case scrModelPicker:
		return m.modelPickerKeys(msg)
	case scrFilePicker:
		return m.filePickerKeys(msg)
	case scrEval:
		return m.evalKeys(msg)
	case scrRunning:
	}
	return nil
}

func (m *model) menuKeys(msg tea.KeyMsg) tea.Cmd {
	// waiting for reset confirmation
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

	items := 6 // 0..6
	switch msg.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.resetErr = ""
	case "down":
		if m.cursor < items {
			m.cursor++
		}
		m.resetErr = ""
	case "enter":
		switch m.cursor {
		case 4: // settings
			m.screen = scrSettings
			m.cursor = 0
		case 5: // evaluate file
			m.openFilePicker()
		case 6: // reset database
			m.confirmReset = true
		default:
			// check obligatory fields
			if missing := m.checkMissing(); missing != "" {
				m.phaseErr = fmt.Errorf("Not set: %s", missing)
			} else {
				m.screen = scrRunning
				m.phaseErr = nil
				return runPhaseCmd(m)
			}
		}
	}
	return nil
}

func (m *model) settingsKeys(msg tea.KeyMsg) tea.Cmd {
	if m.promptList != nil {
		return m.promptKeys(msg)
	}
	max := 7 // 0..7
	switch msg.String() {
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
		m.cursor = 4
	case "enter":
		m.startEdit()
	}
	return nil
}

func (m *model) modelPickerKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
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
			m.modelName = m.ollamaModels[m.cursor]
		}
		m.screen = scrSettings
		m.cursor = 3
	case "esc":
		m.screen = scrSettings
		m.cursor = 3
	}
	return nil
}

func (m *model) openFilePicker() {
	notes, err := core.ListNotes(m.db)
	if err != nil {
		return
	}
	m.fileNotes = notes
	m.cursor = 0
	m.fileFilter = ""
	m.screen = scrFilePicker
}

func (m *model) filePickerKeys(msg tea.KeyMsg) tea.Cmd {
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
			linked, _ := core.LinkedNoteIDs(m.db, id)
			m.evalResult = ""
			m.evalErr = ""
			m.evalLoading = true
			m.screen = scrEval
			return evalNoteCmd(m.db, id, m.host, m.modelName, m.maxChars, linked)
		}
	case "esc":
		m.screen = scrMenu
		m.cursor = 5
	case "backspace":
		r := []rune(m.fileFilter)
		if len(r) > 0 {
			m.fileFilter = string(r[:len(r)-1])
		}
		m.cursor = 0
	default:
		// any printable character — type to filter
		if len(msg.Runes) == 1 && msg.Runes[0] >= 32 {
			m.fileFilter += string(msg.Runes[0])
			m.cursor = 0
		}
	}
	return nil
}

func (m *model) filteredNotes() []core.NoteInfo {
	if m.fileFilter == "" {
		return m.fileNotes
	}
	lower := strings.ToLower(m.fileFilter)
	var out []core.NoteInfo
	for _, n := range m.fileNotes {
		if strings.Contains(strings.ToLower(n.Name), lower) {
			out = append(out, n)
		}
	}
	return out
}

func (m *model) evalKeys(msg tea.KeyMsg) tea.Cmd {
	if !m.evalLoading {
		m.screen = scrMenu
		m.cursor = 5
	}
	return nil
}

// ─── field editing ───────────────────────────────────────────────────────────

func (m *model) startEdit() {
	switch m.cursor {
	case 0: // SRC
		m.textInput.SetValue(m.src)
		m.textInput.Focus()
		m.editingField = "SRC — path to md notes"
	case 1: // OUT
		m.textInput.SetValue(m.out)
		m.textInput.Focus()
		m.editingField = "OUT — vault folder"
	case 2: // DB
		m.textInput.SetValue(m.dbPath)
		m.textInput.Focus()
		m.editingField = "DB — database file"
	case 3: // MODEL
		if m.ollamaOK && len(m.ollamaModels) > 0 {
			m.screen = scrModelPicker
			m.cursor = 0
			for i, mod := range m.ollamaModels {
				if mod == m.modelName {
					m.cursor = i
					break
				}
			}
			return
		}
		m.textInput.SetValue(m.modelName)
		m.textInput.Focus()
		m.editingField = "MODEL — ollama model"
	case 4: // HOST
		m.textInput.SetValue(m.host)
		m.textInput.Focus()
		m.editingField = "HOST — ollama host"
	case 5: // MAXCHARS
		m.textInput.SetValue(fmt.Sprintf("%d", m.maxChars))
		m.textInput.Focus()
		m.editingField = "MAXCHARS — max chars"
	case 6: // WORKERS
		m.textInput.SetValue(fmt.Sprintf("%d", m.workers))
		m.textInput.Focus()
		m.editingField = "WORKERS — concurrency (1–100)"
	case 7: // PROMPTS
		m.editingField = ""
		m.openPromptList()
		return
	}
}

func (m *model) saveField() {
	val := m.textInput.Value()
	switch m.cursor {
	case 0:
		m.src = val
	case 1:
		m.out = val
	case 2:
		m.dbPath = val
	case 3:
		m.modelName = val
	case 4:
		m.host = val
	case 5:
		if n, _ := fmt.Sscanf(val, "%d", &m.maxChars); n != 1 {
			m.maxChars = 4000
		}
	case 6:
		if n, _ := fmt.Sscanf(val, "%d", &m.workers); n != 1 || m.workers < 1 {
			m.workers = 1
		}
		if m.workers > 100 {
			m.workers = 100
		}
	case 7: // PROMPTS — save edited prompt
		name := strings.TrimPrefix(m.editingField, "Prompt: ")
		_ = core.SavePrompt(m.db, name, val)
		m.promptViewing = name
		m.promptText = val
		m.promptList = []string{name}
	}
}

func (m *model) checkMissing() string {
	var missing []string
	switch m.cursor {
	case 0: // phase1
		if m.src == "" {
			missing = append(missing, "SRC")
		}
	case 1: // phase2 — database is enough
	case 2: // phase3
		if m.out == "" {
			missing = append(missing, "OUT")
		}
		if m.src == "" {
			missing = append(missing, "SRC")
		}
	case 3: // all
		if m.src == "" {
			missing = append(missing, "SRC")
		}
		if m.out == "" {
			missing = append(missing, "OUT")
		}
	}
	return strings.Join(missing, ", ")
}

func (m *model) openPromptList() {
	names, _ := core.ListPrompts(m.db)
	if len(names) == 0 {
		names = []string{"phase2", "eval"}
	}
	m.promptList = names
	m.cursor = 0
	m.promptViewing = ""
}

func (m *model) promptKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.promptViewing == "" {
			if m.cursor < len(m.promptList)-1 {
				m.cursor++
			}
		}
	case "enter":
		if m.promptViewing == "" && m.cursor < len(m.promptList) {
			m.promptViewing = m.promptList[m.cursor]
			m.promptText = core.LoadPrompt(m.db, m.promptViewing)
			m.cursor = 0
		}
	case "r", "R":
		if m.promptViewing != "" {
			// reload from file
			if text, err := core.ReloadPromptFromFile(m.db, m.promptViewing); err == nil {
				m.promptText = text
			}
		}
	case "e", "E":
		if m.promptViewing != "" {
			m.textInput.SetValue(m.promptText)
			m.textInput.Focus()
			m.editingField = "Prompt: " + m.promptViewing
			m.promptList = nil
		}
	case "esc":
		if m.promptViewing != "" {
			m.promptViewing = ""
			m.cursor = 0
		} else {
			m.promptList = nil
			m.promptViewing = ""
			m.screen = scrSettings
			m.cursor = 7
		}
	}
	return nil
}
