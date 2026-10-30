package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// RAG (retrieval-augmented generation) turns every note into one or more
// embedded chunks stored in sqlite. Search embeds the query and ranks chunks by
// cosine similarity; "ask" additionally feeds the best chunks to the chat model
// so it can answer from the notes.

// Chunking parameters (in runes). Chunks overlap a little so a passage split
// across a boundary is still fully contained in at least one chunk. The size is
// kept well below the ~512-token context of embedding models: Cyrillic and CJK
// text tokenises at roughly one token per rune, so a smaller window avoids the
// silent truncation that a 1000-rune chunk caused.
const (
	ragChunkSize    = 700
	ragChunkOverlap = 120
	ragDefaultTopK  = 20
	ragMinTopK      = 1
	ragMaxTopK      = 50
)

// DefaultRAGTopK is the number of chunks retrieved when the user has not picked
// a different count.
func DefaultRAGTopK() int { return ragDefaultTopK }

// NormalizeRAGTopK clamps a requested result count into the supported range.
func NormalizeRAGTopK(n int) int {
	if n < ragMinTopK {
		return ragMinTopK
	}
	if n > ragMaxTopK {
		return ragMaxTopK
	}
	return n
}

// RAGResult is one ranked search hit.
type RAGResult struct {
	NoteID int64
	Name   string
	Path   string
	Chunk  string
	Score  float64
}

// NormalizeEmbeddingModel is defined in config.go.

// modelBaseName strips an ollama ":tag" suffix (e.g. "nomic-embed-text:latest"
// → "nomic-embed-text").
func modelBaseName(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[:i]
	}
	return name
}

// EmbeddingModelSuggestion returns a short suggestion when the configured
// embedding model is not among the installed ollama models, or "" when it is
// present (or when the installed list is unknown/empty, so callers never show a
// false alarm). Matching ignores the ":tag" suffix.
func EmbeddingModelSuggestion(installed []string, model string) string {
	if len(installed) == 0 {
		return ""
	}
	model = NormalizeEmbeddingModel(model)
	if ModelInstalled(installed, model) {
		return ""
	}
	return "not installed — run: ollama pull " + model
}

// embeddingPrompts returns the task prefixes the configured embedding model
// expects for retrieval. Some models are trained with an instruction prompt
// (mxbai-embed-large requires it on the query; nomic-embed-text needs one on
// both the query and the document). Omitting them noticeably degrades ranking.
// Empty strings mean "no prefix".
func embeddingPrompts(model string) (queryPrompt, docPrompt string) {
	switch modelBaseName(strings.ToLower(strings.TrimSpace(model))) {
	case "mxbai-embed-large":
		return "Represent this sentence for searching relevant passages: ", ""
	case "nomic-embed-text":
		return "search_query: ", "search_document: "
	}
	return "", ""
}

// EmbedText returns the embedding vector of text using the configured
// embedding model on the ollama host. It prefers the current /api/embed
// endpoint and falls back to the legacy /api/embeddings endpoint.
func EmbedText(ctx context.Context, c Config, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("cannot embed empty text")
	}
	model := NormalizeEmbeddingModel(c.EmbeddingModel)
	host := strings.TrimRight(c.Host, "/")

	var lastErr error
	for _, ep := range []struct {
		path string
		body map[string]any
	}{
		{"/api/embed", map[string]any{"model": model, "input": text}},
		{"/api/embeddings", map[string]any{"model": model, "prompt": text}},
	} {
		vec, err := postEmbedding(ctx, host+ep.path, ep.body)
		if err == nil && len(vec) > 0 {
			return vec, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("empty embedding")
	}
	return nil, fmt.Errorf("embedding model %q: %w (is it pulled? run: ollama pull %s)", model, lastErr, model)
}

func postEmbedding(ctx context.Context, url string, body map[string]any) ([]float32, error) {
	payload, _ := json.Marshal(body)
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Embedding  []float32   `json:"embedding"`
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("failed to read embedding response: %w", err)
	}
	if len(out.Embeddings) > 0 {
		return out.Embeddings[0], nil
	}
	if len(out.Embedding) > 0 {
		return out.Embedding, nil
	}
	return nil, fmt.Errorf("no embedding in response")
}

// EmbedQuery embeds a search query, adding the model's retrieval prompt when it
// has one. This is what makes "search" rank well with mxbai-embed-large and
// nomic-embed-text.
func EmbedQuery(ctx context.Context, c Config, query string) ([]float32, error) {
	qp, _ := embeddingPrompts(NormalizeEmbeddingModel(c.EmbeddingModel))
	return EmbedText(ctx, c, qp+strings.TrimSpace(query))
}

