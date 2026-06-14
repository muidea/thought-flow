package searchdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/markdown"
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

	preview, err := store.GetSearchPreview(ctx, thought.ID)
	if err != nil {
		t.Fatalf("GetSearchPreview() error = %v", err)
	}
	if preview.ThoughtID != thought.ID || preview.Path != thought.Path {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Snippet == "" || preview.Title != thought.DisplayTitle {
		t.Fatalf("preview content = %#v", preview)
	}
	if len(preview.Topics) != 1 || preview.Topics[0] != "duckdb-notes" {
		t.Fatalf("preview topics = %#v", preview.Topics)
	}
}

func TestRecoverableDuckDBOpenErrorDetection(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{err: os.ErrNotExist, want: false},
		{err: &errorString{"IO Error: database is locked by another process"}, want: false},
		{err: &errorString{"Dependency Error: Failure while replaying WAL file thoughtflow.duckdb.wal"}, want: true},
		{err: &errorString{"Cannot drop entry fts_main_thought_contents because there are entries that depend on it"}, want: true},
		{err: &errorString{"IO Error: database file appears corrupt"}, want: true},
	}
	for _, tc := range cases {
		if got := isRecoverableDuckDBOpenError(tc.err); got != tc.want {
			t.Fatalf("isRecoverableDuckDBOpenError(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestQuarantineDuckDBFilesMovesDatabaseSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "thoughtflow.duckdb")
	for _, name := range []string{"thoughtflow.duckdb", "thoughtflow.duckdb.wal"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "other.duckdb"), []byte("other"), 0o644); err != nil {
		t.Fatalf("WriteFile(other): %v", err)
	}

	if err := quarantineDuckDBFiles(dbPath); err != nil {
		t.Fatalf("quarantineDuckDBFiles() error = %v", err)
	}
	for _, name := range []string{"thoughtflow.duckdb", "thoughtflow.duckdb.wal"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists or stat failed differently: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other.duckdb")); err != nil {
		t.Fatalf("other.duckdb should remain: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	var quarantine string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "corrupt-") {
			quarantine = filepath.Join(dir, entry.Name())
			break
		}
	}
	if quarantine == "" {
		t.Fatalf("quarantine dir not created")
	}
	for _, name := range []string{"thoughtflow.duckdb", "thoughtflow.duckdb.wal"} {
		if _, err := os.Stat(filepath.Join(quarantine, name)); err != nil {
			t.Fatalf("quarantined %s missing: %v", name, err)
		}
	}
}

type errorString struct {
	message string
}

func (e *errorString) Error() string {
	return e.message
}

func TestReindexWorkspaceBuildsIndexFromMarkdown(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, ".thoughtflow", "thoughtflow.duckdb"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	stale := searchRangeThought("20260609-160000-stale", "Stale note", time.Date(2026, 6, 9, 16, 0, 0, 0, time.UTC))
	if err := store.IndexThought(ctx, stale, models.ThoughtContent{Original: "stale content"}); err != nil {
		t.Fatalf("IndexThought(stale) error = %v", err)
	}
	thought := searchRangeThought("20260609-160500-reindex", "Reindexed note", time.Date(2026, 6, 9, 16, 5, 0, 0, time.UTC))
	thought.TopicIDs = []string{"reindex-topic"}
	content := models.ThoughtContent{Original: "Workspace reindex should rebuild search from Markdown."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	count, err := store.ReindexWorkspace(ctx, root)
	if err != nil {
		t.Fatalf("ReindexWorkspace() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("reindex count = %d", count)
	}
	result, err := store.Search(ctx, models.SearchQuery{Query: "rebuild", Mode: "keyword", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.Total != 1 || result.Items[0].ThoughtID != thought.ID || result.Items[0].Path != thought.Path {
		t.Fatalf("search result = %#v", result)
	}
	staleResult, err := store.Search(ctx, models.SearchQuery{Query: "stale", Mode: "keyword", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search(stale) error = %v", err)
	}
	if staleResult.Total != 0 {
		t.Fatalf("stale result = %#v", staleResult)
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

func TestSearchFiltersUpdatedAtRange(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "thoughtflow.duckdb"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	for _, thought := range []models.Thought{
		searchRangeThought("20260609-110000-range", "Older range note", base.Add(-time.Hour)),
		searchRangeThought("20260609-120000-range", "Current range note", base),
		searchRangeThought("20260609-130000-range", "Newer range note", base.Add(time.Hour)),
	} {
		if err := store.IndexThought(ctx, thought, models.ThoughtContent{Original: "Range filter fixture."}); err != nil {
			t.Fatalf("IndexThought(%s) error = %v", thought.ID, err)
		}
	}

	result, err := store.Search(ctx, models.SearchQuery{
		Query:    "range",
		Mode:     "keyword",
		From:     base.Add(-time.Minute),
		To:       base.Add(time.Minute),
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("search result total=%d len=%d", result.Total, len(result.Items))
	}
	if result.Items[0].ThoughtID != "20260609-120000-range" {
		t.Fatalf("thought id = %q", result.Items[0].ThoughtID)
	}

	fromOnly, err := store.Search(ctx, models.SearchQuery{Query: "range", Mode: "keyword", From: base, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search(from only) error = %v", err)
	}
	if fromOnly.Total != 2 {
		t.Fatalf("from-only total = %d", fromOnly.Total)
	}

	toOnly, err := store.Search(ctx, models.SearchQuery{Query: "range", Mode: "keyword", To: base, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search(to only) error = %v", err)
	}
	if toOnly.Total != 2 {
		t.Fatalf("to-only total = %d", toOnly.Total)
	}
}

func searchRangeThought(id string, title string, updatedAt time.Time) models.Thought {
	return models.Thought{
		ID:            id,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     title,
		DisplayTitle:  title,
		Path:          filepath.ToSlash(filepath.Join("thoughts", "2026", "06", id+".md")),
		CreatedAt:     updatedAt,
		UpdatedAt:     updatedAt,
		ContentHash:   models.ContentHash(id),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
}
