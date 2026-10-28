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
	PhaseExtract
)

// Run executes the selected phase.
// This is the single entry point every UI should use.
func Run(ctx context.Context, r Reporter, db *sql.DB, c Config, phase Phase, src, out string) error {
	switch phase {
	case PhaseImport:
		return Import(ctx, r, db, src, ImportOptions{
			Source:          SourceFilesystem,
			Formats:         defaultFormatSet(),
			Host:            c.Host,
			TranscribeModel: c.TranscribeModel,
		})
	case PhaseAnalyze:
		return Phase2(ctx, r, db, c)
	case PhaseWrite:
		return Phase3(ctx, r, db, out, src, c.TemplateName)
	case PhaseTagSingle:
		return TagAllSingle(ctx, r, db, c)
	case PhaseExportProcess:
		return ExportForProcess(ctx, r, db, out, c.PromptName, src, c.TemplateName)
	case PhaseExtract:
		return ExtractAll(ctx, r, db, c, true)
	}
	return nil
}
