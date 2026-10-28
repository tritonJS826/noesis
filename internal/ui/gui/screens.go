package gui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		"SRC: %s   ·   OUT: %s   ·   DB: %s   ·   MODEL: %s   ·   HOST: %s   ·   MAXCHARS: %s   ·   WORKERS: %s",
		s.src.Text, s.out.Text, s.dbEntry.Text, s.model.Text, s.host.Text, s.maxChars.Text, s.workers.Text,
	))
	summary.Wrapping = fyne.TextWrapWord

	ollama := widget.NewLabel(s.ollamaStatus)
	ollama.Wrapping = fyne.TextWrapWord

	return container.NewBorder(
		widget.NewLabel("Choose an action:"),
		container.NewVBox(
			summary,
			ollama,
			widget.NewLabelWithStyle(
				"Select an item to open it. The status and log panel stays at the bottom.",
				fyne.TextAlignLeading, fyne.TextStyle{Italic: true},
			),
		),
		nil, nil, list,
	)
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
		"Remove file",
		"Remove file + Obsidian links",
		"Compare file (filesystem search)",
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
			s.openNotes(noteRemove, scrData)
		case 2:
			s.openNotes(noteRemoveLinks, scrData)
		case 3:
			s.openFsSearch()
		}
	}

	return container.NewBorder(widget.NewLabel("Choose a data operation:"), nil, nil, nil, list)
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

	list := widget.NewList(
		func() int { return len(s.filteredNotes()) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			notes := s.filteredNotes()
			if id < len(notes) {
				if s.noteAction == noteEval {
					o.(*widget.Label).SetText(notes[id].Name)
				} else {
					o.(*widget.Label).SetText(notes[id].Path)
				}
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

	return container.NewBorder(
		container.NewVBox(filter, count),
		widget.NewLabel("Select a file to continue — ← Back returns."),
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
			strings.Contains(strings.ToLower(n.MimeType), filter) {
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
	filter.SetPlaceHolder("filter by path or format…")
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
			s.removeBrowseNote(notes[id])
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
		widget.NewLabel("Type to filter by path/format · select a row to remove it · ← Back returns."),
		nil, nil, list,
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

	form := widget.NewForm(
		widget.NewFormItem("SRC", s.src),
		widget.NewFormItem("OUT", s.out),
		widget.NewFormItem("DB", s.dbEntry),
		widget.NewFormItem("MODEL", modelRow),
		widget.NewFormItem("HOST", s.host),
		widget.NewFormItem("MAXCHARS", s.maxChars),
		widget.NewFormItem("WORKERS", s.workers),
	)

	reset := widget.NewButton("Reset database", s.confirmReset)
	reset.Importance = widget.DangerImportance

	scroll := container.NewVScroll(container.NewVBox(
		form,
		widget.NewSeparator(),
		widget.NewLabel(s.ollamaStatus),
		widget.NewButton("Edit eval prompt…", func() {
			s.openPrompt("eval", scrSettings)
		}),
		reset,
	))
	scroll.SetMinSize(fyne.NewSize(720, 380))

	return scroll
}

// ─── prompts ────────────────────────────────────────────────────────────────

func (s *state) promptVarsHint(name string) string {
	db := s.currentDB()
	ptype := core.LoadPromptType(db, name)
	var named []string
	switch {
	case ptype == "pair":
		named = []string{"${A_NAME}", "${A_CONTENT}", "${B_NAME}", "${B_CONTENT}"}
	case name == "eval":
		named = []string{"${FILE_NAME}", "${CONTENT}", "${LINKED_NOTES}"}
	default:
		named = []string{"${FILE_NAME}", "${CONTENT}"}
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
	items := append(append([]string{}, names...), "+ Add new "+s.processType+" process")

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
		if int(id) < len(names) {
			s.processName = names[id]
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
		widget.NewButton("Export for "+s.processName, func() {
			s.startPhase(core.PhaseExportProcess, s.processName, "")
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
