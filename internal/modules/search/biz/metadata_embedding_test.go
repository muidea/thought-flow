package biz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/searchdb"
)

func TestLongNoteRebuildUsesMetadataAndInvalidatesFullTextCache(t *testing.T) {
	ctx := context.Background()
	inputs := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Input) > 4096 || strings.Contains(body.Input, "UNIQUEBODYTOKEN") {
			t.Errorf("oversized or full-text embedding: %d bytes", len(body.Input))
			http.Error(w, "INPUT_TOKEN_LIMIT_EXCEEDED", http.StatusBadRequest)
			return
		}
		inputs <- body.Input
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": []float64{1, 0}}}})
	}))
	defer server.Close()
	root := t.TempDir()
	ws := &models.Workspace{ID: "local", RootPath: root, JobsPath: filepath.Join(root, "runtime/jobs")}
	store, err := searchdb.Open(ctx, filepath.Join(root, "runtime/index.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	jobs := jobstore.New(ws.JobsPath)
	service := NewService(ws, jobs, store, nil, nil, ai.NewEmbeddingProvider(appconfig.EmbeddingConfig{APIKey: "test", BaseURL: server.URL, Model: "test-model"}), "")
	defer service.Close()
	thought := models.Thought{ID: "20261023-100000-longnote", UserTitle: "向量索引", Summary: "根据元数据建立向量索引。", UserTags: []string{"DuckDB"}}
	content := models.ThoughtContent{Original: strings.Repeat("UNIQUEBODYTOKEN 超长正文。", 5000), ExtractedContent: "UNIQUEBODYTOKEN extracted", AINotes: "UNIQUEBODYTOKEN edited"}
	if err = markdown.WriteThought(root, thought, content); err != nil {
		t.Fatal(err)
	}
	// Simulate a cached vector produced by the previous full-text strategy.
	if err = store.IndexEmbedding(ctx, models.EmbeddingRecord{ThoughtID: thought.ID, Model: "test-model", Vector: []float64{0, 1}, Dimension: 2, ContentHash: models.ContentHash(content.Original)}); err != nil {
		t.Fatal(err)
	}
	service.rebuildError = "previous oversized input failure"
	job, err := jobs.Create(models.JobTypeReindex, models.ResourceTypeWorkspace, ws.ID, "queued")
	if err != nil {
		t.Fatal(err)
	}
	service.reindexJob(job)
	status := service.RuntimeStatus(ctx)
	if status.Status != "ready" || status.Vector.VectorRows != 1 || len(inputs) != 1 {
		t.Fatalf("rebuild did not recover with metadata: %+v, requests=%d", status, len(inputs))
	}
	if input := <-inputs; input != models.EmbeddingText(thought) {
		t.Fatalf("incorrect embedding input: %q", input)
	}
	if _, _, err = service.rebuild(ctx); err != nil || len(inputs) != 0 {
		t.Fatalf("unchanged metadata not cached: %v, calls=%d", err, len(inputs))
	}
	content.AINotes += "\nUNIQUEBODYTOKEN another edit"
	if err = markdown.WriteThought(root, thought, content); err != nil {
		t.Fatal(err)
	}
	if err = service.IndexThought(ctx, thought.ID, nil); err != nil || len(inputs) != 0 {
		t.Fatalf("body-only indexing recomputed unchanged metadata: %v", err)
	}
	thought.Summary = "更新后的摘要"
	if err = markdown.WriteThought(root, thought, content); err != nil {
		t.Fatal(err)
	}
	if err = service.IndexThought(ctx, thought.ID, nil); err != nil || len(inputs) != 1 {
		t.Fatalf("summary change did not update vector: %v", err)
	}
	if input := <-inputs; !strings.Contains(input, thought.Summary) {
		t.Fatalf("updated summary missing from embedding: %q", input)
	}
	response, err := service.Search(ctx, models.SearchQuery{Query: "UNIQUEBODYTOKEN", Mode: "keyword"})
	if err != nil || len(response.Items) != 1 {
		t.Fatalf("full-body keyword index lost: %+v %v", response, err)
	}
}
