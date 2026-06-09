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
