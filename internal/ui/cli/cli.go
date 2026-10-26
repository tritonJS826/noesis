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

const usageHeader = `noesis-cli — import md notes, tag/link them with ollama and write an Obsidian vault.

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

	src := fs.String("src", ".", "source folder with markdown notes")
	out := fs.String("out", "out", "output Obsidian vault folder")
	dbPath := fs.String("db", "notes.db", "sqlite database file")
	host := fs.String("host", def.Host, "ollama host")
	model := fs.String("model", def.Model, "ollama model")
	maxChars := fs.Int("maxchars", def.MaxChars, "max characters per note sent to ollama")
	workers := fs.Int("workers", def.Workers, "concurrent ollama requests (1-100)")
	phaseName := fs.String("phase", "all", "phase to run: 1/import, 2/analyze, 3/write or all")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	phase, err := parsePhase(*phaseName)
	if err != nil {
		return err
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
	progress := consoleshared.New(db)
	progress.Start()
	defer progress.Stop()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return core.Run(ctx, progress, db, cfg, phase, *src, *out)
}

func parsePhase(s string) (core.Phase, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "import":
		return core.PhaseImport, nil
	case "2", "analyze", "tags":
		return core.PhaseAnalyze, nil
	case "3", "write":
		return core.PhaseWrite, nil
	case "all", "":
		return core.PhaseAll, nil
	default:
		return 0, fmt.Errorf("unknown phase %q (use 1, 2, 3 or all)", s)
	}
}
