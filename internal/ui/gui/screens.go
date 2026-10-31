package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"noesis/internal/core"
)

// ─── main menu ──────────────────────────────────────────────────────────────

func (s *state) menuScreen() fyne.CanvasObject {
	list := widget.NewList(
		func() int { return len(menuItems) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			o.(*widget.Label).SetText(menuItems[id])
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		s.activateMenu(int(id))
	}

	summary := widget.NewLabel(fmt.Sprintf(
		"SRC: %s   ·   OUT: %s   ·   DB: %s   ·   MODEL: %s   ·   AUDIO: %s   ·   WHISPER: %s   ·   HOST: %s   ·   MAXCHARS: %s   ·   WORKERS: %s   ·   SKIP DIRS: %s",
		s.src.Text, s.out.Text, s.dbEntry.Text, s.model.Text, s.audioEngine.Selected, s.whisperModel.Selected, s.host.Text, s.maxChars.Text, s.workers.Text, s.skipDirs.Text,
	))
	summary.Wrapping = fyne.TextWrapWord

	ollama := widget.NewLabel(s.ollamaStatus)
	ollama.Wrapping = fyne.TextWrapWord

	formatStats := wrappedLabel(s.formatStatsSummary())

	return container.NewBorder(
		widget.NewLabel("Choose an action:"),
		container.NewVBox(
			summary,
			formatStats,
			ollama,
			widget.NewLabelWithStyle(
				"Select an item to open it. The status and log panel stays at the bottom.",
				fyne.TextAlignLeading, fyne.TextStyle{Italic: true},
			),
		),
		nil, nil, list,
	)
}

// formatStatsSummary renders the per-extension import/extraction counters
// shown on the start screen, e.g. "3/20 .mp3 extracted · 89 .md ready".
func (s *state) formatStatsSummary() string {
	db := s.currentDB()
	if db == nil {
		return ""
	}
	stats := core.LoadFormatStats(db)
	if len(stats) == 0 {
		return "Formats: no files imported yet."
	}
	parts := make([]string, len(stats))
	for i, st := range stats {
		parts[i] = st.Label()
	}
	return "Formats: " + strings.Join(parts, "  ·  ")
}

// ─── import ─────────────────────────────────────────────────────────────────

func (s *state) importScreen() fyne.CanvasObject {
	srcSelect := widget.NewSelect(importSourceNames, func(name string) {
		if v, err := core.ParseImportSource(name); err == nil {
			s.importSource = v
		}
	})
	srcSelect.SetSelected(s.importSource.String())

	browse := widget.NewButton("Browse…", func() {
		d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			if uri != nil {
				s.src.SetText(uri.Path())
			}
		}, s.win)
		d.SetTitleText("Select source folder")
		if path := strings.TrimSpace(s.src.Text); path != "" {
			if abs, err := filepath.Abs(path); err == nil {
				if uri, err := storage.ListerForURI(storage.NewFileURI(abs)); err == nil {
					d.SetLocation(uri)
				}
			}
		}
		d.Show()
	})

	s.formatsLabel.SetText(s.formatsSummary())

	form := widget.NewForm(
		widget.NewFormItem("Import source", srcSelect),
		widget.NewFormItem("Source", container.NewBorder(nil, nil, nil, browse, s.src)),
		widget.NewFormItem("Formats", container.NewBorder(nil, nil, nil, widget.NewButton("Choose formats…", func() {
			s.screen = scrFormats
			s.render()
		}), s.formatsLabel)),
	)

	return container.NewVBox(
		widget.NewLabel("Import (Phase 1) — choose a source and the formats to read."),
		form,
		container.NewHBox(
			widget.NewButton("Run import", s.startImport),
			widget.NewLabel("SRC can be a folder, a .zip archive or a .json file."),
		),
	)
}

// ─── sync ────────────────────────────────────────────────────────────────────

func (s *state) syncScreen() fyne.CanvasObject {
	// Source type selector (shared by import and sync)
	srcSelect := widget.NewSelect(importSourceNames, func(name string) {
		if v, err := core.ParseImportSource(name); err == nil {
			s.importSource = v
			s.syncItems = nil
			s.syncChecked = nil
			s.render()
		}
	})
	srcSelect.SetSelected(s.importSource.String())

	browse := widget.NewButton("Browse…", func() {
		d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			if uri != nil {
				s.src.SetText(uri.Path())
			}
		}, s.win)
		d.SetTitleText("Select source folder")
		if path := strings.TrimSpace(s.src.Text); path != "" {
			if abs, err := filepath.Abs(path); err == nil {
				if uri, err := storage.ListerForURI(storage.NewFileURI(abs)); err == nil {
					d.SetLocation(uri)
				}
			}
		}
		d.Show()
	})

	s.formatsLabel.SetText(s.formatsSummary())

	form := widget.NewForm(
		widget.NewFormItem("Source type", srcSelect),
		widget.NewFormItem("Source", container.NewBorder(nil, nil, nil, browse, s.src)),
		widget.NewFormItem("Formats", container.NewBorder(nil, nil, nil, widget.NewButton("Choose formats…", func() {
			s.screen = scrFormats
			s.render()
		}), s.formatsLabel)),
	)

	if s.src.Text == "" {
		return container.NewVBox(
			widget.NewLabel("Synchronize — choose a source and the formats to read."),
			form,
			widget.NewLabel("SRC can be a folder, a .zip archive or a .json file."),
		)
	}

	// For non-filesystem sources, show an import button directly.
	if s.importSource != core.SourceFilesystem {
		return container.NewVBox(
			widget.NewLabel("Synchronize — choose a source and the formats to read."),
			form,
			container.NewHBox(
				widget.NewButton("Import all", s.startImport),
				widget.NewLabel("All files from the source will be imported into the database."),
			),
		)
	}

	// Filesystem source: show sync diff interface.
	if s.syncItems == nil {
		return container.NewVBox(
			widget.NewLabel("Synchronize — choose a source and the formats to read."),
			form,
			widget.NewLabel("Comparing database with folder: "+s.src.Text+" …"),
			widget.NewButton("Run comparison", func() {
				s.runSyncDiff()
			}),
		)
	}

	if len(s.syncItems) == 0 {
		return container.NewVBox(
			widget.NewLabel("Synchronize — choose a source and the formats to read."),
			form,
			widget.NewLabel("Everything is in sync — no differences found."),
			widget.NewLabel("Source: "+s.src.Text),
			container.NewHBox(
				widget.NewButton("Refresh", func() {
					s.runSyncDiff()
				}),
				widget.NewButton("Back", func() {
					s.syncItems = nil
					s.syncChecked = nil
					s.screen = scrMenu
					s.render()
				}),
			),
		)
	}

	if s.syncChecked == nil {
		s.syncChecked = map[string]bool{}
		for _, it := range s.syncItems {
			key := syncItemGUIKey(it)
			s.syncChecked[key] = true
		}
	}

	list := container.NewVBox()
	for _, it := range s.syncItems {
		it := it
		key := syncItemGUIKey(it)
		prefix := "+"
		labelText := it.Name
		if it.Deleted {
			prefix = "− "
			labelText = "DELETED: " + it.Name
		}
		check := widget.NewCheck(prefix+" "+labelText, func(on bool) {
			s.syncChecked[key] = on
		})
		check.SetChecked(s.syncChecked[key])
		list.Add(check)
	}

	scroll := container.NewVScroll(list)
	scroll.SetMinSize(fyne.NewSize(620, 380))

	status := widget.NewLabel(fmt.Sprintf("%d differences — check the ones you want to process.", len(s.syncItems)))
	bottom := container.NewVBox(
		status,
		widget.NewLabel("Checked deleted files will be removed from the database. New files will be imported."),
		container.NewHBox(
			widget.NewButton("Apply", func() { go s.applySync() }),
			widget.NewButton("Select all / none", func() {
				all := true
				for _, it := range s.syncItems {
					if !s.syncChecked[syncItemGUIKey(it)] {
						all = false
						break
					}
				}
				for _, it := range s.syncItems {
					s.syncChecked[syncItemGUIKey(it)] = !all
				}
				s.render()
			}),
			widget.NewButton("Refresh", func() { s.runSyncDiff() }),
		),
	)

	return container.NewBorder(
		widget.NewLabel("Synchronize — choose a source and the formats to read."),
		bottom, nil, nil,
		container.NewVBox(form, scroll),
	)
}

