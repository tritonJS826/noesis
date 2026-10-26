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
