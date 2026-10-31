package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"noesis/internal/core"
)

// TestRAGAnswerViewFitsTerminalHeight guards the second half of the RAG
// viewport work: the answer block must be windowed too, otherwise a long answer
// pushes the results list and the footer off the bottom of the terminal.
func TestRAGAnswerViewFitsTerminalHeight(t *testing.T) {
	ti := textinput.New()
	results := make([]core.RAGResult, 20)
	for i := range results {
		results[i] = core.RAGResult{Score: 0.9, Path: "notes/file" + strconv.Itoa(i) + ".md", Chunk: "chunk text " + strconv.Itoa(i)}
	}
	longAnswer := strings.Repeat("word ", 400)

	for _, size := range []struct{ width, height int }{{80, 24}, {50, 12}, {120, 40}} {
		for _, withResults := range []bool{false, true} {
			m := &model{width: size.width, height: size.height, textInput: ti}
			m.ragQuery = "question"
			m.ragAnswer = longAnswer
			if withResults {
				m.ragResults = results
				m.cursor = len(results) - 1
			}

			view := m.viewRAGSearch()
			if got := strings.Count(view, "\n") + 1; got > size.height {
				t.Fatalf("%dx%d (results=%v): view is %d lines (> %d):\n%s",
					size.width, size.height, withResults, got, size.height, view)
			}
			if !strings.Contains(view, "Answer:") {
				t.Fatalf("%dx%d (results=%v): answer header missing", size.width, size.height, withResults)
			}
		}
	}
}

// TestRAGAnswerScrollsWithPgKeys checks that PgUp/PgDn move the answer window
// and clamp at both ends.
func TestRAGAnswerScrollsWithPgKeys(t *testing.T) {
	ti := textinput.New()
	m := &model{width: 80, height: 24, textInput: ti}
	m.ragQuery = "question"
	m.ragAnswer = strings.Repeat("word ", 400)

	if m.ragAnswerVisible() <= 0 {
		t.Fatal("expected the answer to have a visible window")
	}
	if got := strings.Count(m.viewRAGSearch(), "\n") + 1; got > m.height {
		t.Fatalf("view is %d lines (> %d)", got, m.height)
	}

	m.ragSearchKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.ragAnswerOffset == 0 {
		t.Fatal("pgdown did not scroll the answer")
	}
	if view := m.viewRAGSearch(); !strings.Contains(view, "of") {
		t.Fatalf("scrolled answer does not show a position hint:\n%s", view)
	}

	m.ragSearchKeys(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.ragAnswerOffset != 0 {
		t.Fatalf("pgup did not return to the top: offset=%d", m.ragAnswerOffset)
	}

	// Scrolling up at the top stays at zero.
	m.ragSearchKeys(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.ragAnswerOffset != 0 {
		t.Fatalf("offset went negative: %d", m.ragAnswerOffset)
	}
}