func syncItemGUIKey(it core.SyncItem) string {
	if it.Deleted {
		return "del:" + it.Path
	}
	return "new:" + it.Path
}

func (s *state) formatsSummary() string {
	if s.importFormats == nil {
		return "all"
	}
	if len(s.importFormats) == 0 {
		return "none"
	}
	var exts []string
	for e := range s.importFormats {
		exts = append(exts, "."+e)
	}
	sort.Strings(exts)
	return strings.Join(exts, " ")
}

// ─── format selection ───────────────────────────────────────────────────────

func (s *state) formatsScreen() fyne.CanvasObject {
	formats := core.KnownFormats()
	if s.importFormats == nil {
		s.importFormats = make(core.FormatSet)
		for _, f := range formats {
			s.importFormats[f.Ext] = true
		}
	}

	status := widget.NewLabel("")
	checks := make([]*widget.Check, len(formats))

	updateStatus := func() {
		status.SetText(fmt.Sprintf("selected %d of %d formats", len(s.importFormats), len(formats)))
	}

	for i, f := range formats {
		f := f
		check := widget.NewCheck(fmt.Sprintf(".%-10s (%s)", f.Ext, f.Kind), func(on bool) {
			if on {
				s.importFormats[f.Ext] = true
			} else {
				delete(s.importFormats, f.Ext)
			}
			updateStatus()
		})
		check.SetChecked(s.importFormats[f.Ext])
		checks[i] = check
	}

	toggleAll := widget.NewButton("Select all / none", func() {
		selectAll := len(s.importFormats) < len(formats)
		for i, f := range formats {
			checks[i].SetChecked(selectAll)
			if selectAll {
				s.importFormats[f.Ext] = true
			} else {
				delete(s.importFormats, f.Ext)
			}
		}
		updateStatus()
	})

	updateStatus()

	list := container.NewVBox()
	for _, c := range checks {
		list.Add(c)
	}
	scroll := container.NewVScroll(list)
	scroll.SetMinSize(fyne.NewSize(620, 440))

	return container.NewBorder(
		container.NewVBox(
			widget.NewLabel("Everything checked = import every known format."),
			status,
			container.NewHBox(toggleAll, widget.NewButton("Done", func() {
				// Restore the canonical "all formats" value so formats added in
				// future releases are selected automatically.
				if len(s.importFormats) == len(formats) {
					s.importFormats = nil
				}
				s.persistSettings()
				s.screen = scrImport
				s.render()
			})),
		),
		nil, nil, nil,
		scroll,
	)
}

// ─── work with data ─────────────────────────────────────────────────────────

func (s *state) dataScreen() fyne.CanvasObject {
	items := []string{
		"Browse database",
		"Compare file (filesystem search)",
		"Re-extract documents, images & transcribe audio",
		"Search RAG (semantic)",
		"★ Evaluate note uniqueness",
	}
	list := widget.NewList(
		func() int { return len(items) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(items[id]) },
	)
	list.OnSelected = func(id widget.ListItemID) {
		switch id {
		case 0:
			s.openBrowse()
		case 1:
			s.openFsSearch()
		case 2:
			s.openExtract()
		case 3:
			s.openRAGSearch()
		case 4:
			s.openNotes(noteEval, scrData)
		}
	}

	return container.NewBorder(widget.NewLabel("Choose a data operation:"), nil, nil, nil, list)
}

// openExtract opens the re-extraction picker seeded with "all formats".
func (s *state) openExtract() {
	s.extractFilter = ""
	s.extractFormats = nil
	s.screen = scrExtract
	s.render()
}

