.DEFAULT_GOAL := help

# Default values — override on command line, e.g. make phase1 SRC=~/notes MODEL=qwen2.5:7b
DB       ?= notes.db
SRC      ?= .
OUT      ?= out
MODEL    ?= gemma4:31b-cloud
HOST     ?= http://localhost:11434
MAXCHARS ?= 4000
WORKERS  ?= 1

BIN := obsid
MODEL_STAMP := .model-stamp

.PHONY: help build phase1 phase2 phase3 all status clean

help: ## Show this help
	@echo "obsidianize — конвертер md-заметок в Obsidian-vault"
	@echo ""
	@echo "  make build                         собрать бинарник"
	@echo "  make phase1    SRC=~/notes         импорт md-файлов в sqlite"
	@echo "  make phase2    MODEL=qwen2.5:7b    теги и связи через ollama"
	@echo "  make phase3    OUT=~/vault SRC=~/notes   запись vault с front matter и [[ссылками]]"
	@echo "  make all       SRC=~/notes OUT=~/vault   все три фазы подряд"
	@echo "  make status                        только счётчики"
	@echo "  make clean                         удалить бинарник и базу"
	@echo ""
	@echo "  Переменные:  DB=$(DB)  MODEL=$(MODEL)  HOST=$(HOST)  MAXCHARS=$(MAXCHARS)  WORKERS=$(WORKERS)"

build: $(BIN)

$(BIN): obsid.go go.mod go.sum $(MODEL_STAMP)
	go build -ldflags "-X main.defaultModel=$(MODEL)" -o $(BIN) .

$(MODEL_STAMP): FORCE
	@echo "$(MODEL)" | cmp -s $@ - 2>/dev/null || echo "$(MODEL)" > $@

FORCE:

phase1: $(BIN) ## Фаза 1: импорт md → sqlite
	./$(BIN) phase1 -src "$(SRC)" -db "$(DB)"

phase2: $(BIN) ## Фаза 2: ollama теги и связи
	./$(BIN) phase2 -db "$(DB)" -model "$(MODEL)" -host "$(HOST)" -maxchars "$(MAXCHARS)" -workers "$(WORKERS)"

phase3: $(BIN) ## Фаза 3: запись vault
	./$(BIN) phase3 -out "$(OUT)" -src "$(SRC)" -db "$(DB)"

all: $(BIN) ## Все три фазы
	./$(BIN) all -src "$(SRC)" -out "$(OUT)" -db "$(DB)" -model "$(MODEL)" -host "$(HOST)" -maxchars "$(MAXCHARS)" -workers "$(WORKERS)"

status: $(BIN) ## Статистика
	./$(BIN) status -db "$(DB)"

clean: ## Очистка
	rm -f $(BIN) $(DB) $(MODEL_STAMP)
