// Package cli implements the non-interactive command-line frontend.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"noesis/internal/core"
	"noesis/internal/ui/consoleShared"
)

const usageHeader = `noesis-cli — import notes, tag/link them with ollama and export an Obsidian vault.

Usage:
  noesis-cli [flags]

Flags:
`

// Run parses command-line flags and executes the selected phase(s).
func Run(args []string) error {
	def := core.DefaultConfig()
	fs := flag.NewFlagSet("noesis-cli", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usageHeader)
		fs.PrintDefaults()
	}

	src := fs.String("src", ".", "source folder, .zip archive or .json array file")
	out := fs.String("out", "out", "output Obsidian vault folder")
	dbPath := fs.String("db", "notes.db", "sqlite database file")
	host := fs.String("host", def.Host, "ollama host")
	model := fs.String("model", def.Model, "ollama model")
	transcribeModel := fs.String("transcribe-model", def.TranscribeModel, "ollama audio transcription model")
	maxChars := fs.Int("maxchars", def.MaxChars, "max characters per note sent to ollama")
	workers := fs.Int("workers", def.Workers, "concurrent ollama requests (1-100)")
	phaseName := fs.String("phase", "all", "phase to run: import, extract, pairs, single, export, all")
	source := fs.String("source", "", "import source: filesystem, zip or json (default filesystem)")
	format := fs.String("format", "", "comma-separated extensions to import, e.g. md,txt,csv,png (empty = default set)")
	promptName := fs.String("prompt", "", "prompt name for pairs/single phases (empty uses default)")
	templateName := fs.String("template", "", "export template name (empty uses default)")

	removePath := fs.String("remove", "", "remove note matching this path from the db")
	removeLinksPath := fs.String("remove-links", "", "remove note matching this path and scrub its obsidian links")
	comparePath := fs.String("compare", "", "evaluate novelty of a file without adding it to the db")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	if *maxChars < 1 {
		return errors.New("maxchars must be positive")
	}
	if *workers < 1 {
		*workers = 1
	}
	if *workers > 100 {
		*workers = 100
	}

	db, err := core.OpenDB(*dbPath)
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	// Flags that were not passed explicitly fall back to the settings saved by
	// the TUI/GUI in this database.
	saved := core.LoadAppSettings(db)
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if !seen["src"] && strings.TrimSpace(saved.Src) != "" {
		*src = saved.Src
	}
	if !seen["out"] && strings.TrimSpace(saved.Out) != "" {
		*out = saved.Out
	}
	if !seen["host"] && strings.TrimSpace(saved.Host) != "" {
		*host = saved.Host
	}
	if !seen["model"] && strings.TrimSpace(saved.Model) != "" {
		*model = saved.Model
	}
	if !seen["transcribe-model"] && strings.TrimSpace(saved.TranscribeModel) != "" {
		*transcribeModel = saved.TranscribeModel
	}
	if !seen["maxchars"] {
		*maxChars = saved.MaxChars
	}
	if !seen["workers"] {
		*workers = saved.Workers
	}

	cfg := core.Config{
		Host:            *host,
		Model:           *model,
		TranscribeModel: *transcribeModel,
		MaxChars:        *maxChars,
		Workers:         *workers,
		PromptName:      *promptName,
		TemplateName:    *templateName,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// data operations, when requested, take precedence over phases
	switch {
	case *removePath != "":
		n, err := core.RemoveNote(db, *removePath)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d note(s)\n", n)
		return nil

	case *removeLinksPath != "":
		removed, scrubbed, err := core.RemoveNoteAndLinks(db, *removeLinksPath)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d note(s), scrubbed links in %d other file(s)\n", removed, scrubbed)
		return nil

	case *comparePath != "":
		result, err := core.CompareFile(ctx, db, *comparePath, *host, *model, *transcribeModel, *maxChars)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	}

	progress := consoleshared.New(db)
	progress.Start()
	defer progress.Stop()

	// "all" (the default) runs import → analysis → export in sequence.
	if isAllPhase(*phaseName) {
		for _, p := range []core.Phase{core.PhaseImport, core.PhaseAnalyze, core.PhaseWrite} {
			if err := core.Run(ctx, progress, db, cfg, p, *src, *out); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
		}
		return nil
	}

	phase, err := parsePhase(*phaseName)
	if err != nil {
		return err
	}

	// custom import source/format paths bypass the default phase pipeline
	if phase == core.PhaseImport && (*source != "" || *format != "") {
		src2, err := core.ParseImportSource(*source)
		if err != nil {
			return err
		}
		opts := core.ImportOptions{
			Source:          src2,
			Formats:         parseFormats(*format),
			Host:            *host,
			TranscribeModel: *transcribeModel,
		}
		return core.Import(ctx, progress, db, *src, opts)
	}

	return core.Run(ctx, progress, db, cfg, phase, *src, *out)
}

// parseFormats turns a comma-separated extension list into a FormatSet.
func parseFormats(s string) core.FormatSet {
	if strings.TrimSpace(s) == "" {
		return nil // nil → import everything
	}
	fs := make(core.FormatSet)
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
		if e != "" {
			fs[e] = true
		}
	}
	return fs
}

func isAllPhase(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "all" || s == ""
}

func parsePhase(s string) (core.Phase, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "import":
		return core.PhaseImport, nil
	case "extract", "extraction", "transcribe":
		return core.PhaseExtract, nil
	case "2", "analyze", "tags", "pairs":
		return core.PhaseAnalyze, nil
	case "3", "export", "write":
		return core.PhaseWrite, nil
	case "single", "1x1", "tags-single", "one":
		return core.PhaseTagSingle, nil
	default:
		return 0, fmt.Errorf("unknown phase %q (use import, extract, pairs, single, export or all)", s)
	}
}