// EmbedDocument embeds one note chunk. title is prepended so a chunk keeps its
// source context (which helps titles and topics match), and the model's
// document prompt is applied when it has one.
func EmbedDocument(ctx context.Context, c Config, title, text string) ([]float32, error) {
	_, dp := embeddingPrompts(NormalizeEmbeddingModel(c.EmbeddingModel))
	body := strings.TrimSpace(text)
	if t := strings.TrimSpace(title); t != "" {
		body = t + "\n" + body
	}
	return EmbedText(ctx, c, dp+body)
}

// encodeVec serialises a float32 vector as little-endian bytes for sqlite.
func encodeVec(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVec parses a vector written by encodeVec.
func decodeVec(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// cosineSimilarity returns the cosine of the angle between a and b.
func cosineSimilarity(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// chunkText splits s into overlapping pieces of at most size runes, preferring
// to cut on a newline and otherwise on a sentence boundary. Short text yields a
// single chunk.
func chunkText(s string, size, overlap int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if size <= 0 {
		size = ragChunkSize
	}
	runes := []rune(s)
	if len(runes) <= size {
		return []string{s}
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 5
	}

	var out []string
	start := 0
	for start < len(runes) {
		end := start + size
		if end >= len(runes) {
			end = len(runes)
		} else {
			// Look for a natural break in the last third of the window.
			lower := start + size*2/3
			cut := -1
			for i := end - 1; i > lower; i-- {
				if runes[i] == '\n' {
					cut = i
					break
				}
			}
			if cut <= 0 {
				for i := end - 1; i > lower; i-- {
					switch runes[i] {
					case '.', '!', '?', '。', '！', '？', ';':
						cut = i + 1
					}
					if cut > 0 {
						break
					}
				}
			}
			if cut > 0 {
				end = cut
			}
		}
		if piece := strings.TrimSpace(string(runes[start:end])); piece != "" {
			out = append(out, piece)
		}
		if end >= len(runes) {
			break
		}
		next := end - overlap
		if next <= start {
			next = end
		}
		start = next
	}
	return out
}

// ragIndexHash fingerprints the indexed text together with the model so that
// re-running the index only re-embeds notes whose text or model changed.
func ragIndexHash(model, text string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

type ragNote struct {
	id   int64
	name string
	path string
	text string
}

// BuildRAGIndex embeds every note into rag_chunks. Notes whose text and model
// are unchanged since the last run are skipped. It is the built-in one-by-one
// "RAG index" process.
func BuildRAGIndex(ctx context.Context, r Reporter, db *sql.DB, c Config) error {
	model := NormalizeEmbeddingModel(c.EmbeddingModel)
	if err := EnsureOllamaModel(ctx, r, c.Host, model); err != nil {
		return err
	}

	rows, err := db.Query(`SELECT id, file_name, file_path, markdown, mime_type FROM notes ORDER BY id`)
	if err != nil {
		return err
	}
	var notes []ragNote
	for rows.Next() {
		var id int64
		var name, path, md, mime string
		if err := rows.Scan(&id, &name, &path, &md, &mime); err != nil {
			rows.Close()
			return err
		}
		notes = append(notes, ragNote{id: id, name: name, path: path, text: textForModel(md, mime)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	workers := normalizedWorkers(c.Workers)
	r.SetPhase(fmt.Sprintf("RAG index (%s, ×%d)", model, workers), 0, len(notes))
	if len(notes) == 0 {
		r.Logf("no notes to index")
		return nil
	}

	var mu sync.Mutex
	var done, skipped, chunkCount int
	err = runConcurrent(ctx, workers, notes, func(n ragNote) error {
		if strings.TrimSpace(n.text) == "" {
			r.Advance(true)
			return nil
		}
		_, docPrompt := embeddingPrompts(model)
		titleText := strings.TrimSpace(n.name)
		chunker := fmt.Sprintf("chunk=%d/%d", ragChunkSize, ragChunkOverlap)
		hash := ragIndexHash(model, docPrompt+chunker+"\n"+titleText+"\n"+n.text)
		var existing string
		if db.QueryRow(`SELECT hash FROM rag_notes WHERE note_id=? AND model=?`, n.id, model).Scan(&existing) == nil && existing == hash {
			mu.Lock()
			skipped++
			mu.Unlock()
			r.Advance(true)
			return nil
		}

		pieces := chunkText(n.text, ragChunkSize, ragChunkOverlap)
		type chunkVec struct {
			idx  int
			text string
			vec  []float32
		}
		var cvs []chunkVec
		for i, piece := range pieces {
			r.SetCurrent(n.name)
			vec, err := EmbedDocument(ctx, c, n.name, piece)
			if err != nil {
				r.Fail("%s: %v", n.name, err)
				r.Advance(false)
				return nil
			}
			cvs = append(cvs, chunkVec{idx: i, text: piece, vec: vec})
		}
		if len(cvs) == 0 {
			r.Advance(true)
			return nil
		}

		dim := len(cvs[0].vec)
		now := time.Now().UTC().Format(time.RFC3339)
		saveMu.Lock()
		tx, err := db.Begin()
		if err == nil {
			if _, err = tx.Exec(`DELETE FROM rag_chunks WHERE note_id=?`, n.id); err == nil {
				for _, cv := range cvs {
					if _, err = tx.Exec(
						`INSERT INTO rag_chunks(note_id, chunk_idx, text, model, dim, embedding, created_at) VALUES(?,?,?,?,?,?,?)`,
						n.id, cv.idx, cv.text, model, len(cv.vec), encodeVec(cv.vec), now); err != nil {
						break
					}
				}
			}
			if err == nil {
				_, err = tx.Exec(
					`INSERT OR REPLACE INTO rag_notes(note_id, hash, model, chunks, dim, indexed_at) VALUES(?,?,?,?,?,?)`,
					n.id, hash, model, len(cvs), dim, now)
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		saveMu.Unlock()
		if err != nil {
			r.Fail("%s: %v", n.name, err)
			r.Advance(false)
			return nil
		}

		mu.Lock()
		done++
		chunkCount += len(cvs)
		mu.Unlock()
		r.Advance(false)
		r.Logf("%s: %d chunk(s)", n.name, len(cvs))
		return nil
	})
	if err != nil {
		return err
	}
	r.Logf("RAG index: %d note(s) indexed (%d chunks), %d unchanged", done, chunkCount, skipped)
	return nil
}

// RAGSearch embeds query and returns the topK most similar chunks. topK <= 0
// uses ragDefaultTopK.
func RAGSearch(ctx context.Context, db *sql.DB, c Config, query string, topK int) ([]RAGResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	if topK <= 0 {
		topK = ragDefaultTopK
	}
	topK = NormalizeRAGTopK(topK)
	qv, err := EmbedQuery(ctx, c, query)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`SELECT rc.note_id, n.file_name, n.file_path, rc.text, rc.embedding
		FROM rag_chunks rc JOIN notes n ON n.id=rc.note_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RAGResult
	for rows.Next() {
		var res RAGResult
		var blob []byte
		if err := rows.Scan(&res.NoteID, &res.Name, &res.Path, &res.Chunk, &blob); err != nil {
			return nil, err
		}
		res.Score = cosineSimilarity(qv, decodeVec(blob))
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// RAGAnswer retrieves the most relevant chunks and asks the chat model to
// answer the question from that context. It returns the answer and the chunks
// that were used.
func RAGAnswer(ctx context.Context, db *sql.DB, c Config, query string, topK int) (string, []RAGResult, error) {
	results, err := RAGSearch(ctx, db, c, query, topK)
	if err != nil {
		return "", nil, err
	}
	if len(results) == 0 {
		return "", nil, fmt.Errorf("nothing indexed yet — run the \"%s\" process first", RAGIndexProcess)
	}
	var ctxb strings.Builder
	for i, res := range results {
		fmt.Fprintf(&ctxb, "[%d] %s\n%s\n\n", i+1, res.Name, strings.TrimSpace(res.Chunk))
	}
	prompt := fmt.Sprintf(
		"Ты помощник по личной базе заметок. Ответь на вопрос, используя ТОЛЬКО контекст ниже.\n"+
			"Если ответа в контексте нет — честно скажи, что не знаешь. Отвечай на языке вопроса, кратко и по делу, со ссылками на источники вида [n].\n\n"+
			"=== Контекст ===\n%s\n=== Вопрос ===\n%s",
		ctxb.String(), query)
	answer, err := askOllamaText(ctx, c, prompt, nil)
	if err != nil {
		return "", results, err
	}
	return answer, results, nil
}

// RAGStats reports how many notes and chunks are indexed and which models were
// used. models is empty when nothing is indexed.
func RAGStats(db *sql.DB) (notes, chunks int, models []string) {
	if db == nil {
		return 0, 0, nil
	}
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(chunks),0) FROM rag_notes`).Scan(&notes, &chunks)
	rows, err := db.Query(`SELECT DISTINCT model FROM rag_notes WHERE model<>'' ORDER BY model`)
	if err != nil {
		return notes, chunks, nil
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if rows.Scan(&m) == nil {
			models = append(models, m)
		}
	}
	return notes, chunks, models
}
