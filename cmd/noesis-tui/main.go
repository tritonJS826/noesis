package main

import (
	"fmt"
	"os"

	"noesis/internal/core"
	"noesis/internal/ui/tui"
)

func main() {
	db, err := core.OpenDB("notes.db")
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqlite:", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := tui.Run(db); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
