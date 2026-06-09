package searchdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestIndexAndSearchThought(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "thoughtflow.duckdb"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 9, 15, 30, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            "20260609-153000-search",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     "DuckDB search note",
		DisplayTitle:  "DuckDB search note",
		Path:          "thoughts/2026/06/20260609-153000-search.md",
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("hybrid search"),
		UserTags:      []string{"search"},
		AITags:        []string{"engineering"},
		TopicIDs:      []string{"duckdb-notes"},
		Summary:       "DuckDB keyword search baseline",
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	content := models.ThoughtContent{
		Original: "Hybrid search should find this DuckDB note.",
		AINotes:  "Summary: DuckDB keyword search baseline",
	}
	if err := store.IndexThought(ctx, thought, content); err != nil {
		t.Fatalf("IndexThought() error = %v", err)
	}
	if err := store.IndexEmbedding(ctx, models.EmbeddingRecord{
		ThoughtID:   thought.ID,
		Model:       "test-embedding",
		Dimension:   3,
		Vector:      []float64{1, 0, 0},
		ContentHash: models.ContentHash("hybrid search"),
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("IndexEmbedding() error = %v", err)
	}

	result, err := store.Search(ctx, models.SearchQuery{Query: "duckdb", Mode: "hybrid", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("search result total=%d len=%d", result.Total, len(result.Items))
	}
	if result.Items[0].ThoughtID != thought.ID {
		t.Fatalf("thought id = %q", result.Items[0].ThoughtID)
	}
	if result.Items[0].KeywordScore <= 0 {
		t.Fatalf("expected positive keyword score")
	}
	if len(result.Items[0].Topics) != 1 || result.Items[0].Topics[0] != "duckdb-notes" {
		t.Fatalf("topics = %#v", result.Items[0].Topics)
	}

	filtered, err := store.Search(ctx, models.SearchQuery{Query: "duckdb", TopicID: "duckdb-notes", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search() with topic filter error = %v", err)
	}
	if filtered.Total != 1 {
		t.Fatalf("topic filtered total = %d", filtered.Total)
	}
	empty, err := store.Search(ctx, models.SearchQuery{Query: "duckdb", TopicID: "other-topic", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search() with unmatched topic filter error = %v", err)
	}
	if empty.Total != 0 {
		t.Fatalf("unmatched topic filtered total = %d", empty.Total)
	}

	semantic, err := store.Search(ctx, models.SearchQuery{
		Query:          "analytics",
		Mode:           "semantic",
		QueryVector:    []float64{1, 0, 0},
		EmbeddingModel: "test-embedding",
		Page:           1,
		PageSize:       10,
	})
	if err != nil {
		t.Fatalf("semantic Search() error = %v", err)
	}
	if semantic.Total != 1 || len(semantic.Items) != 1 {
		t.Fatalf("semantic result total=%d len=%d", semantic.Total, len(semantic.Items))
	}
	if semantic.Items[0].SemanticScore <= 0 {
		t.Fatalf("expected positive semantic score, got %v", semantic.Items[0].SemanticScore)
	}
}

func TestSearchSortWeightsAndExplain(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "thoughtflow.duckdb"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 9, 18, 20, 0, 0, time.UTC)
	nearest := models.Thought{
		ID:            "20260609-182000-rank-a",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     "Nearest ranking note",
		DisplayTitle:  "Nearest ranking note",
		Path:          "thoughts/2026/06/20260609-182000-rank-a.md",
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("nearest ranking"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	distant := nearest
	distant.ID = "20260609-182000-rank-b"
	distant.UserTitle = "Distant ranking note"
	distant.DisplayTitle = "Distant ranking note"
	distant.Path = "thoughts/2026/06/20260609-182000-rank-b.md"
	distant.UpdatedAt = now.Add(-time.Minute)
	distant.ContentHash = models.ContentHash("distant ranking")

	for _, thought := range []models.Thought{nearest, distant} {
		if err := store.IndexThought(ctx, thought, models.ThoughtContent{Original: "Ranking explain fixture."}); err != nil {
			t.Fatalf("IndexThought(%s) error = %v", thought.ID, err)
		}
	}
	if err := store.IndexEmbedding(ctx, models.EmbeddingRecord{
		ThoughtID:   nearest.ID,
		Model:       "test-embedding",
		Dimension:   3,
		Vector:      []float64{1, 0, 0},
		ContentHash: models.ContentHash(nearest.ID),
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("IndexEmbedding(nearest) error = %v", err)
	}
	if err := store.IndexEmbedding(ctx, models.EmbeddingRecord{
		ThoughtID:   distant.ID,
		Model:       "test-embedding",
		Dimension:   3,
		Vector:      []float64{0, 1, 0},
		ContentHash: models.ContentHash(distant.ID),
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("IndexEmbedding(distant) error = %v", err)
	}

	result, err := store.Search(ctx, models.SearchQuery{
		Query:          "ranking",
		Mode:           "semantic",
		Sort:           "semantic",
		Explain:        true,
		Weights:        models.SearchWeights{Semantic: 2, Recency: 1},
		QueryVector:    []float64{1, 0, 0},
		EmbeddingModel: "test-embedding",
		Page:           1,
		PageSize:       10,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.Total != 2 || len(result.Items) != 2 {
		t.Fatalf("search result total=%d len=%d", result.Total, len(result.Items))
	}
	if result.Items[0].ThoughtID != nearest.ID {
		t.Fatalf("top thought id = %q, want %q", result.Items[0].ThoughtID, nearest.ID)
	}
	explain := result.Items[0].Explain
	if explain == nil {
		t.Fatal("expected explain payload")
	}
	if explain.Sort != "semantic" || explain.Mode != "semantic" {
		t.Fatalf("explain mode/sort = %q/%q", explain.Mode, explain.Sort)
	}
	if explain.Weights.Semantic < 0.66 || explain.Weights.Semantic > 0.67 {
		t.Fatalf("normalized semantic weight = %v", explain.Weights.Semantic)
	}
	if explain.Weights.Recency < 0.33 || explain.Weights.Recency > 0.34 {
		t.Fatalf("normalized recency weight = %v", explain.Weights.Recency)
	}
	if explain.Components.Semantic <= 0 {
		t.Fatalf("semantic component = %v", explain.Components.Semantic)
	}
}
