package topicstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func TestCollectionCatalogCASIdempotencyAndIndependentNotes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	topic, e := store.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Design", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	if topic.AutoWeave {
		t.Fatal("collection enabled weave")
	}
	for _, id := range []string{"20261023-one", "20261023-two"} {
		if e = markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: topic.ID, UserTitle: id}, models.ThoughtContent{AINotes: id}); e != nil {
			t.Fatal(e)
		}
	}
	c, e := store.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	req := models.CatalogWriteRequest{OperationID: "catalog", ExpectedGeneration: c.Generation, Groups: []models.CatalogGroup{{ID: "first", Title: "First"}, {ID: "second", Title: "Second"}}, Entries: []models.CatalogEntry{{ID: "planned", GroupID: "first", Title: "Planned", Required: true, Binding: models.CatalogBinding{Kind: "planned"}}, {ID: "one", GroupID: "second", Title: "One", Required: true, Binding: models.CatalogBinding{Kind: "thought", ThoughtID: "20261023-one", VersionPolicy: "latest_finalized"}}}}
	view, e := store.WriteCatalog(ctx, topic.ID, req)
	if e != nil {
		t.Fatal(e)
	}
	if view.RequiredCount != 2 || view.DeliveredCount != 0 || len(view.Uncatalogued) != 1 || view.Entries[1].DeliveryStatus != "ordinary_note" {
		t.Fatalf("%+v", view)
	}
	replay, e := New(root).WriteCatalog(ctx, topic.ID, req)
	if e != nil || replay.Catalog.Generation != view.Catalog.Generation {
		t.Fatal(replay, e)
	}
	req.OperationID = "stale"
	if _, e = store.WriteCatalog(ctx, topic.ID, req); !errors.Is(e, objectlocation.ErrConflict) {
		t.Fatal("lost update", e)
	}
	req.OperationID = "duplicate"
	req.ExpectedGeneration = view.Catalog.Generation
	req.Entries = append(req.Entries, models.CatalogEntry{ID: "dup", GroupID: "second", Title: "duplicate", Binding: req.Entries[1].Binding})
	if _, e = store.WriteCatalog(ctx, topic.ID, req); e == nil {
		t.Fatal("duplicate binding accepted")
	}
	if e = markdown.DeleteThought(root, "20261023-one"); e != nil {
		t.Fatal(e)
	}
	view, e = store.CatalogView(ctx, topic.ID)
	if e != nil || view.RequiredCount != 2 || view.Entries[1].BindingStatus != "missing" {
		t.Fatal(view, e)
	}
	manual := []byte("# Human authored\n")
	if e = os.WriteFile(filepath.Join(root, "topics", topic.ID, "index.md"), manual, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = store.RebuildCatalog(ctx, topic.ID); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(root, "topics", topic.ID, "index.md"))
	if e != nil || string(raw) != string(manual) {
		t.Fatal("manual page overwritten", e)
	}
	if e = store.DeleteCollection(ctx, topic.ID); !errors.Is(e, objectlocation.ErrConflict) {
		t.Fatal("nonempty collection deleted", e)
	}
}

func TestCollectionDeletionCrashRestoresConsistentAbsence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := New(root)
	topic, e := s.Create(ctx, models.TopicCreateRequest{OperationID: "create-delete", Name: "Delete", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RebuildCatalog(ctx, topic.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.StorageSnapshot(ctx, topic.ID); e != nil {
		t.Fatal(e)
	}
	c, e := s.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	req := DeleteCollectionRequest{OperationID: "delete", ExpectedGeneration: topic.Generation, ExpectedCatalogGeneration: c.Generation}
	r := objectlocation.For(root)
	r.Fault = func(phase string) error {
		if phase == "collection_deleted" {
			return os.ErrClosed
		}
		return nil
	}
	if e = s.DeleteCollectionCAS(ctx, topic.ID, req); !errors.Is(e, os.ErrClosed) {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, topic.ID); !os.IsNotExist(e) {
		t.Fatal("deleted topic visible", e)
	}
	r.Fault = nil
	if e = New(root).RecoverCollections(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(root, "topics", topic.ID)); !os.IsNotExist(e) {
		t.Fatal("cleanup not resumed", e)
	}
	if e = s.DeleteCollectionCAS(ctx, topic.ID, req); e != nil {
		t.Fatal("idempotent delete", e)
	}
}

func TestCatalogCanReorderAndUnlinkMissingEntriesWithoutDeletingOwnedContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := New(root)
	topic, e := s.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Missing", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"20261023-missing", "20261023-retain"} {
		if e = markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: topic.ID}, models.ThoughtContent{AINotes: id}); e != nil {
			t.Fatal(e)
		}
	}
	c, e := s.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	entries := []models.CatalogEntry{}
	for _, id := range []string{"20261023-missing", "20261023-retain"} {
		entries = append(entries, models.CatalogEntry{ID: id, GroupID: "default", Title: id, Required: true, Binding: models.CatalogBinding{Kind: "thought", ThoughtID: id, VersionPolicy: "latest_finalized"}})
	}
	view, e := s.WriteCatalog(ctx, topic.ID, models.CatalogWriteRequest{OperationID: "bind", ExpectedGeneration: c.Generation, Groups: c.Groups, Entries: entries})
	if e != nil {
		t.Fatal(e)
	}
	if e = markdown.DeleteThought(root, "20261023-missing"); e != nil {
		t.Fatal(e)
	}
	entries[0].Title = "Still required but missing"
	entries[0], entries[1] = entries[1], entries[0]
	view, e = s.WriteCatalog(ctx, topic.ID, models.CatalogWriteRequest{OperationID: "reorder", ExpectedGeneration: view.Catalog.Generation, Groups: c.Groups, Entries: entries})
	if e != nil || view.RequiredCount != 2 || view.Entries[1].BindingStatus != "missing" {
		t.Fatal(view, e)
	}
	view, e = s.WriteCatalog(ctx, topic.ID, models.CatalogWriteRequest{OperationID: "unlink", ExpectedGeneration: view.Catalog.Generation, Groups: c.Groups, Entries: entries[1:]})
	if e != nil || len(view.Uncatalogued) != 1 || view.Uncatalogued[0].ObjectID != "20261023-retain" {
		t.Fatal(view, e)
	}
	if _, _, e = markdown.ReadThought(root, "20261023-retain"); e != nil {
		t.Fatal(e)
	}
}

func TestConcurrentCatalogCASHasOneWinner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := New(root)
	topic, e := s.Create(ctx, models.TopicCreateRequest{OperationID: "create-race", Name: "Race", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	results := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			<-start
			_, err := New(root).WriteCatalog(ctx, topic.ID, models.CatalogWriteRequest{OperationID: fmt.Sprintf("writer-%d", i), ExpectedGeneration: c.Generation, Groups: c.Groups, Entries: []models.CatalogEntry{{ID: fmt.Sprintf("entry-%d", i), GroupID: "default", Title: fmt.Sprintf("Writer %d", i), Binding: models.CatalogBinding{Kind: "planned"}}}})
			results <- err
		}(i)
	}
	close(start)
	winners := 0
	for i := 0; i < 8; i++ {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, objectlocation.ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful writers=%d", winners)
	}
	current, e := New(root).Catalog(ctx, topic.ID)
	if e != nil || current.Generation != c.Generation+1 || len(current.Entries) != 1 {
		t.Fatal(current, e)
	}
}
