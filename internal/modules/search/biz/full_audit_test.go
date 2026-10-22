package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muidea/magicCommon/event"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/searchdb"
)

type auditEmbedder struct {
	calls atomic.Int64
	fail  atomic.Bool
}

func (p *auditEmbedder) EmbeddingSpec() (string, int) { return "audit-model", 2 }
func (p *auditEmbedder) Embed(_ context.Context, req ai.EmbedRequest) (models.EmbeddingRecord, error) {
	p.calls.Add(1)
	if p.fail.Load() {
		return models.EmbeddingRecord{}, errors.New("provider interrupted")
	}
	return models.EmbeddingRecord{ThoughtID: req.ThoughtID, Model: "audit-model", Dimension: 2, Vector: []float64{1, 0}}, nil
}

func TestRebuildCacheFailureAtomicityAndStartupRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws := &models.Workspace{ID: "local", RootPath: root, RuntimePath: filepath.Join(root, "runtime"), JobsPath: filepath.Join(root, "runtime/jobs")}
	path := filepath.Join(ws.RuntimePath, "index.duckdb")
	store, err := searchdb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &auditEmbedder{}
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, provider, path)
	defer service.Close()
	thought := models.Thought{ID: "20261022-100000-recover", UserTitle: "Recovery"}
	content := models.ThoughtContent{Original: "UNIQUEOLD"}
	if err = markdown.WriteThought(root, thought, content); err != nil {
		t.Fatal(err)
	}
	if err = service.Recover(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, e := service.Search(ctx, models.SearchQuery{Query: "UNIQUEOLD", Mode: "keyword"})
		if e == nil && len(response.Items) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup left missing index empty", e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err = service.rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatal("unchanged input recomputed", provider.calls.Load())
	}
	topic := models.Topic{ID: "new-topic", Members: []string{thought.ID}}
	service.Notify(event.NewEvent(models.EventTopicUpdated, "topic", "#", event.NewHeader(), models.DomainEvent{ResourceType: models.ResourceTypeTopic, EventType: models.EventTopicUpdated, Payload: topic}), nil)
	if provider.calls.Load() != 1 {
		t.Fatal("topic update recomputed embeddings")
	}
	response, err := service.Search(ctx, models.SearchQuery{Query: "UNIQUEOLD", TopicID: topic.ID, Mode: "keyword"})
	if err != nil || len(response.Items) != 1 {
		t.Fatal("topic metadata did not update", err)
	}
	content.AINotes = "CHANGED"
	if err = markdown.WriteThought(root, thought, content); err != nil {
		t.Fatal(err)
	}
	provider.fail.Store(true)
	if _, _, err = service.rebuild(ctx); err == nil {
		t.Fatal("failed provider not reported")
	}
	response, err = service.Search(ctx, models.SearchQuery{Query: "UNIQUEOLD", Mode: "keyword"})
	if err != nil || len(response.Items) != 1 {
		t.Fatal("failed rebuild destroyed searchable index", err)
	}
	provider.fail.Store(false)
	if err = service.IndexThought(ctx, thought.ID, &models.EmbeddingRecord{Model: "wrong-refiner-model", Vector: []float64{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	current, c, _ := markdown.ReadThought(root, thought.ID)
	record, ok := store.GetEmbedding(ctx, thought.ID, "audit-model")
	if !ok || record.ContentHash != models.ContentHash(models.EmbeddingText(current, c)) {
		t.Fatal("patched content kept old vector")
	}
	if _, ok = store.GetEmbedding(ctx, thought.ID, "wrong-refiner-model"); ok {
		t.Fatal("used refinement payload vector")
	}
}

func TestEmbeddingKeyMatrixUsesIndependentProvider(t *testing.T) {
	for _, llmKey := range []bool{false, true} {
		for _, embeddingKey := range []bool{false, true} {
			t.Run(fmt.Sprintf("llm=%t/embedding=%t", llmKey, embeddingKey), func(t *testing.T) {
				var requests atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/embeddings" {
						t.Error("unexpected endpoint", r.URL.Path)
					}
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": []float64{1, 0}}}})
				}))
				defer server.Close()
				cfg := appconfig.EmbeddingConfig{BaseURL: server.URL, Model: "remote-model"}
				if embeddingKey {
					cfg.APIKey = "embedding-key"
				}
				llm := appconfig.LLMConfig{}
				if llmKey {
					llm.APIKey = "llm-key"
				}
				_ = ai.NewRefineProvider(llm, cfg)
				root := t.TempDir()
				ws := &models.Workspace{RootPath: root, JobsPath: filepath.Join(root, "runtime/jobs")}
				store, err := searchdb.Open(context.Background(), filepath.Join(root, "runtime/index.duckdb"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, ai.NewEmbeddingProvider(cfg), "")
				defer service.Close()
				thought := models.Thought{ID: "20261022-100000-keymatrix", UserTitle: "Facts"}
				if err = markdown.WriteThought(root, thought, models.ThoughtContent{Original: "Unique factual input"}); err != nil {
					t.Fatal(err)
				}
				if err = service.IndexThought(context.Background(), thought.ID, nil); err != nil {
					t.Fatal(err)
				}
				record, ok := store.GetEmbedding(context.Background(), thought.ID, "")
				if !ok {
					t.Fatal("missing vector")
				}
				if (record.Model == "remote-model") != embeddingKey {
					t.Fatalf("wrong model: %s", record.Model)
				}
				if (requests.Load() > 0) != embeddingKey {
					t.Fatal("embedding configuration ignored")
				}
				_, err = service.Search(context.Background(), models.SearchQuery{Query: "facts", Mode: "semantic"})
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(record.ContentHash) == "" {
					t.Fatal("missing input identity")
				}
			})
		}
	}
}
