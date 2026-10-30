// Package cli implements the non-interactive command-line frontend.
package cli

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"noesis/internal/core"
	"noesis/internal/ui/consoleShared"
)

const usageHeader = `noesis-cli — import notes, tag/link them with ollama and export an Obsidian vault.

Usage:
  noesis-cli [flags]

Flags:
`

// Run parses command-line flags and executes the selected phase(s).
func Run(args []string) error {
	def := core.DefaultConfig()
	fs := flag.NewFlagSet("noesis-cli", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usageHeader)
		fs.PrintDefaults()
	}

	src := fs.String("src", ".", "source folder, .zip archive or .json array file")
	out := fs.String("out", "out", "output Obsidian vault folder")
	dbPath := fs.String("db", "notes.db", "sqlite database file")
	host := fs.String("host", def.Host, "ollama host")
	model := fs.String("model", def.Model, "ollama model")
	visionModel := fs.String("vision-model", def.VisionModel, "ollama vision model for images (empty uses -model)")
	embeddingModel := fs.String("embedding-model", def.EmbeddingModel, "ollama embedding model for the RAG index")
	transcribeModel := fs.String("transcribe-model", def.TranscribeModel, "legacy ollama audio transcription model")
	audioEngine := fs.String("audio", def.AudioEngine, "audio backend: whisper, google or ollama (legacy)")
	whisperModel := fs.String("whisper-model", def.WhisperModel, "whisper.cpp model; download it first in the TUI/GUI settings")
	whisperBackend := fs.String("whisper-backend", def.WhisperBackend, "whisper GPU backend: auto, cpu or vulkan")
	whisperDevice := fs.Int("whisper-device", def.WhisperDevice, "Vulkan device index (0 = default adapter)")
	googleKey := fs.String("google-key", def.GoogleAPIKey, "Google Speech-to-Text API key (requires ffmpeg)")
	speechLanguage := fs.String("speech-language", def.SpeechLanguage, "language code for Google transcription, e.g. ru-RU")
	binaryStorage := fs.String("binary-storage", def.BinaryStorage, "binary storage: links (default, smaller db) or db (base64)")
	maxChars := fs.Int("maxchars", def.MaxChars, "max characters per note sent to ollama")
	workers := fs.Int("workers", def.Workers, "concurrent ollama requests (1-100)")
	phaseName := fs.String("phase", "all", "phase to run: import, extract, pairs, single, rag, export, all")
	source := fs.String("source", "", "import source: filesystem, zip or json (default filesystem)")
	format := fs.String("format", "", "comma-separated extensions to import, e.g. md,txt,csv,png (empty = default set)")
	extractName := fs.String("extract-name", "", "extract phase: only re-extract notes whose path/name contains this text")
	extractFormat := fs.String("extract-format", "", "extract phase: comma-separated extensions to re-extract, e.g. pdf,png,mp3 (empty = all)")
	promptName := fs.String("prompt", "", "prompt name for pairs/single phases (empty uses default)")
	templateName := fs.String("template", "", "export template name (empty uses default)")

	removePath := fs.String("remove", "", "remove note matching this path from the db")
	removeLinksPath := fs.String("remove-links", "", "remove note matching this path and scrub its obsidian links")
	comparePath := fs.String("compare", "", "evaluate novelty of a file without adding it to the db")
	statsOnly := fs.Bool("stats", false, "print per-format import/extraction counts and exit")
	ragIndex := fs.Bool("rag-index", false, "build/refresh the semantic (RAG) vector index and exit")
	ragSearch := fs.String("rag-search", "", "semantic search query over the RAG index and exit")
	ragAsk := fs.Bool("rag-ask", false, "with -rag-search: also answer the question with the chat model")
	ragTopK := fs.Int("rag-topk", 5, "number of RAG chunks to retrieve")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	if *maxChars < 1 {
		return errors.New("maxchars must be positive")
	}
	if *workers < 1 {
		*workers = 1
	}
	if *workers > 100 {
		*workers = 100
	}
	if *whisperDevice < 0 {
		*whisperDevice = 0
	}

	db, err := core.OpenDB(*dbPath)
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	// Flags that were not passed explicitly fall back to the settings saved by
	// the TUI/GUI in this database.
	saved := core.LoadAppSettings(db)
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if !seen["src"] && strings.TrimSpace(saved.Src) != "" {
		*src = saved.Src
	}
	if !seen["out"] && strings.TrimSpace(saved.Out) != "" {
		*out = saved.Out
	}
	if !seen["host"] && strings.TrimSpace(saved.Host) != "" {
		*host = saved.Host
	}
	if !seen["model"] && strings.TrimSpace(saved.Model) != "" {
		*model = saved.Model
	}
	if !seen["vision-model"] && strings.TrimSpace(saved.VisionModel) != "" {
		*visionModel = saved.VisionModel
	}
	if !seen["embedding-model"] && strings.TrimSpace(saved.EmbeddingModel) != "" {
		*embeddingModel = saved.EmbeddingModel
	}
	if !seen["transcribe-model"] && strings.TrimSpace(saved.TranscribeModel) != "" {
		*transcribeModel = saved.TranscribeModel
	}
	if !seen["audio"] {
		*audioEngine = saved.AudioEngine
	}
	if !seen["whisper-model"] {
		*whisperModel = saved.WhisperModel
	}
	if !seen["whisper-backend"] {
		*whisperBackend = saved.WhisperBackend
	}
	if !seen["whisper-device"] {
		*whisperDevice = saved.WhisperDevice
	}
	if !seen["google-key"] {
		*googleKey = saved.GoogleAPIKey
	}
	if !seen["speech-language"] {
		*speechLanguage = saved.SpeechLanguage
	}
	if !seen["binary-storage"] {
		*binaryStorage = saved.BinaryStorage
	}
	if !seen["maxchars"] {
		*maxChars = saved.MaxChars
	}
	if !seen["workers"] {
		*workers = saved.Workers
	}

	cfg := core.Config{
		Host:            *host,
		Model:           *model,
		VisionModel:     *visionModel,
		EmbeddingModel:  core.NormalizeEmbeddingModel(*embeddingModel),
		TranscribeModel: *transcribeModel,
		AudioEngine:     core.NormalizeAudioEngine(*audioEngine),
		WhisperModel:    core.NormalizeWhisperModel(*whisperModel),
		WhisperBackend:  core.NormalizeWhisperBackend(*whisperBackend),
		WhisperDevice:   *whisperDevice,
		GoogleAPIKey:    *googleKey,
		SpeechLanguage:  core.NormalizeSpeechLanguage(*speechLanguage),
		BinaryStorage:   core.NormalizeBinaryStorage(*binaryStorage),
		MaxChars:        *maxChars,
		Workers:         *workers,
		PromptName:      *promptName,
		TemplateName:    *templateName,
		ExtractName:     *extractName,
		ExtractFormats:  core.ExtractFormatSet(strings.Split(*extractFormat, ",")),
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// data operations, when requested, take precedence over phases
	switch {
	case *statsOnly:
		printFormatStats(db)
		return nil

	case *removePath != "":
		n, err := core.RemoveNote(db, *removePath)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d note(s)\n", n)
		return nil

	case *removeLinksPath != "":
		removed, scrubbed, err := core.RemoveNoteAndLinks(db, *removeLinksPath)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d note(s), scrubbed links in %d other file(s)\n", removed, scrubbed)
		return nil

	case *comparePath != "":
		audio := core.AudioOptions{
			Engine:         cfg.AudioEngine,
			WhisperModel:   cfg.WhisperModel,
			WhisperBackend: cfg.WhisperBackend,
			WhisperDevice:  cfg.WhisperDevice,
			GoogleAPIKey:   cfg.GoogleAPIKey,
			SpeechLanguage: cfg.SpeechLanguage,
			OllamaHost:     cfg.Host,
			OllamaModel:    cfg.TranscribeModel,
		}
		result, err := core.CompareFileWithAudio(ctx, db, *comparePath, *host, *model, audio, *maxChars)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil

	case *ragSearch != "":
		return runRAGSearch(ctx, db, cfg, *ragSearch, *ragTopK, *ragAsk)
	}

	progress := consoleshared.New(db)
	progress.Start()
	defer progress.Stop()

	if *ragIndex {
		return core.BuildRAGIndex(ctx, progress, db, cfg)
	}

	// "all" (the default) runs import → analysis → export in sequence.
	if isAllPhase(*phaseName) {
		for _, p := range []core.Phase{core.PhaseImport, core.PhaseAnalyze, core.PhaseWrite} {
			if err := core.Run(ctx, progress, db, cfg, p, *src, *out); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
		}
		return nil
	}

	phase, err := parsePhase(*phaseName)
	if err != nil {
		return err
	}

	// custom import source/format paths bypass the default phase pipeline
	if phase == core.PhaseImport && (*source != "" || *format != "") {
		src2, err := core.ParseImportSource(*source)
		if err != nil {
			return err
		}
		formats := core.SavedImportFormats(db)
		if strings.TrimSpace(*format) != "" {
			formats = parseFormats(*format)
		}
		opts := core.ImportOptions{
			Source:        src2,
			Formats:       formats,
			BinaryStorage: cfg.BinaryStorage,
		}
		return core.Import(ctx, progress, db, *src, opts)
	}

	return core.Run(ctx, progress, db, cfg, phase, *src, *out)
}

