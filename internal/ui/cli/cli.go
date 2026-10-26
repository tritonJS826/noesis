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

const usageHeader = `noesis-cli — import notes, tag/link them with ollama and write an Obsidian vault.

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
	maxChars := fs.Int("maxchars", def.MaxChars, "max characters per note sent to ollama")
	workers := fs.Int("workers", def.Workers, "concurrent ollama requests (1-100)")
	phaseName := fs.String("phase", "all", "phase to run: import, pairs, single, write, all")
	source := fs.String("source", "", "import source: filesystem, zip or json (default filesystem)")
	format := fs.String("format", "", "comma-separated extensions to import, e.g. md,txt,csv,png (empty = default set)")

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

	cfg := core.Config{Host: *host, Model: *model, MaxChars: *maxChars, Workers: *workers}
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
		result, err := core.CompareFile(ctx, db, *comparePath, *host, *model, *maxChars)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	}

	phase, err := parsePhase(*phaseName)
	if err != nil {
		return err
	}

	progress := consoleshared.New(db)
	progress.Start()
	defer progress.Stop()

	// custom import source/format paths bypass the default phase pipeline
	if phase == core.PhaseImport && (*source != "" || *format != "") {
		src2, err := core.ParseImportSource(*source)
		if err != nil {
			return err
		}
		opts := core.ImportOptions{Source: src2, Formats: parseFormats(*format)}
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

func parsePhase(s string) (core.Phase, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "import":
		return core.PhaseImport, nil
	case "2", "analyze", "tags", "pairs":
		return core.PhaseAnalyze, nil
	case "3", "write":
		return core.PhaseWrite, nil
	case "single", "1x1", "tags-single", "one":
		return core.PhaseTagSingle, nil
	case "all", "":
		return core.PhaseAll, nil
	default:
		return 0, fmt.Errorf("unknown phase %q (use import, pairs, single, write or all)", s)
	}
}