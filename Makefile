.DEFAULT_GOAL := help

# Default values — override on command line
MODEL    ?= gemma4:31b-cloud
HOST     ?= http://localhost:11434

BIN := obsid
MODEL_STAMP := .model-stamp

.PHONY: help build run clean

help: ## Show this help
	@echo "obsidianize — md-notes to Obsidian vault converter (TUI)"
	@echo ""
	@echo "  make build                          build the binary"
	@echo "  make run                            launch the TUI"
	@echo "  make clean                          remove binary and database"
	@echo ""
	@echo "  Variables:  MODEL=$(MODEL)  HOST=$(HOST)"

build: $(BIN)

$(BIN): obsid.go go.mod go.sum $(MODEL_STAMP)
	go build -ldflags "-X main.defaultModel=$(MODEL)" -o $(BIN) .

$(MODEL_STAMP): FORCE
	@echo "$(MODEL)" | cmp -s $@ - 2>/dev/null || echo "$(MODEL)" > $@

FORCE:

run: $(BIN) ## Launch the TUI
	./$(BIN)

clean: ## Cleanup
	rm -f $(BIN) notes.db notes.db-wal notes.db-shm $(MODEL_STAMP)