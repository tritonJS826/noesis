package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"noesis/internal/core"
)

func TestListWindowKeepsCursorVisible(t *testing.T) {
	total, window := 46, 10
	for cursor := 0; cursor < total; cursor++ {
		start, end := listWindow(total, cursor, window)
		if start < 0 || end > total || start >= end {
			t.Fatalf("cursor %d: bad window %d-%d", cursor, start, end)
		}
		if cursor < start || cursor >= end {
			t.Fatalf("cursor %d outside window %d-%d", cursor, start, end)
		}
	}
}

func TestSettingsOnlyExposesEvalPrompt(t *testing.T) {
	m := &model{width: 80, height: 24}
	view := m.viewSettings()
	if !strings.Contains(view, "EVAL PROMPT") {
		t.Fatalf("settings does not expose the eval prompt:\n%s", view)
	}
	if strings.Contains(view, "PROMPTS") {
		t.Fatalf("settings still exposes the generic prompt list:\n%s", view)
	}
}

func TestFormatsViewFitsTerminalHeight(t *testing.T) {
	ti := textinput.New()
	for _, size := range []struct{ width, height int }{{80, 24}, {50, 12}} {
		m := &model{width: size.width, height: size.height, textInput: ti}
		m.importFormats = nil
		m.fmtCursor = len(core.KnownFormats()) - 1

		view := m.viewFormats()
		if got := strings.Count(view, "\n") + 1; got > m.height {
			t.Fatalf("%dx%d: view is %d lines:\n%s", size.width, size.height, got, view)
		}
		if !strings.Contains(view, "of "+strconv.Itoa(len(core.KnownFormats()))) {
			t.Fatalf("%dx%d: view does not show the scroll position:\n%s", size.width, size.height, view)
		}

		m.fmtCursor = 0
		view = m.viewFormats()
		if !strings.Contains(view, "▸ [x]  md ") {
			t.Fatalf("%dx%d: first format is not visible at the top:\n%s", size.width, size.height, view)
		}
	}
}

func TestRAGSearchViewFitsTerminalHeightAndScrolls(t *testing.T) {
	ti := textinput.New()
	results := make([]core.RAGResult, 20)
	for i := range results {
		results[i] = core.RAGResult{Score: 0.9, Path: "notes/file" + strconv.Itoa(i) + ".md", Chunk: "chunk text " + strconv.Itoa(i)}
	}

	for _, size := range []struct{ width, height int }{{80, 24}, {50, 12}} {
		m := &model{width: size.width, height: size.height, textInput: ti}
		m.ragQuery = "question"
		m.ragResults = results
		m.ragTopK = 20

		// Cursor on the last result: the header must stay pinned at the top
		// while the list scrolls to keep the selection visible.
		m.cursor = len(results) - 1
		view := m.viewRAGSearch()
		if got := strings.Count(view, "\n") + 1; got > m.height {
			t.Fatalf("%dx%d: view is %d lines:\n%s", size.width, size.height, got, view)
		}
		if !strings.Contains(view, "Query: question") {
			t.Fatalf("%dx%d: query header scrolled off the top:\n%s", size.width, size.height, view)
		}
		if !strings.Contains(view, "of 20") {
			t.Fatalf("%dx%d: view does not show the scroll position:\n%s", size.width, size.height, view)
		}
		if !strings.Contains(view, "[20]") {
			t.Fatalf("%dx%d: last result not visible:\n%s", size.width, size.height, view)
		}

		// Cursor on the first result: the list scrolls back to the top.
		m.cursor = 0
		view = m.viewRAGSearch()
		if got := strings.Count(view, "\n") + 1; got > m.height {
			t.Fatalf("%dx%d: view is %d lines:\n%s", size.width, size.height, got, view)
		}
		if !strings.Contains(view, "▸ [1]") {
			t.Fatalf("%dx%d: first result not visible at the top:\n%s", size.width, size.height, view)
		}
	}
}

func TestBrowseDetailScrollsWithVimKeys(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {60, 12}} {
		m := &model{width: size.width, height: size.height}
		m.browseDetailPath = "docs/a.md"
		m.browseDetail = []core.NoteField{{Name: "content", Value: strings.Repeat("line\n", 200)}}

		m.scrollBrowseDetail(1000)
		if m.browseDetailOffset != m.browseDetailMaxOffset() {
			t.Fatalf("%dx%d: offset %d not clamped to %d", size.width, size.height, m.browseDetailOffset, m.browseDetailMaxOffset())
		}
		view := m.viewBrowseDetail()
		if got := strings.Count(view, "\n") + 1; got > m.height {
			t.Fatalf("%dx%d: detail view is %d lines:\n%s", size.width, size.height, got, view)
		}
		if !strings.Contains(view, "of "+strconv.Itoa(len(m.browseDetailContent()))) {
			t.Fatalf("%dx%d: detail view does not show the scroll position:\n%s", size.width, size.height, view)
		}

		m.scrollBrowseDetail(-1000)
		if m.browseDetailOffset != 0 {
			t.Fatalf("%dx%d: offset %d, want 0", size.width, size.height, m.browseDetailOffset)
		}
	}
}
