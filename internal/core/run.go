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
	PhaseExtract
	PhaseRAG
)

// Run executes the selected phase.
// This is the single entry point every UI should use.
func Run(ctx context.Context, r Reporter, db *sql.DB, c Config, phase Phase, src, out string) error {
	switch phase {
	case PhaseImport:
		skipDirs := c.SkipDirs
		if skipDirs == nil {
			skipDirs = SavedSkipDirs(db)
		}
		return Import(ctx, r, db, src, ImportOptions{
			Source:        SourceFilesystem,
			Formats:       SavedImportFormats(db),
			BinaryStorage: c.BinaryStorage,
			SkipDirs:      skipDirs,
		})
	case PhaseAnalyze:
		return Phase2(ctx, r, db, c)
	case PhaseWrite:
		return Phase3(ctx, r, db, out, src, c.TemplateName)
	case PhaseTagSingle:
		return TagAllSingle(ctx, r, db, c)
	case PhaseExtract:
		return ExtractAll(ctx, r, db, c, ExtractFilter{
			Name:    c.ExtractName,
			Formats: c.ExtractFormats,
		}, true)
	case PhaseRAG:
		return BuildRAGIndex(ctx, r, db, c)
	}
	return nil
}