// runRAGSearch performs a semantic search over the RAG index, optionally
// asking the chat model to answer the question from the retrieved context.
func runRAGSearch(ctx context.Context, db *sql.DB, cfg core.Config, query string, topK int, ask bool) error {
	if ask {
		answer, results, err := core.RAGAnswer(ctx, db, cfg, query, topK)
		if err != nil {
			return err
		}
		fmt.Println(answer)
		fmt.Println()
		printRAGResults(results)
		return nil
	}
	results, err := core.RAGSearch(ctx, db, cfg, query, topK)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("no matches (is the RAG index built? run: noesis-cli -rag-index)")
		return nil
	}
	printRAGResults(results)
	return nil
}

// printRAGResults prints ranked chunks with their similarity score.
func printRAGResults(results []core.RAGResult) {
	for i, r := range results {
		snippet := strings.Join(strings.Fields(r.Chunk), " ")
		if len([]rune(snippet)) > 240 {
			snippet = string([]rune(snippet)[:240]) + "…"
		}
		fmt.Printf("[%d] %.3f  %s\n    %s\n", i+1, r.Score, r.Path, snippet)
	}
}

// parseFormats turns a comma-separated extension list into a FormatSet.
func parseFormats(s string) core.FormatSet {
	if strings.TrimSpace(s) == "" {
		return nil // nil → import everything
	}
	fs := make(core.FormatSet)
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
		if e != "" {
			fs[e] = true
		}
	}
	return fs
}

