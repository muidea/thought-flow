package searchdb

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"thoughtflow/internal/pkg/models"
)

func TestFiltersMatchWholeElementsIncludingWildcards(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "index.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, thought := range []models.Thought{
		{ID: "exact", UserTitle: "Exact", TopicIDs: []string{"ai"}, UserTags: []string{"go", "%", "a_b"}},
		{ID: "prefix", UserTitle: "Prefix", TopicIDs: []string{"ai-tools"}, UserTags: []string{"golang", "abc"}},
	} {
		if err = store.IndexThought(ctx, thought, models.ThoughtContent{Original: "common content"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []models.SearchQuery{{TopicID: "ai"}, {Tags: []string{"go"}}, {Tags: []string{"%"}}, {Tags: []string{"a_b"}}} {
		query.Mode = "keyword"
		query.Query = "common"
		response, err := store.Search(ctx, query)
		if err != nil || len(response.Items) != 1 || response.Items[0].ThoughtID != "exact" {
			t.Fatalf("inexact filter %+v: %+v %v", query, response, err)
		}
	}
}

func TestFailedReplacementKeepsPreviousTextAndVectors(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "index.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	thought := models.Thought{ID: "original", UserTitle: "Original"}
	content := models.ThoughtContent{Original: "UNIQUEOLD"}
	record := models.EmbeddingRecord{ThoughtID: thought.ID, Model: "model", Dimension: 2, Vector: []float64{1, 0}, ContentHash: models.ContentHash(models.EmbeddingText(thought))}
	if err = store.ReplaceWorkspace(ctx, []models.ThoughtSnapshot{{Thought: thought, Content: content}}, []models.EmbeddingRecord{record}); err != nil {
		t.Fatal(err)
	}
	bad := record
	bad.Dimension = 3
	if err = store.ReplaceWorkspace(ctx, []models.ThoughtSnapshot{{Thought: models.Thought{ID: "new"}, Content: models.ThoughtContent{Original: "replacement"}}}, []models.EmbeddingRecord{bad}); err == nil {
		t.Fatal("invalid replacement succeeded")
	}
	response, err := store.Search(ctx, models.SearchQuery{Query: "UNIQUEOLD", Mode: "keyword"})
	if err != nil || len(response.Items) != 1 {
		t.Fatalf("old index lost: %+v %v", response, err)
	}
	if cached, ok := store.GetEmbedding(ctx, thought.ID, "model"); !ok || cached.ContentHash != record.ContentHash {
		t.Fatal("old vector lost")
	}
	content.AINotes = "newly edited input"
	if err = store.IndexThought(ctx, thought, content); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetEmbedding(ctx, thought.ID, "model"); !ok {
		t.Fatal("body-only edit invalidated unchanged metadata embedding")
	}
	thought.Summary = "Newly refined summary"
	if err = store.IndexThought(ctx, thought, content); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetEmbedding(ctx, thought.ID, "model"); ok {
		t.Fatal("changed content retained stale embedding")
	}
	scores, _, _ := store.SemanticScores(ctx, []float64{1, 0}, "model", 10)
	if _, ok := scores[thought.ID]; ok {
		t.Fatal("stale vector table row survived invalidation")
	}
}

func TestVectorIndexesSurviveTransactionalUpdatesAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors.duckdb")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	thought := models.Thought{ID: "mutable-vector", UserTitle: "Mutable vector"}
	content := models.ThoughtContent{Original: "original evidence"}
	vector := make([]float64, 64)
	vector[0] = 1
	record := models.EmbeddingRecord{ThoughtID: thought.ID, Model: "local-model", Dimension: 64, Vector: vector}
	for iteration := range 4 {
		content.AINotes = fmt.Sprintf("updated evidence %d", iteration)
		thought.Summary = fmt.Sprintf("updated summary %d", iteration)
		record.ContentHash = models.ContentHash(models.EmbeddingText(thought))
		if err = store.IndexThought(ctx, thought, content); err != nil {
			t.Fatal(err)
		}
		if err = store.IndexEmbedding(ctx, record); err != nil {
			t.Fatal(err)
		}
		// Runtime status and semantic reads lazily create HNSW where available.
		status := store.VectorRuntimeStatus(ctx)
		if status.VectorRows != 1 {
			t.Fatalf("incorrect vector state: %+v", status)
		}
		scores, _, _ := store.SemanticScores(ctx, vector, record.Model, 10)
		if scores[thought.ID] < .99 {
			t.Fatalf("current vector unavailable: %+v", scores)
		}
		if iteration == 1 {
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = store.ReplaceWorkspace(ctx, []models.ThoughtSnapshot{{Thought: thought, Content: content}}, []models.EmbeddingRecord{record}); err != nil {
		t.Fatal(err)
	}
	store.VectorRuntimeStatus(ctx)
	if err = store.DeleteThought(ctx, thought.ID); err != nil {
		t.Fatal(err)
	}
	if status := store.VectorRuntimeStatus(ctx); status.VectorRows != 0 || status.EmbeddingRecords != 0 {
		t.Fatalf("deleted vector survived: %+v", status)
	}
}
