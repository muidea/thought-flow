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
}
