package main

import (
	"flag"
	"fmt"
	"os"

	"noesis/internal/ui/gui"
)

func main() {
	dbPath := flag.String("db", "notes.db", "sqlite database file")
	flag.Parse()

	if err := gui.Run(*dbPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
