.DEFAULT_GOAL := help

# Default values — override on command line
MODEL    ?= gemma4:31b-cloud
HOST     ?= http://localhost:11434
ARGS     ?=

BIN_TUI  := noesis-tui
BIN_CLI  := noesis-cli
BIN_GUI  := noesis-gui
MODEL_STAMP := .model-stamp
GO_SOURCES := $(shell find . -name '*.go' -not -path './.git/*')
LDFLAGS  := -ldflags "-X noesis/internal/core.DefaultModel=$(MODEL)"

.PHONY: help build build-tui build-cli build-gui run run-cli run-gui dist clean

help: ## Show this help
	@echo "noesis — md-notes to Obsidian vault converter"
	@echo ""
	@echo "  make build                          build all binaries"
	@echo "  make build-tui                      build noesis-tui"
	@echo "  make build-cli                      build noesis-cli"
	@echo "  make build-gui                      build noesis-gui (Fyne)"
	@echo "  make run                            launch the TUI"
	@echo "  make run-cli ARGS='-phase 1'        run the CLI"
	@echo "  make run-gui                        launch the GUI"
	@echo "  make dist                           build release binaries for this OS"
	@echo "  make clean                          remove binaries, dist and database"
	@echo ""
	@echo "  Variables:  MODEL=$(MODEL)  HOST=$(HOST)  ARGS=$(ARGS)"
	@echo "  Distribution builds:  ./scripts/build-linux.sh"
	@echo "                        ./scripts/build-macos.sh"
	@echo "                        ./scripts/build-windows.sh (or .ps1)"

build: build-tui build-cli build-gui

build-tui: $(BIN_TUI)

build-cli: $(BIN_CLI)

build-gui: $(BIN_GUI)

$(BIN_TUI): $(GO_SOURCES) go.mod go.sum $(MODEL_STAMP)
	go build $(LDFLAGS) -o $(BIN_TUI) ./cmd/noesis-tui

$(BIN_CLI): $(GO_SOURCES) go.mod go.sum $(MODEL_STAMP)
	go build $(LDFLAGS) -o $(BIN_CLI) ./cmd/noesis-cli

$(BIN_GUI): $(GO_SOURCES) go.mod go.sum $(MODEL_STAMP)
	go build $(LDFLAGS) -o $(BIN_GUI) ./cmd/noesis-gui

$(MODEL_STAMP): FORCE
	@echo "$(MODEL)" | cmp -s $@ - 2>/dev/null || echo "$(MODEL)" > $@

FORCE:

run: $(BIN_TUI) ## Launch the TUI
	./$(BIN_TUI)

run-cli: $(BIN_CLI) ## Run the CLI
	./$(BIN_CLI) $(ARGS)

run-gui: $(BIN_GUI) ## Launch the GUI
	./$(BIN_GUI)

dist: ## Build release binaries for this OS
	./scripts/build.sh

clean: ## Cleanup
	rm -f $(BIN_TUI) $(BIN_CLI) $(BIN_GUI) notes.db notes.db-wal notes.db-shm $(MODEL_STAMP)
	rm -rf dist
