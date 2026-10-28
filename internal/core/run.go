package core

import (
	"context"
	"database/sql"
)

// Phase identifies which part of the pipeline to run.
type Phase int

const (
	PhaseImport Phase = iota
	PhaseAnalyze
	PhaseWrite
	PhaseTagSingle
	PhaseExportProcess
)

// Run executes the selected phase.
// This is the single entry point every UI should use.
func Run(ctx context.Context, r Reporter, db *sql.DB, c Config, phase Phase, src, out string) error {
	switch phase {
	case PhaseImport:
		return Phase1(ctx, r, db, src)
	case PhaseAnalyze:
		return Phase2(ctx, r, db, c)
	case PhaseWrite:
		return Phase3(ctx, r, db, out, src, c.TemplateName)
	case PhaseTagSingle:
		return TagAllSingle(ctx, r, db, c)
	case PhaseExportProcess:
		return ExportForProcess(ctx, r, db, out, c.PromptName, src, c.TemplateName)
	}
	return nil
}
