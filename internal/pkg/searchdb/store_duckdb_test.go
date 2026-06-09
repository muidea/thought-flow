//go:build duckdb

package searchdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestDuckDBFTSMatchesNonContiguousTerms(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "thoughtflow.duckdb"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 9, 17, 10, 0, 0, time.UTC)
	matching := searchTestThought("20260609-171000-fts-a", "FTS ranking note", now)
	other := searchTestThought("20260609-171000-fts-b", "DuckDB storage note", now.Add(-time.Minute))

	if err := store.IndexThought(ctx, matching, models.ThoughtContent{
		Original: "DuckDB indexes local notes. Ranking uses sparse term statistics.",
	}); err != nil {
		t.Fatalf("IndexThought(matching) error = %v", err)
	}
	if err := store.IndexThought(ctx, other, models.ThoughtContent{
		Original: "DuckDB stores local analytics snapshots for notes.",
	}); err != nil {
		t.Fatalf("IndexThought(other) error = %v", err)
	}
	if _, ok := store.keywordScoresFromFTS(ctx, "duckdb ranking"); !ok {
		t.Skipf("DuckDB FTS extension is unavailable in this environment: %v", store.ftsErr)
	}

	result, err := store.Search(ctx, models.SearchQuery{Query: "duckdb ranking", Mode: "keyword", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("search result total=%d len=%d", result.Total, len(result.Items))
	}
	if result.Items[0].ThoughtID != matching.ID {
		t.Fatalf("thought id = %q, want %q", result.Items[0].ThoughtID, matching.ID)
	}
	if result.Items[0].KeywordScore <= 0 {
		t.Fatalf("expected positive FTS keyword score, got %v", result.Items[0].KeywordScore)
	}
}

func TestDuckDBArrayVectorSemanticSearch(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "thoughtflow.duckdb"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 9, 17, 35, 0, 0, time.UTC)
	nearest := searchTestThought("20260609-173500-vector-a", "Vector nearest note", now)
	distant := searchTestThought("20260609-173500-vector-b", "Vector distant note", now.Add(-time.Minute))
	for _, thought := range []models.Thought{nearest, distant} {
		if err := store.IndexThought(ctx, thought, models.ThoughtContent{Original: "Vector similarity fixture."}); err != nil {
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

	scores, ok := store.semanticScoresFromDuckDB(ctx, []float64{1, 0, 0}, "test-embedding")
	if !ok {
		t.Fatalf("expected DuckDB ARRAY vector scores")
	}
	if scores[nearest.ID] <= scores[distant.ID] {
		t.Fatalf("semantic scores = %#v", scores)
	}

	result, err := store.Search(ctx, models.SearchQuery{
		Query:          "nearest vector",
		Mode:           "semantic",
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
	if result.Items[0].SemanticScore < 0.99 {
		t.Fatalf("top semantic score = %v", result.Items[0].SemanticScore)
	}
}

func searchTestThought(id string, title string, updatedAt time.Time) models.Thought {
	return models.Thought{
		ID:            id,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     title,
		DisplayTitle:  title,
		Path:          "thoughts/2026/06/" + id + ".md",
		CreatedAt:     updatedAt,
		UpdatedAt:     updatedAt,
		ContentHash:   models.ContentHash(title),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
}