// extractScreen lets the user choose which files to re-extract: by name and by
// format. Markdown is regenerated for every matched note.
func (s *state) extractScreen() fyne.CanvasObject {
	formats := core.ExtractableFormats()

	name := widget.NewEntry()
	name.SetPlaceHolder("filter by file name (empty = all files)")
	name.SetText(s.extractFilter)
	name.OnChanged = func(v string) { s.extractFilter = v }

	if s.extractFormats == nil {
		s.extractFormats = make(core.FormatSet)
		for _, f := range formats {
			s.extractFormats[f.Ext] = true
		}
	}

	status := widget.NewLabel("")
	checks := make([]*widget.Check, len(formats))
	updateStatus := func() {
		status.SetText(fmt.Sprintf("selected %d of %d formats", len(s.extractFormats), len(formats)))
	}
	for i, f := range formats {
		f := f
		check := widget.NewCheck(fmt.Sprintf(".%-10s (%s)", f.Ext, f.Kind), func(on bool) {
			if on {
				s.extractFormats[f.Ext] = true
			} else {
				delete(s.extractFormats, f.Ext)
			}
			updateStatus()
		})
		check.SetChecked(s.extractFormats[f.Ext])
		checks[i] = check
	}
	updateStatus()

	toggleAll := widget.NewButton("Select all / none", func() {
		selectAll := len(s.extractFormats) < len(formats)
		for i, f := range formats {
			checks[i].SetChecked(selectAll)
			if selectAll {
				s.extractFormats[f.Ext] = true
			} else {
				delete(s.extractFormats, f.Ext)
			}
		}
		updateStatus()
	})

	list := container.NewVBox()
	for _, c := range checks {
		list.Add(c)
	}
	scroll := container.NewVScroll(list)
	scroll.SetMinSize(fyne.NewSize(620, 360))

	run := widget.NewButton("Re-extract now", func() {
		s.startPhase(core.PhaseExtract, "", "")
	})
	run.Importance = widget.HighImportance

	return container.NewBorder(
		container.NewVBox(
			wrappedLabel("Markdown is regenerated for every matched file. Documents and audio use the local extractors; images are described by the vision model."),
			name,
			status,
			container.NewHBox(toggleAll, run),
		),
		nil, nil, nil,
		scroll,
	)
}

func (s *state) filteredNotes() []core.NoteInfo {
	if s.noteFilter == "" {
		return s.notes
	}
	lower := strings.ToLower(s.noteFilter)
	var out []core.NoteInfo
	for _, n := range s.notes {
		if strings.Contains(strings.ToLower(n.Name), lower) ||
			strings.Contains(strings.ToLower(n.Path), lower) {
			out = append(out, n)
		}
	}
	return out
}