func isAllPhase(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "all" || s == ""
}

func parsePhase(s string) (core.Phase, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "import":
		return core.PhaseImport, nil
	case "extract", "extraction", "transcribe":
		return core.PhaseExtract, nil
	case "2", "analyze", "tags", "pairs":
		return core.PhaseAnalyze, nil
	case "3", "export", "write":
		return core.PhaseWrite, nil
	case "single", "1x1", "tags-single", "one":
		return core.PhaseTagSingle, nil
	case "rag", "index", "embed", "embeddings":
		return core.PhaseRAG, nil
	default:
		return 0, fmt.Errorf("unknown phase %q (use import, extract, pairs, single, rag, export or all)", s)
	}
}

// printFormatStats prints per-extension import/extraction counters.
func printFormatStats(db *sql.DB) {
	stats := core.LoadFormatStats(db)
	if len(stats) == 0 {
		fmt.Println("no files imported yet")
		return
	}
	total, text, textWithText, binary, extracted, failed := 0, 0, 0, 0, 0, 0
	for _, st := range stats {
		fmt.Printf("%-22s %s\n", st.Name(), st.Label())
		total += st.Total
		if st.Kind == "text" {
			text += st.Total
			textWithText += st.Extracted
		} else {
			binary += st.Total
			extracted += st.Extracted
		}
		failed += st.Failed
	}
	fmt.Printf("total: %d notes over %d format(s)\n", total, len(stats))
	if text > 0 {
		fmt.Printf("text: %d imported · %d with text (no extraction needed)\n", text, textWithText)
	}
	if binary > 0 {
		fmt.Printf("binary: %d/%d extracted", extracted, binary)
		if failed > 0 {
			fmt.Printf(" · %d failed", failed)
		}
		fmt.Println()
	}
}
