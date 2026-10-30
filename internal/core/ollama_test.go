package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// approveReporter implements Reporter (via NoopReporter) and ModelApprover.
type approveReporter struct {
	NoopReporter
	approve bool
	asked   bool
}

func (a *approveReporter) ApproveModelPull(string) bool {
	a.asked = true
	return a.approve
}

func TestPullModelStreamsProgress(t *testing.T) {
	var gotBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"status":"pulling manifest"}` + "\n"))
		_, _ = w.Write([]byte(`{"status":"downloading","total":100,"completed":40}` + "\n"))
		_, _ = w.Write([]byte(`{"status":"success"}` + "\n"))
	}))
	defer srv.Close()

	var statuses []string
	var lastCompleted, lastTotal int64
	err := PullModel(context.Background(), srv.URL, "mxbai-embed-large", func(status string, completed, total int64) {
		statuses = append(statuses, status)
		if status == "downloading" {
			lastCompleted, lastTotal = completed, total
		}
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if gotBody.Model != "mxbai-embed-large" || !gotBody.Stream {
		t.Fatalf("unexpected pull body: %+v", gotBody)
	}
	if len(statuses) != 3 || statuses[0] != "pulling manifest" || statuses[2] != "success" {
		t.Fatalf("unexpected statuses: %v", statuses)
	}
	if lastCompleted != 40 || lastTotal != 100 {
		t.Fatalf("progress = %d/%d, want 40/100", lastCompleted, lastTotal)
	}
}

func TestPullModelReportsErrorField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"pull access denied"}` + "\n"))
	}))
	defer srv.Close()

	err := PullModel(context.Background(), srv.URL, "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("err = %v, want pull access denied", err)
	}
}

func TestEnsureOllamaModelSkipsInstalled(t *testing.T) {
	var pulled int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"mxbai-embed-large:latest"}]}`))
		case "/api/pull":
			atomic.AddInt32(&pulled, 1)
			_, _ = w.Write([]byte(`{"status":"success"}` + "\n"))
		}
	}))
	defer srv.Close()

	if err := EnsureOllamaModel(context.Background(), NoopReporter{}, srv.URL, "mxbai-embed-large"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if atomic.LoadInt32(&pulled) != 0 {
		t.Fatalf("installed model should not be pulled")
	}
}

func TestEnsureOllamaModelPullsWhenApproved(t *testing.T) {
	var pulled int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/pull":
			atomic.AddInt32(&pulled, 1)
			_, _ = w.Write([]byte(`{"status":"success"}` + "\n"))
		}
	}))
	defer srv.Close()

	r := &approveReporter{approve: true}
	if err := EnsureOllamaModel(context.Background(), r, srv.URL, "nomic-embed-text"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !r.asked {
		t.Fatal("expected the approver to be asked")
	}
	if atomic.LoadInt32(&pulled) != 1 {
		t.Fatalf("pull count = %d, want 1", atomic.LoadInt32(&pulled))
	}
}

func TestEnsureOllamaModelDeclinedDoesNotPull(t *testing.T) {
	var pulled int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/pull":
			atomic.AddInt32(&pulled, 1)
			_, _ = w.Write([]byte(`{"status":"success"}` + "\n"))
		}
	}))
	defer srv.Close()

	r := &approveReporter{approve: false}
	err := EnsureOllamaModel(context.Background(), r, srv.URL, "some-model")
	if err == nil || !strings.Contains(err.Error(), "download skipped") {
		t.Fatalf("err = %v, want a skipped-download error", err)
	}
	if atomic.LoadInt32(&pulled) != 0 {
		t.Fatalf("declined model must not be pulled")
	}
}

func TestEnsureOllamaModelUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	err := EnsureOllamaModel(context.Background(), NoopReporter{}, url, "mxbai-embed-large")
	if err == nil || !strings.Contains(err.Error(), "ollama unavailable") {
		t.Fatalf("err = %v, want ollama unavailable", err)
	}
}
