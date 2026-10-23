package searchdb

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

func TestAuthoritativeScopeFiltersBeforeRankingAndPagination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	r := objectlocation.For(root)
	topics := topicstore.New(root)
	topic, e := topics.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Hidden", Mode: "collection", SearchPolicy: &models.TopicSearchPolicy{TopicGlobalVisible: true, OwnedThoughtsGlobalDefault: false}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := Open(ctx, filepath.Join(t.TempDir(), "scope.duckdb"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	store.SetWorkspaceRoot(root)
	entries := []models.CatalogEntry{}
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("20261023-hidden-%03d", i)
		thought := models.Thought{ID: id, OwnerTopicID: topic.ID, UserTitle: "Needle highest match", Summary: "Needle", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		content := models.ThoughtContent{AINotes: "Needle hidden body"}
		if e = markdown.WriteThought(root, thought, content); e != nil {
			t.Fatal(e)
		}
		if e = store.IndexThought(ctx, thought, content); e != nil {
			t.Fatal(e)
		}
		if e = store.IndexEmbedding(ctx, models.EmbeddingRecord{ThoughtID: id, Model: "test", Vector: []float64{1, 0}, Dimension: 2, CreatedAt: time.Now()}); e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			entries = append(entries, models.CatalogEntry{ID: "hidden", GroupID: "default", Title: "Hidden", Binding: models.CatalogBinding{Kind: "thought", ThoughtID: id, VersionPolicy: "latest_finalized"}})
		}
	}
	visible := models.Thought{ID: "20261023-visible", UserTitle: "Needle visible", Summary: "Needle", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	content := models.ThoughtContent{AINotes: "Needle visible body"}
	if e = markdown.WriteThought(root, visible, content); e != nil {
		t.Fatal(e)
	}
	if e = store.IndexThought(ctx, visible, content); e != nil {
		t.Fatal(e)
	}
	if e = store.IndexEmbedding(ctx, models.EmbeddingRecord{ThoughtID: visible.ID, Model: "test", Vector: []float64{0.5, 0.5}, Dimension: 2, CreatedAt: time.Now()}); e != nil {
		t.Fatal(e)
	}
	c, e := topics.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = topics.WriteCatalog(ctx, topic.ID, models.CatalogWriteRequest{OperationID: "catalog", ExpectedGeneration: c.Generation, Groups: c.Groups, Entries: entries}); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		response, e := store.Search(ctx, models.SearchQuery{Query: "Needle", Scope: "global", Mode: mode, QueryVector: []float64{1, 0}, EmbeddingModel: "test", PageSize: 1})
		if e != nil || response.Total != 1 || len(response.Items) != 1 || response.Items[0].ThoughtID != visible.ID {
			t.Fatal(mode, response, e)
		}
		response, e = store.Search(ctx, models.SearchQuery{Query: "Needle", Scope: "topic", TopicID: topic.ID, Mode: mode, QueryVector: []float64{1, 0}, EmbeddingModel: "test", PageSize: 1})
		if e != nil || response.Total != 1 || response.Items[0].ThoughtID != "20261023-hidden-000" {
			t.Fatal("topic", mode, response, e)
		}
	}
	hidden, e := r.Resolve(ctx, "20261023-hidden-000")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.SetSearchPolicy(hidden.ObjectID, models.SearchPolicyRequest{OperationID: "include", ExpectedGeneration: hidden.Generation, GlobalSearch: "include"}); e != nil {
		t.Fatal(e)
	}
	response, e := store.Search(ctx, models.SearchQuery{Query: "Needle", Scope: "global", Mode: "keyword"})
	if e != nil || response.Total != 2 {
		t.Fatal("policy update did not invalidate visibility", response, e)
	}
	if _, e = store.Search(ctx, models.SearchQuery{Scope: "topic"}); e == nil {
		t.Fatal("topic scope without id accepted")
	}
}
