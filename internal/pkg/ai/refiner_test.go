package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
)

func TestLocalProviderRefineIncludesEmbedding(t *testing.T) {
	provider := NewLocalRefineProvider()
	refinement, err := provider.Refine(context.Background(), RefineRequest{
		Thought: models.Thought{ID: "thought-1"},
		Content: models.ThoughtContent{Original: "DuckDB search and embedding should work together."},
	})
	if err != nil {
		t.Fatalf("Refine() error = %v", err)
	}
	if refinement.Summary == "" {
		t.Fatalf("expected summary")
	}
	if refinement.Embedding == nil {
		t.Fatalf("expected embedding")
	}
	if refinement.Embedding.Dimension != len(refinement.Embedding.Vector) {
		t.Fatalf("embedding dimension = %d len = %d", refinement.Embedding.Dimension, len(refinement.Embedding.Vector))
	}
}

func TestOpenAICompatibleProviderRefineAndEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header = %q", req.Header.Get("Authorization"))
		}
		res.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/v1/chat/completions":
			_ = json.NewEncoder(res).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]string{
							"content": `{"summary":"Cloud summary","key_points":["Point A"],"tags":["cloud"],"title":"Cloud title"}`,
						},
					},
				},
			})
		case "/v1/embeddings":
			_ = json.NewEncoder(res).Encode(map[string]any{
				"data": []map[string]any{
					{"embedding": []float64{0.1, 0.2, 0.3}},
				},
			})
		default:
			http.NotFound(res, req)
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.AIConfig{
		BaseURL:        server.URL,
		APIKey:         "test-key",
		ChatModel:      "chat-model",
		EmbeddingModel: "embedding-model",
		Timeout:        time.Second,
	})
	refinement, err := provider.Refine(context.Background(), RefineRequest{
		Thought: models.Thought{ID: "thought-1", UserTitle: "Original title"},
		Content: models.ThoughtContent{Original: "Content to refine."},
	})
	if err != nil {
		t.Fatalf("Refine() error = %v", err)
	}
	if refinement.Summary != "Cloud summary" {
		t.Fatalf("summary = %q", refinement.Summary)
	}
	if refinement.ExtractedTitle != "Cloud title" {
		t.Fatalf("title = %q", refinement.ExtractedTitle)
	}
	if refinement.Embedding == nil || refinement.Embedding.Model != "embedding-model" {
		t.Fatalf("embedding = %#v", refinement.Embedding)
	}
	if len(refinement.Embedding.Vector) != 3 {
		t.Fatalf("embedding vector = %#v", refinement.Embedding.Vector)
	}
}
