package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestSingleProcessUsesWorkers verifies that one-by-one processes issue
// concurrent ollama requests when more than one worker is configured.
func TestSingleProcessUsesWorkers(t *testing.T) {
	db := openTestDB(t)
	const notes = 6
	for i := 0; i < notes; i++ {
		if _, err := db.Exec(`INSERT INTO notes(file_path, abs_path, file_name, content, mime_type, imported_at)
			VALUES(?,?,?,'hello','text/markdown','2024-01-02')`,
			fmt.Sprintf("n%d.md", i), fmt.Sprintf("/x/n%d.md", i), fmt.Sprintf("n%d.md", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := SavePrompt(db, "test_single", "${FILE_NAME}"); err != nil {
		t.Fatal(err)
	}

	var inflight, maxInflight int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		cur := atomic.AddInt32(&inflight, 1)
		for {
			max := atomic.LoadInt32(&maxInflight)
			if cur <= max || atomic.CompareAndSwapInt32(&maxInflight, max, cur) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"ok"}`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Host = srv.URL
	cfg.Model = "m"
	cfg.PromptName = "test_single"
	cfg.Workers = 4
	if err := RunSingleProcess(context.Background(), NoopReporter{}, db, cfg); err != nil {
		t.Fatalf("run single: %v", err)
	}

	if got := atomic.LoadInt32(&maxInflight); got < 2 {
		t.Fatalf("max concurrent requests = %d, want >= 2 with 4 workers", got)
	}
	var outputs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM process_output WHERE process_name='test_single'`).Scan(&outputs); err != nil {
		t.Fatal(err)
	}
	if outputs != notes {
		t.Fatalf("saved outputs = %d, want %d", outputs, notes)
	}
}

func TestNormalizedWorkers(t *testing.T) {
	cases := map[int]int{0: 1, -3: 1, 1: 1, 7: 7, 100: 100, 500: 100}
	for in, want := range cases {
		if got := normalizedWorkers(in); got != want {
			t.Fatalf("normalizedWorkers(%d) = %d, want %d", in, got, want)
		}
	}
}
