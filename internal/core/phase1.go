package core

import (
	"context"
	"database/sql"
)

// Phase1 imports files from src into sqlite using the default text formats and
// the filesystem source. It is kept for CLI/GUI backward compatibility; the
// TUI uses Import directly to offer source and format selection.
func Phase1(ctx context.Context, r Reporter, db *sql.DB, src string) error {
	return Import(ctx, r, db, src, ImportOptions{
		Source:  SourceFilesystem,
		Formats: defaultFormatSet(),
	})
}

// defaultFormatSet converts the default extension list into a FormatSet.
func defaultFormatSet() FormatSet {
	fs := make(FormatSet)
	for _, e := range DefaultImportFormats() {
		fs[e] = true
	}
	return fs
}