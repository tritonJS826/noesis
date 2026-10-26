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
	PhaseAll
)

// Run executes the selected phase (or the whole pipeline for PhaseAll).
// This is the single entry point every UI should use.
func Run(ctx context.Context, r Reporter, db *sql.DB, c Config, phase Phase, src, out string) error {
	switch phase {
	case PhaseImport:
		return Phase1(ctx, r, db, src)
	case PhaseAnalyze:
		return Phase2(ctx, r, db, c)
	case PhaseWrite:
		return Phase3(ctx, r, db, out, src)
	case PhaseAll:
		if err := Phase1(ctx, r, db, src); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := Phase2(ctx, r, db, c); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		return Phase3(ctx, r, db, out, src)
	}
	return nil
}