func (s *state) notesScreen() fyne.CanvasObject {
	filter := widget.NewEntry()
	filter.SetPlaceHolder("start typing to filter by path…")
	filter.SetText(s.noteFilter)

	removing := s.noteAction == noteRemove || s.noteAction == noteRemoveLinks
	if removing && s.noteSelected == nil {
		s.noteSelected = map[int64]bool{}
	}
	if !removing {
		s.noteSelected = nil
	}

	selectedCount := func() int {
		n := 0
		for _, on := range s.noteSelected {
			if on {
				n++
			}
		}
		return n
	}

	var removeSel *widget.Button
	updateRemoveButton := func() {
		if removeSel != nil {
			removeSel.SetText(fmt.Sprintf("Remove selected (%d)", selectedCount()))
		}
	}

	list := widget.NewList(
		func() int { return len(s.filteredNotes()) },
		func() fyne.CanvasObject {
			if removing {
				return container.NewHBox(widget.NewCheck("", nil), widget.NewLabel(""))
			}
			return widget.NewLabel("")
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			notes := s.filteredNotes()
			if id >= len(notes) {
				return
			}
			n := notes[id]
			if removing {
				box := o.(*fyne.Container)
				check := box.Objects[0].(*widget.Check)
				label := box.Objects[1].(*widget.Label)
				// SetChecked fires OnChanged, so silence it while we sync the
				// visual state with our own selection set.
				check.OnChanged = nil
				check.SetChecked(s.noteSelected[n.ID])
				check.OnChanged = func(on bool) {
					if on {
						s.noteSelected[n.ID] = true
					} else {
						delete(s.noteSelected, n.ID)
					}
					updateRemoveButton()
				}
				label.SetText(n.Path)
			} else if s.noteAction == noteEval {
				o.(*widget.Label).SetText(n.Name)
			} else {
				o.(*widget.Label).SetText(n.Path)
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		notes := s.filteredNotes()
		list.UnselectAll()
		if id < len(notes) {
			s.pickNote(notes[id])
		}
	}

	count := widget.NewLabel("")
	updateCount := func() {
		count.SetText(fmt.Sprintf("found: %d", len(s.filteredNotes())))
	}

	filter.OnChanged = func(v string) {
		s.noteFilter = v
		list.UnselectAll()
		list.Refresh()
		updateCount()
	}
	updateCount()

	header := container.NewVBox(filter, count)

	if removing {
		selectAll := widget.NewButton("Select all shown", func() {
			for _, n := range s.filteredNotes() {
				s.noteSelected[n.ID] = true
			}
			list.Refresh()
			updateRemoveButton()
		})
		clear := widget.NewButton("Clear selection", func() {
			s.noteSelected = map[int64]bool{}
			list.Refresh()
			updateRemoveButton()
		})
		removeSel = widget.NewButton(fmt.Sprintf("Remove selected (%d)", selectedCount()), func() {
			s.removeSelectedNotes(s.noteAction == noteRemoveLinks)
		})
		removeSel.Importance = widget.DangerImportance
		header.Add(container.NewHBox(selectAll, clear, removeSel))
	}

	return container.NewBorder(
		header,
		widget.NewLabel("Tick files to remove several at once, or select a file to continue — ← Back returns."),
		nil, nil,
		list,
	)
}

// ─── database browser ───────────────────────────────────────────────────────

// browseColumnOrder is the order in which columns are shown and toggled.
var browseColumnOrder = []string{"path", "format", "imported", "tags", "links"}

var browseColumnLabels = map[string]string{
	"path":     "Path",
	"format":   "Format",
	"imported": "Imported",
	"tags":     "Tags",
	"links":    "Links",
}

// defaultBrowseCols selects the columns shown when the browser is first opened.
func defaultBrowseCols() map[string]bool {
	return map[string]bool{
		"path":   true,
		"format": true,
		"tags":   true,
		"links":  true,
	}
}

func (s *state) filteredBrowseNotes() []core.NoteDetail {
	filter := strings.ToLower(strings.TrimSpace(s.browseFilter))
	if filter == "" {
		return s.browseNotes
	}
	var out []core.NoteDetail
	for _, n := range s.browseNotes {
		if strings.Contains(strings.ToLower(n.FilePath), filter) ||
			strings.Contains(strings.ToLower(n.FileName), filter) ||
			strings.Contains(strings.ToLower(n.MimeType), filter) ||
			strings.Contains(strings.ToLower(n.Tags), filter) {
			out = append(out, n)
		}
	}
	return out
}

func (s *state) browseHeader() string {
	var parts []string
	for _, key := range browseColumnOrder {
		if s.browseCols[key] {
			parts = append(parts, browseColumnLabels[key])
		}
	}
	if len(parts) == 0 {
		return "File"
	}
	return strings.Join(parts, "  ·  ")
}

func (s *state) browseRowText(n core.NoteDetail) string {
	var parts []string
	if s.browseCols["path"] {
		parts = append(parts, n.FilePath)
	}
	if s.browseCols["format"] {
		parts = append(parts, n.Format())
	}
	if s.browseCols["imported"] {
		parts = append(parts, n.ImportedAt)
	}
	if s.browseCols["tags"] {
		tags := n.Tags
		if tags == "" {
			tags = "—"
		}
		parts = append(parts, tags)
	}
	if s.browseCols["links"] {
		parts = append(parts, fmt.Sprintf("%d", n.Links))
	}
	if len(parts) == 0 {
		return n.FileName
	}
	return strings.Join(parts, "  ·  ")
}

func (s *state) browseScreen() fyne.CanvasObject {
	filter := widget.NewEntry()
	filter.SetPlaceHolder("filter by path, format or tags…")
	filter.SetText(s.browseFilter)

	list := widget.NewList(
		func() int { return len(s.filteredBrowseNotes()) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			notes := s.filteredBrowseNotes()
			if id < len(notes) {
				o.(*widget.Label).SetText(s.browseRowText(notes[id]))
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		notes := s.filteredBrowseNotes()
		list.UnselectAll()
		if id < len(notes) {
			s.browseBack = scrBrowse
			s.openBrowseDetail(notes[id])
		}
	}

	count := widget.NewLabel("")
	updateCount := func() {
		count.SetText(fmt.Sprintf("found: %d", len(s.filteredBrowseNotes())))
	}
	filter.OnChanged = func(v string) {
		s.browseFilter = v
		list.UnselectAll()
		list.Refresh()
		updateCount()
	}
	updateCount()

	header := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	refreshHeader := func() { header.SetText(s.browseHeader()) }
	refreshHeader()

	cols := container.NewHBox(widget.NewLabel("Columns:"))
	for _, key := range browseColumnOrder {
		key := key
		check := widget.NewCheck(browseColumnLabels[key], func(on bool) {
			s.browseCols[key] = on
			list.Refresh()
			refreshHeader()
		})
		check.SetChecked(s.browseCols[key])
		cols.Add(check)
	}

	top := container.NewVBox(
		filter,
		container.NewBorder(nil, nil, nil, count, cols),
		header,
	)

	return container.NewBorder(
		top,
		widget.NewLabel("Type to filter by path/format/tags · select a row to view the full record · ← Back returns."),
		nil, nil, list,
	)
}

// browseDetailScreen shows every column of a single note and allows removing
// it from the database.
func (s *state) browseDetailScreen() fyne.CanvasObject {
	if len(s.browseDetail) == 0 {
		return container.NewVBox(widget.NewLabel("No data."))
	}

	content := container.NewVBox()
	for _, f := range s.browseDetail {
		content.Add(widget.NewLabelWithStyle(f.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		value := widget.NewLabel(f.Value)
		value.Wrapping = fyne.TextWrapWord
		value.Selectable = true
		content.Add(value)
		content.Add(widget.NewSeparator())
	}
	scroll := container.NewVScroll(content)
	scroll.SetMinSize(fyne.NewSize(720, 420))

	remove := widget.NewButton("Remove from database", s.removeBrowseNote)
	remove.Importance = widget.DangerImportance

	return container.NewBorder(
		widget.NewLabel("Full record — column name and value."),
		container.NewHBox(
			remove,
			widget.NewButton("Back", func() {
				s.screen = s.browseBack
				s.render()
			}),
		),
		nil, nil, scroll,
	)
}

// ─── filesystem search (compare) ────────────────────────────────────────────

func (s *state) fsSearchScreen() fyne.CanvasObject {
	filter := widget.NewEntry()
	filter.SetPlaceHolder("type to search files…")
	filter.SetText(s.fsFilter)

	list := widget.NewList(
		func() int { return len(s.fsFiles) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < len(s.fsFiles) {
				o.(*widget.Label).SetText(s.relativePath(s.fsFiles[id]))
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		if id < len(s.fsFiles) {
			s.startCompare(s.fsFiles[id])
		}
	}

	count := widget.NewLabel("")
	updateCount := func() {
		count.SetText(fmt.Sprintf("found: %d", len(s.fsFiles)))
	}
	updateCount()

	filter.OnChanged = func(v string) {
		s.fsFilter = v
		list.UnselectAll()
		s.searchFiles(v, func(files []string) {
			s.fsFiles = files
			list.UnselectAll()
			list.Refresh()
			updateCount()
		})
	}

	return container.NewBorder(
		container.NewVBox(filter, count),
		widget.NewLabel("Select a file to evaluate its novelty — ← Back returns."),
		nil, nil,
		list,
	)
}

func (s *state) relativePath(path string) string {
	if cwd, err := os.Getwd(); err == nil {
		return strings.TrimPrefix(path, cwd+string(os.PathSeparator))
	}
	return path
}

// ─── semantic (RAG) search ──────────────────────────────────────────────────

func (s *state) ragSearchScreen() fyne.CanvasObject {
	db := s.currentDB()
	idxNotes, idxChunks, _ := core.RAGStats(db)
	model := core.NormalizeEmbeddingModel(s.embedding.Text)

	status := "Embedding model: " + model
	if idxChunks > 0 {
		status += fmt.Sprintf("   ·   indexed: %d note(s) / %d chunk(s)", idxNotes, idxChunks)
	} else {
		status += "   ·   index empty — run the \"RAG index\" one-by-one process first."
	}

	query := widget.NewEntry()
	query.SetPlaceHolder("ask a question about your notes…")
	query.SetText(s.ragQuery)
	query.OnChanged = func(v string) { s.ragQuery = v }
	query.OnSubmitted = func(string) { s.runRAGSearch(false) }

	searchBtn := widget.NewButton("Search", func() { s.runRAGSearch(false) })
	askBtn := widget.NewButton("Ask", func() { s.runRAGSearch(true) })
	askBtn.Importance = widget.HighImportance

	resultsEntry := s.ragTopKEntry
	resultsEntry.SetPlaceHolder(strconv.Itoa(s.ragTopK))
	results := container.NewHBox(widget.NewLabel("Results:"), container.NewGridWrap(fyne.NewSize(60, resultsEntry.MinSize().Height), resultsEntry))

	var feedback fyne.CanvasObject
	switch {
	case s.ragBusy:
		feedback = widget.NewLabel("Searching…")
	case s.ragErr != "":
		feedback = wrappedLabel("Error: " + s.ragErr)
	case s.ragAnswer != "":
		answer := widget.NewLabel(s.ragAnswer)
		answer.Wrapping = fyne.TextWrapWord
		answer.Selectable = true
		feedback = container.NewVBox(
			widget.NewLabelWithStyle("Answer:", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			answer,
		)
	default:
		feedback = widget.NewLabel("")
	}

	list := widget.NewList(
		func() int { return len(s.ragResults) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < len(s.ragResults) {
				r := s.ragResults[id]
				o.(*widget.Label).SetText(fmt.Sprintf("[%.3f]  %s — %s", r.Score, r.Path, ragSnippet(r.Chunk)))
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		if id >= len(s.ragResults) {
			return
		}
		list.UnselectAll()
		r := s.ragResults[id]
		s.browseBack = scrRAGSearch
		s.openBrowseDetail(core.NoteDetail{ID: r.NoteID, FileName: r.Name, FilePath: r.Path})
	}

	top := container.NewVBox(
		wrappedLabel(status),
		container.NewBorder(nil, nil, nil, container.NewHBox(results, searchBtn, askBtn), query),
		feedback,
	)

	return container.NewBorder(
		top,
		wrappedLabel("Search ranks notes by meaning. Ask also answers the question from the top matches. Set Results to choose how many matches to return. Select a result to open the full note record."),
		nil, nil, list,
	)
}

// ragSnippet collapses whitespace and truncates a chunk for list display.
func ragSnippet(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if rs := []rune(s); len(rs) > 160 {
		s = string(rs[:160]) + "…"
	}
	return s
}

// ─── evaluation result ──────────────────────────────────────────────────────

func (s *state) evalScreen() fyne.CanvasObject {
	if s.evalLoading {
		return container.NewVBox(
			widget.NewLabel("Analyzing note…"),
			widget.NewLabel("Sending it to ollama. This can take a while."),
		)
	}
	if s.evalErr != "" {
		label := widget.NewLabel("Error: " + s.evalErr)
		label.Wrapping = fyne.TextWrapWord
		return container.NewVBox(label, widget.NewButton("Back", func() {
			s.screen = s.evalBack
			s.render()
		}))
	}

	var res struct {
		Novelty string `json:"novelty"`
		Summary string `json:"summary"`
		NewInfo string `json:"new_info"`
	}

	content := container.NewVBox()
	if err := json.Unmarshal([]byte(s.evalResult), &res); err != nil {
		raw := widget.NewLabel(s.evalResult)
		raw.Wrapping = fyne.TextWrapWord
		raw.Selectable = true
		content.Add(raw)
	} else {
		content.Add(widget.NewLabelWithStyle("Novelty: "+res.Novelty, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		content.Add(widget.NewLabelWithStyle("Summary:", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		summary := widget.NewLabel(res.Summary)
		summary.Wrapping = fyne.TextWrapWord
		summary.Selectable = true
		content.Add(summary)
		content.Add(widget.NewLabelWithStyle("New information:", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		info := widget.NewLabel(res.NewInfo)
		info.Wrapping = fyne.TextWrapWord
		info.Selectable = true
		content.Add(info)
	}

	scroll := container.NewVScroll(content)
	scroll.SetMinSize(fyne.NewSize(720, 320))

	return container.NewBorder(
		nil,
		widget.NewButton("Back", func() {
			s.screen = s.evalBack
			s.render()
		}),
		nil, nil, scroll,
	)
}

// ─── settings ───────────────────────────────────────────────────────────────

func (s *state) settingsScreen() fyne.CanvasObject {
	modelRow := container.NewBorder(nil, nil, nil, widget.NewButton("Fetch…", s.pickModel), s.model)
	embeddingRow := container.NewBorder(nil, nil, nil, widget.NewButton("Fetch…", func() {
		s.pickOllamaModel("Choose embedding model", s.embedding)
	}), s.embedding)
	whisperValue := container.NewVBox(s.whisperModel, wrappedLabel(s.whisperModelStatus()))
	whisperRow := container.NewBorder(nil, nil, nil, widget.NewButton("Manage…", s.openWhisperManager), whisperValue)

	form := widget.NewForm(
		widget.NewFormItem("SRC", s.src),
		widget.NewFormItem("OUT", s.out),
		widget.NewFormItem("DB", s.dbEntry),
		widget.NewFormItem("MODEL", modelRow),
		widget.NewFormItem("VISION MODEL", s.vision),
		widget.NewFormItem("EMBEDDING MODEL", embeddingRow),
		widget.NewFormItem("AUDIO", s.audioEngine),
		widget.NewFormItem("WHISPER MODEL", whisperRow),
		widget.NewFormItem("WHISPER GPU", s.whisperBackend),
		widget.NewFormItem("GPU DEVICE", s.whisperDevice),
		widget.NewFormItem("GOOGLE API KEY", s.googleKey),
		widget.NewFormItem("SPEECH LANGUAGE", s.speechLanguage),
		widget.NewFormItem("HOST", s.host),
		widget.NewFormItem("MAXCHARS", s.maxChars),
		widget.NewFormItem("WORKERS", s.workers),
		widget.NewFormItem("SKIP DIRS", s.skipDirs),
	)

	audioHint := wrappedLabel("Local Whisper runs offline after downloading a model and auto-detects the language. GPU acceleration is automatic: noesis uses Vulkan when your graphics driver supports it (Linux/Windows), Apple Metal on macOS, and falls back to the CPU otherwise — no extra setup. Google Speech-to-Text needs an API key (stored in the database) and ffmpeg.")
	ffmpegHelp := fyne.CanvasObject(wrappedLabel("ffmpeg: installed."))
	if !core.FFmpegAvailable() {
		ffmpegHelp = container.NewVBox(
			wrappedLabel("ffmpeg: not found — it is needed for Google and some audio formats."),
			docsLink("Install ffmpeg", core.FFmpegDownloadURL),
			wrappedLabel(core.FFmpegInstallExample()),
		)
	}
	audioHelp := container.NewVBox(
		widget.NewLabelWithStyle("Audio setup help", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		wrappedLabel("Local Whisper: "+core.LocalWhisperSetupNote()),
		wrappedLabel("GPU acceleration needs no setup: noesis downloads the right whisper.cpp runtime and picks the GPU automatically when available. Keep your graphics driver up to date. Choose CPU only if the GPU driver crashes; device 0 is usually the default adapter."),
		container.NewHBox(
			docsLink("whisper.cpp project", core.WhisperCPPBuildURL),
			docsLink("Homebrew formula (macOS)", core.WhisperCPPHomebrewURL),
		),
		widget.NewSeparator(),
		wrappedLabel("Google Speech-to-Text: enable the API (billing required), create an API key, then install ffmpeg. Choose a specific language; Google handles about one minute per file."),
		container.NewHBox(
			docsLink("Enable Speech-to-Text", core.GoogleSpeechEnableURL),
			docsLink("Create API key", core.GoogleCredentialsURL),
		),
		ffmpegHelp,
		docsLink("Speech-to-Text documentation", core.GoogleSpeechDocsURL),
	)

	reset := widget.NewButton("Reset database", s.confirmReset)
	reset.Importance = widget.DangerImportance

	clearRAG := widget.NewButton("Reset embeddings", s.confirmClearRAG)
	clearRAG.Importance = widget.DangerImportance

	scroll := container.NewVScroll(container.NewVBox(
		form,
		wrappedLabel("VISION MODEL describes images during import, sync and re-extraction. Leave it empty to reuse MODEL."),
		wrappedLabel("SKIP DIRS is a comma-separated list of directory name globs skipped during import and sync, e.g. .private,node_modules. Empty skips nothing; the default (.*) skips hidden folders."),
		wrappedLabel(s.embeddingHint()),
		audioHint,
		audioHelp,
		widget.NewSeparator(),
		widget.NewLabel(s.ollamaStatus),
		widget.NewButton("Edit eval prompt…", func() {
			s.openPrompt("eval", scrSettings)
		}),
		clearRAG,
		reset,
	))
	scroll.SetMinSize(fyne.NewSize(720, 380))

	return scroll
}

// embeddingHint describes the EMBEDDING MODEL setting and, when the selected
// model is not installed, suggests how to install it. When the existing index
// was built with a different model it points at "Reset embeddings".
func (s *state) embeddingHint() string {
	hint := "EMBEDDING MODEL builds the semantic (RAG) index used by \"Work with data → Search RAG\". Pick one with Fetch… or type a name."
	if sug := core.EmbeddingModelSuggestion(s.ollamaModels, s.embedding.Text); sug != "" {
		hint += " The selected model is " + sug + "."
	}
	if _, chunks, models := core.RAGStats(s.currentDB()); chunks > 0 {
		model := core.NormalizeEmbeddingModel(s.embedding.Text)
		stale := true
		for _, m := range models {
			if m == model {
				stale = false
				break
			}
		}
		if stale {
			hint += " The index was built with " + strings.Join(models, ", ") + " — use \"Reset embeddings\" and run the RAG index again."
		}
	}
	return hint
}

// ─── whisper manager ────────────────────────────────────────────────────────

func (s *state) selectedWhisperBackend() string {
	return core.NormalizeWhisperBackend(s.whisperBackend.Selected)
}

func (s *state) whisperDeviceValue() int {
	device, err := strconv.Atoi(strings.TrimSpace(s.whisperDevice.Text))
	if err != nil || device < 0 {
		return core.DefaultWhisperDevice
	}
	return device
}

func (s *state) whisperModelStatus() string {
	name := core.NormalizeWhisperModel(s.whisperModel.Selected)
	if !core.WhisperModelInstalled(name) {
		return "Not downloaded — use Manage… to download it."
	}
	backend := s.selectedWhisperBackend()
	info := core.WhisperRuntimeInfoFor(backend)
	if info.Path == "" {
		if backend == core.WhisperBackendVulkan {
			return "Model downloaded; Vulkan runtime not installed — use Manage…"
		}
		return "Model downloaded; runtime not installed — use Manage…"
	}
	if backend == core.WhisperBackendVulkan && info.Backend == core.WhisperBackendCPU {
		return "Model downloaded; the selected runtime is CPU-only — install Vulkan in Manage…"
	}
	return "Downloaded and ready (" + info.Label() + ")."
}

func (s *state) openWhisperManager() {
	s.whisperModels = core.WhisperModels()
	s.whisperStatus = ""
	s.whisperErr = ""
	s.screen = scrWhisper
	s.render()
}

func (s *state) useWhisperModel(name string) {
	s.whisperModel.SetSelected(core.NormalizeWhisperModel(name))
	s.persistSettings()
	if core.WhisperRuntimeInstalledFor(s.selectedWhisperBackend()) {
		s.whisperStatus = name + " selected (" + core.WhisperBackendLabel(s.selectedWhisperBackend()) + ")."
	} else {
		s.whisperStatus = name + " selected. Install whisper-cli to use it — see the instructions above."
	}
	s.whisperErr = ""
	s.render()
}

func (s *state) deleteWhisperModel(name string) {
	if err := core.DeleteWhisperModel(name); err != nil {
		s.whisperErr = err.Error()
		s.render()
		return
	}
	if s.whisperModel.Selected == name {
		s.whisperModel.SetSelected(core.DefaultWhisperModel)
		s.persistSettings()
	}
	s.whisperModels = core.WhisperModels()
	s.whisperStatus = name + " deleted."
	s.whisperErr = ""
	s.render()
}

func (s *state) startWhisperDownload(model string) {
	s.startWhisperDownloadFor(model, s.selectedWhisperBackend())
}

func (s *state) startWhisperDownloadFor(model, backend string) {
	if s.whisperBusy {
		return
	}
	backend = core.NormalizeWhisperBackend(backend)
	if model == "" && !core.WhisperRuntimeInstallSupportedFor(backend) {
		s.whisperErr = core.WhisperRuntimeInstallInstructions()
		s.whisperStatus = ""
		s.render()
		return
	}
	s.whisperBusy = true
	s.whisperProgress = 0
	s.whisperErr = ""
	s.whisperStatus = ""
	if model == "" {
		s.whisperStage = "runtime"
		if backend == core.WhisperBackendVulkan {
			s.whisperStage = "Vulkan runtime"
		}
	} else {
		s.whisperStage = "model " + model
	}
	s.render()

	go func() {
		ctx := context.Background()
		lastStage, lastPct := "", -1
		updateProgress := func(stage string, done, total int64) {
			pct := 0.0
			if total > 0 {
				pct = float64(done) * 100 / float64(total)
			}
			rounded := int(pct)
			if stage == lastStage && rounded == lastPct && total > 0 {
				return
			}
			lastStage, lastPct = stage, rounded
			fyne.Do(func() {
				s.whisperStage = stage
				s.whisperProgress = pct
				s.render()
			})
		}

		if core.WhisperRuntimeNeedsInstall(backend) {
			updateProgress("runtime", 0, 0)
			if err := core.EnsureWhisperRuntimeFor(ctx, backend, func(done, total int64) {
				updateProgress("runtime", done, total)
			}); err != nil {
				fyne.Do(func() { s.finishWhisperDownload(model, backend, err) })
				return
			}
		}
		if model != "" {
			updateProgress("model "+model, 0, 0)
			if err := core.DownloadWhisperModel(ctx, model, func(done, total int64) {
				updateProgress("model "+model, done, total)
			}); err != nil {
				fyne.Do(func() { s.finishWhisperDownload(model, backend, err) })
				return
			}
		}
		fyne.Do(func() { s.finishWhisperDownload(model, backend, nil) })
	}()
}

func (s *state) finishWhisperDownload(model, backend string, err error) {
	s.whisperBusy = false
	s.whisperProgress = 0
	if err != nil {
		s.whisperErr = err.Error()
		s.whisperStatus = ""
	} else {
		s.whisperErr = ""
		s.whisperModels = core.WhisperModels()
		if model == "" {
			if backend == core.WhisperBackendVulkan {
				s.whisperStatus = "Vulkan whisper.cpp runtime installed."
			} else {
				s.whisperStatus = "whisper.cpp runtime installed."
			}
		} else {
			s.whisperModel.SetSelected(core.NormalizeWhisperModel(model))
			s.persistSettings()
			if core.WhisperRuntimeInstalledFor(backend) {
				s.whisperStatus = model + " downloaded and selected (" + core.WhisperBackendLabel(backend) + ")."
			} else {
				s.whisperStatus = model + " downloaded. Install a " + core.WhisperBackendLabel(backend) + " runtime in Manage…."
			}
		}
	}
	s.render()
}

func (s *state) whisperScreen() fyne.CanvasObject {
	policy := s.selectedWhisperBackend()
	info := core.WhisperRuntimeInfoFor(policy)
	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord
	text := fmt.Sprintf("GPU policy: %s · device %d\nRuntime: %s", core.WhisperBackendLabel(policy), s.whisperDeviceValue(), info.Label())
	if info.Path != "" {
		text += "\n" + info.Path
	}
	status.SetText(text)

	rows := container.NewVBox()
	rows.Add(status)
	rows.Add(wrappedLabel(core.LocalWhisperSetupNote()))
	if info.Path == "" || (policy == core.WhisperBackendVulkan && info.Backend == core.WhisperBackendCPU) {
		rows.Add(wrappedLabel(core.WhisperRuntimeInstallInstructions()))
		rows.Add(container.NewHBox(
			docsLink("whisper.cpp installation guide", core.WhisperCPPBuildURL),
			docsLink("Homebrew formula (macOS)", core.WhisperCPPHomebrewURL),
			docsLink("Download builds", core.WhisperCPPReleasesURL),
		))
	}
	if core.WhisperRuntimeNeedsInstall(policy) && core.WhisperRuntimeInstallSupportedFor(policy) {
		label := "Download runtime"
		if policy == core.WhisperBackendVulkan {
			label = "Download Vulkan runtime"
		}
		rows.Add(widget.NewButton(label, func() {
			s.startWhisperDownloadFor("", policy)
		}))
	}
	rows.Add(widget.NewSeparator())

	for _, info := range s.whisperModels {
		info := info
		state := "not downloaded"
		if info.Downloaded {
			state = "downloaded"
		}
		if info.Name == s.whisperModel.Selected {
			state += " · active"
		}
		label := widget.NewLabel(fmt.Sprintf("%s — %s — %s", info.Name, formatBytes(info.Size), state))
		var actions *fyne.Container
		if info.Downloaded {
			actions = container.NewHBox(
				widget.NewButton("Use", func() { s.useWhisperModel(info.Name) }),
				widget.NewButton("Delete", func() { s.deleteWhisperModel(info.Name) }),
			)
		} else {
			actions = container.NewHBox(widget.NewButton("Download", func() {
				s.startWhisperDownload(info.Name)
			}))
		}
		rows.Add(container.NewBorder(nil, nil, nil, actions, label))
	}

	rows.Add(widget.NewSeparator())
	if s.whisperBusy {
		label := s.whisperStage
		if label == "" {
			label = "download"
		}
		rows.Add(widget.NewLabel("Downloading " + label + "…"))
		bar := widget.NewProgressBar()
		bar.SetValue(s.whisperProgress / 100)
		rows.Add(bar)
	}
	if s.whisperErr != "" {
		errLabel := widget.NewLabel(s.whisperErr)
		errLabel.Wrapping = fyne.TextWrapWord
		errLabel.Importance = widget.DangerImportance
		rows.Add(errLabel)
	}
	if s.whisperStatus != "" {
		rows.Add(widget.NewLabel(s.whisperStatus))
	}

	scroll := container.NewVScroll(rows)
	scroll.SetMinSize(fyne.NewSize(760, 420))
	return container.NewBorder(nil, widget.NewButton("Back", func() {
		s.screen = scrSettings
		s.render()
	}), nil, nil, scroll)
}

// ─── prompts ────────────────────────────────────────────────────────────────

func (s *state) promptVarsHint(name string) string {
	db := s.currentDB()
	ptype := core.LoadPromptType(db, name)
	var named []string
	switch {
	case ptype == "pair":
		named = []string{"${A_NAME}", "${A_CONTENT}", "${B_NAME}", "${B_CONTENT}"}
	default:
		named = []string{"${FILE_NAME}", "${CONTENT}", "${LINKED_NOTES}"}
	}
	parts := append([]string{}, named...)
	cols := core.NotesColumns(db)
	for _, c := range cols {
		parts = append(parts, "${db."+c+"}")
	}
	if ptype == "pair" {
		for _, c := range cols {
			parts = append(parts, "${db_b."+c+"}")
		}
	}
	return "Variables: " + strings.Join(parts, "  ")
}

func (s *state) promptScreen() fyne.CanvasObject {
	hint := widget.NewLabel(s.promptVarsHint(s.promptName))
	hint.Wrapping = fyne.TextWrapWord

	if s.promptEdit {
		editor := widget.NewMultiLineEntry()
		editor.SetText(s.promptText)
		editor.SetMinRowsVisible(18)

		save := func() {
			s.promptText = editor.Text
			s.savePrompt()
		}
		return container.NewBorder(
			hint,
			container.NewHBox(
				widget.NewButton("Save", save),
				widget.NewButton("Cancel", func() {
					s.promptEdit = false
					s.render()
				}),
				widget.NewLabel("Save to keep your changes."),
			),
			nil, nil, editor,
		)
	}

	body := widget.NewLabel(s.promptText)
	body.Wrapping = fyne.TextWrapWord
	body.Selectable = true
	scroll := container.NewVScroll(body)
	scroll.SetMinSize(fyne.NewSize(740, 400))

	return container.NewBorder(
		hint,
		container.NewHBox(
			widget.NewButton("Edit", func() {
				s.promptEdit = true
				s.render()
			}),
			widget.NewButton("Reload from file", s.reloadPrompt),
			widget.NewButton("Back", func() {
				s.screen = s.promptBack
				s.render()
			}),
		),
		nil, nil, scroll,
	)
}

// ─── processes ──────────────────────────────────────────────────────────────

func (s *state) processNames() []string {
	db := s.currentDB()
	if db == nil {
		return nil
	}
	names, _ := core.ListPromptsByType(db, s.processType)
	if len(names) == 0 {
		if s.processType == "pair" {
			return []string{core.DefaultPairPrompt}
		}
		return []string{"tag_single"}
	}
	return names
}

func (s *state) processListScreen() fyne.CanvasObject {
	names := s.processNames()

	items := make([]string, 0, len(names)+1)
	items = append(items, names...)
	items = append(items, "+ Add new "+s.processType+" process")

	list := widget.NewList(
		func() int { return len(items) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < len(items) {
				o.(*widget.Label).SetText(items[id])
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		list.UnselectAll()
		idx := int(id)
		if idx < len(names) {
			s.processName = names[idx]
			s.screen = scrProcessDetail
			s.render()
			return
		}
		s.addProcess()
	}

	return container.NewBorder(
		widget.NewLabel("Select a process to run, export or edit its prompt."),
		nil, nil, nil, list,
	)
}

func (s *state) processDetailScreen() fyne.CanvasObject {
	runPhase := core.PhaseTagSingle
	runLabel := "Run on every note"
	if s.processType == "pair" {
		runPhase = core.PhaseAnalyze
		runLabel = "Run on every pair"
	}

	return container.NewVBox(
		widget.NewLabelWithStyle("Process: "+s.processName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewButton(runLabel, func() {
			s.startPhase(runPhase, s.processName, "")
		}),
		widget.NewButton("Edit prompt", func() {
			s.openPrompt(s.processName, scrProcessDetail)
		}),
		widget.NewButton("Rename", s.renameProcess),
		widget.NewButton("Delete", s.deleteProcess),
		widget.NewButton("Back", func() {
			s.openProcessList(s.processType)
		}),
	)
}

// ─── export templates ───────────────────────────────────────────────────────

func (s *state) exportTemplatesScreen() fyne.CanvasObject {
	items := append(append([]string{}, s.exportTemplates...), "+ Add new export template")

	list := widget.NewList(
		func() int { return len(items) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if int(id) < len(items) {
				o.(*widget.Label).SetText(items[id])
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		list.UnselectAll()
		if int(id) < len(s.exportTemplates) {
			s.openExportTemplateDetail(s.exportTemplates[id])
			return
		}
		s.addExportTemplate()
	}

	return container.NewBorder(
		widget.NewLabel("Select a template to export with, or add a new one."),
		nil, nil, nil, list,
	)
}

func (s *state) exportTemplateDetailScreen() fyne.CanvasObject {
	if s.templateEdit {
		editor := widget.NewMultiLineEntry()
		editor.SetText(s.templateText)
		editor.SetMinRowsVisible(18)

		save := func() {
			db := s.currentDB()
			if db == nil {
				return
			}
			if err := core.SaveExportTemplate(db, s.templateName, editor.Text); err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			s.templateText = editor.Text
			s.templateEdit = false
			s.render()
		}

		hint := widget.NewLabel("Variables: ${frontmatter_block} ${frontmatter} ${body} ${related} " +
			"${links} ${tags} ${tags_yaml} ${tags_inline} ${name} ${title} ${path} ${db.<column>}")
		hint.Wrapping = fyne.TextWrapWord

		return container.NewBorder(
			hint,
			container.NewHBox(
				widget.NewButton("Save", save),
				widget.NewButton("Cancel", func() {
					s.templateEdit = false
					s.render()
				}),
			),
			nil, nil, editor,
		)
	}

	preview := widget.NewLabel(s.templateText)
	preview.Wrapping = fyne.TextWrapWord
	preview.Selectable = true
	scroll := container.NewVScroll(preview)
	scroll.SetMinSize(fyne.NewSize(740, 260))

	hint := widget.NewLabel("Variables: ${frontmatter_block} ${frontmatter} ${body} ${related} " +
		"${links} ${tags} ${tags_yaml} ${tags_inline} ${name} ${title} ${path} ${db.<column>}")
	hint.Wrapping = fyne.TextWrapWord

	return container.NewBorder(
		widget.NewLabelWithStyle("Export template: "+s.templateName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewVBox(
			widget.NewButton("Export with this template", func() {
				s.startPhase(core.PhaseWrite, "", s.templateName)
			}),
			widget.NewButton("Edit template", func() {
				s.templateEdit = true
				s.render()
			}),
			widget.NewButton("Rename", s.renameExportTemplate),
			widget.NewButton("Delete", s.deleteExportTemplate),
			widget.NewButton("Back", s.openExportTemplates),
		),
		nil, nil,
		container.NewVBox(
			widget.NewSeparator(),
			hint,
			scroll,
		),
	)
}

// ─── running ────────────────────────────────────────────────────────────────

func (s *state) runningScreen() fyne.CanvasObject {
	return container.NewVBox(
		widget.NewLabel("Processing…"),
		widget.NewLabel("Progress, the current file and the log are shown in the panel below."),
		widget.NewButton("Stop", s.stopRun),
	)
}

func formatBytes(n int64) string {
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

func wrappedLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}

func docsLink(label, target string) *widget.Hyperlink {
	u, _ := url.Parse(target)
	return widget.NewHyperlink(label, u)
}
