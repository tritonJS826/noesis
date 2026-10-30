package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChunkText(t *testing.T) {
	if got := chunkText("   ", 100, 10); got != nil {
		t.Fatalf("blank text should yield no chunks, got %v", got)
	}
	short := "hello world"
	if got := chunkText(short, 100, 10); len(got) != 1 || got[0] != short {
		t.Fatalf("short text should be one chunk, got %v", got)
	}

	// A long text must split into multiple chunks, each within the size limit.
	long := strings.Repeat("sentence number one. ", 200)
	chunks := chunkText(long, 300, 50)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if n := len([]rune(c)); n > 300 {
			t.Fatalf("chunk exceeds size: %d runes", n)
		}
	}
}

func TestEmbeddingModelSuggestion(t *testing.T) {
	if got := EmbeddingModelSuggestion(nil, "mxbai-embed-large"); got != "" {
		t.Fatalf("unknown model list should not warn, got %q", got)
	}
	installed := []string{"llama3:latest", "mxbai-embed-large:latest"}
	if got := EmbeddingModelSuggestion(installed, "mxbai-embed-large"); got != "" {
		t.Fatalf("installed model should not warn, got %q", got)
	}
	if got := EmbeddingModelSuggestion(installed, "mxbai-embed-large:latest"); got != "" {
		t.Fatalf("installed model with tag should not warn, got %q", got)
	}
	got := EmbeddingModelSuggestion(installed, "nomic-embed-text")
	if !strings.Contains(got, "ollama pull nomic-embed-text") {
		t.Fatalf("unexpected suggestion: %q", got)
	}
}

func TestCosineAndVectorRoundTrip(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	if got := cosineSimilarity(a, b); math.Abs(got) > 1e-9 {
		t.Fatalf("orthogonal vectors should score 0, got %v", got)
	}
	if got := cosineSimilarity(a, a); math.Abs(got-1) > 1e-9 {
		t.Fatalf("identical vectors should score 1, got %v", got)
	}

	orig := []float32{1.5, -2.25, 0, 42}
	got := decodeVec(encodeVec(orig))
	if len(got) != len(orig) {
		t.Fatalf("length mismatch: %d vs %d", len(got), len(orig))
	}
	for i := range orig {
		if got[i] != orig[i] {
			t.Fatalf("value %d: got %v want %v", i, got[i], orig[i])
		}
	}
}

// fakeEmbed maps text to a tiny bag-of-words vector so cosine ranking is
// meaningful in tests: dimensions are cat / dog / go / bias.
func fakeEmbed(text string) []float32 {
	t := strings.ToLower(text)
	return []float32{
		float32(strings.Count(t, "cat")),
		float32(strings.Count(t, "dog")),
		float32(strings.Count(t, "go")),
		1,
	}
}

func newFakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{{"name": "mxbai-embed-large"}},
		})
	})
	mux.HandleFunc("/api/embed", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{fakeEmbed(in.Input)}})
	})
	mux.HandleFunc("/api/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": fakeEmbed(in.Prompt)})
	})
	mux.HandleFunc("/api/generate", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "grounded answer"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func insertMarkdownNote(t *testing.T, db *sql.DB, path, name, markdown string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, markdown, imported_at)
		VALUES(?,?,?,'','text/markdown',?,'2024-01-02')`, path, "/x/"+path, name, markdown)
	if err != nil {
		t.Fatalf("insert note: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestBuildRAGIndexAndSearch(t *testing.T) {
	db := openTestDB(t)
	srv := newFakeOllama(t)

	insertMarkdownNote(t, db, "cat.md", "cat.md", "I have a cat. The cat is fluffy and sleeps a lot.")
	insertMarkdownNote(t, db, "dog.md", "dog.md", "Dogs are loyal. A dog barks at strangers.")

	cfg := DefaultConfig()
	cfg.Host = srv.URL
	cfg.EmbeddingModel = "mxbai-embed-large"
	cfg.Workers = 2

	if err := BuildRAGIndex(context.Background(), NoopReporter{}, db, cfg); err != nil {
		t.Fatalf("build index: %v", err)
	}

	notes, chunks, models := RAGStats(db)
	if notes != 2 || chunks < 2 {
		t.Fatalf("stats: notes=%d chunks=%d models=%v", notes, chunks, models)
	}
	if len(models) != 1 || models[0] != "mxbai-embed-large" {
		t.Fatalf("unexpected models: %v", models)
	}

	results, err := RAGSearch(context.Background(), db, cfg, "tell me about my cat", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no search results")
	}
	if results[0].Name != "cat.md" {
		t.Fatalf("top result = %q, want cat.md", results[0].Name)
	}
	if results[0].Score <= results[len(results)-1].Score {
		t.Fatalf("results not sorted by score: %v", results)
	}

	answer, used, err := RAGAnswer(context.Background(), db, cfg, "what about cats?", 5)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if answer != "grounded answer" {
		t.Fatalf("answer = %q", answer)
	}
	if len(used) == 0 {
		t.Fatal("expected the answer to cite retrieved chunks")
	}
}

func TestBuildRAGIndexSkipsUnchanged(t *testing.T) {
	db := openTestDB(t)
	srv := newFakeOllama(t)
	insertMarkdownNote(t, db, "a.md", "a.md", "cat cat cat")

	cfg := DefaultConfig()
	cfg.Host = srv.URL
	cfg.Workers = 1

	if err := BuildRAGIndex(context.Background(), NoopReporter{}, db, cfg); err != nil {
		t.Fatalf("first index: %v", err)
	}
	_, chunks1, _ := RAGStats(db)

	// Re-running must not duplicate chunks because the text is unchanged.
	if err := BuildRAGIndex(context.Background(), NoopReporter{}, db, cfg); err != nil {
		t.Fatalf("second index: %v", err)
	}
	_, chunks2, _ := RAGStats(db)
	if chunks1 != chunks2 {
		t.Fatalf("chunk count changed on re-index: %d -> %d", chunks1, chunks2)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rag_chunks`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != chunks1 {
		t.Fatalf("rag_chunks rows = %d, want %d", rows, chunks1)
	}
}

func TestEmbedTextFallsBackToLegacyEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/embed", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/api/embeddings", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 2, 3}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Host = srv.URL
	vec, err := EmbedText(context.Background(), cfg, "hello")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(vec) != 3 || vec[0] != 1 || vec[2] != 3 {
		t.Fatalf("unexpected vector: %v", vec)
	}
}

func TestDeleteNoteRemovesRAGRows(t *testing.T) {
	db := openTestDB(t)
	srv := newFakeOllama(t)
	id := insertMarkdownNote(t, db, "x.md", "x.md", "cat")

	cfg := DefaultConfig()
	cfg.Host = srv.URL
	cfg.Workers = 1
	if err := BuildRAGIndex(context.Background(), NoopReporter{}, db, cfg); err != nil {
		t.Fatalf("index: %v", err)
	}

	if _, err := RemoveNoteByID(db, id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	var chunks, metas int
	_ = db.QueryRow(`SELECT COUNT(*) FROM rag_chunks WHERE note_id=?`, id).Scan(&chunks)
	_ = db.QueryRow(`SELECT COUNT(*) FROM rag_notes WHERE note_id=?`, id).Scan(&metas)
	if chunks != 0 || metas != 0 {
		t.Fatalf("rag rows survived note deletion: chunks=%d metas=%d", chunks, metas)
	}
}
