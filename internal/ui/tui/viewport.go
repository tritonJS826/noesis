package tui

import "fmt"

// listRows returns how many list items fit on screen after reserving rows for
// the screen chrome (title, footer, blank lines) and the box border. When the
// terminal height is still unknown it falls back to showing the whole list.
func (m *model) listRows(reserved int) int {
	if m.height <= 0 {
		return 0
	}
	rows := m.height - 2 - reserved // 2 = top and bottom border
	if rows < 1 {
		rows = 1
	}
	return rows
}

// listWindow returns the [start, end) slice of total items that keeps cursor
// visible in a window of the given size. It scrolls just enough so the cursor
// never leaves the viewport and centres it when possible.
func listWindow(total, cursor, window int) (start, end int) {
	if total <= 0 {
		return 0, 0
	}
	if window <= 0 || window >= total {
		return 0, total
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}

	start = cursor - window/2
	if start < 0 {
		start = 0
	}
	if start+window > total {
		start = total - window
	}
	return start, start + window
}

// scrollHint describes the visible part of a list, e.g. "1–12 of 46".
func scrollHint(start, end, total int) string {
	if total == 0 {
		return "0 of 0"
	}
	return fmt.Sprintf("%d–%d of %d", start+1, end, total)
}

// ─── scrollable detail view ─────────────────────────────────────────────────

// detailLine is one rendered line of the record detail view. Header lines are
// column names and are shown in bold.
type detailLine struct {
	header bool
	text   string
}

// browseDetailWidth is the usable text width inside the screen box.
func (m *model) browseDetailWidth() int {
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	return w
}

// browseDetailPage returns how many detail lines fit on one screen.
func (m *model) browseDetailPage() int {
	rows := m.listRows(5) // title, blank, footer and safety margin
	if rows < 1 {
		rows = 10
	}
	return rows
}

// browseDetailContent flattens the selected record into display lines, wrapping
// long values so arrow keys can scroll through the whole document.
func (m *model) browseDetailContent() []detailLine {
	if m.browseDetailConfirm {
		return []detailLine{{text: fmt.Sprintf("Remove %q from the database? (y/N)", m.browseDetailPath)}}
	}
	cw := m.browseDetailWidth()
	var lines []detailLine
	for _, f := range m.browseDetail {
		lines = append(lines, detailLine{header: true, text: f.Name})
		for _, l := range wrapText(f.Value, cw) {
			lines = append(lines, detailLine{text: l})
		}
		lines = append(lines, detailLine{})
	}
	return lines
}

// browseDetailMaxOffset clamps the scroll offset so the last line stays visible.
func (m *model) browseDetailMaxOffset() int {
	total, page := len(m.browseDetailContent()), m.browseDetailPage()
	if total <= page {
		return 0
	}
	return total - page
}

// scrollBrowseDetail moves the detail viewport by delta lines.
func (m *model) scrollBrowseDetail(delta int) {
	offset := m.browseDetailOffset + delta
	if offset < 0 {
		offset = 0
	}
	if max := m.browseDetailMaxOffset(); offset > max {
		offset = max
	}
	m.browseDetailOffset = offset
}

// scrollRAGAnswer moves the semantic-search answer window by delta lines. The
// offset is clamped to what actually fits so PgUp/PgDn never scroll past the
// text.
func (m *model) scrollRAGAnswer(delta int) {
	visible := m.ragAnswerVisible()
	total := len(wrapText(m.ragAnswer, m.width-4))
	max := total - visible
	if max < 0 {
		max = 0
	}
	offset := m.ragAnswerOffset + delta
	if offset < 0 {
		offset = 0
	}
	if offset > max {
		offset = max
	}
	m.ragAnswerOffset = offset
}

// ragAnswerStep is the PgUp/PgDn step: one screen minus a line of overlap, so
// the reader keeps their place.
func (m *model) ragAnswerStep() int {
	step := m.ragAnswerVisible() - 1
	if step < 1 {
		step = 1
	}
	return step
}
