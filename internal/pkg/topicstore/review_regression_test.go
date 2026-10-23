package topicstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/searchdb"
)

func TestImportRecoveryNeverOverwritesForeignTopicOrFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := New(root)
	topic, err := source.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Import guard", Mode: "collection"})
	if err != nil {
		t.Fatal(err)
	}
	id := "20261023-import-guard"
	if err = markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: topic.ID}, models.ThoughtContent{AINotes: "original"}); err != nil {
		t.Fatal(err)
	}
	location, err := objectlocation.For(root).Resolve(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := objectlocation.For(root).UploadResource(ctx, topic.ID, id, "upload", "file.txt", location.Generation, []byte("original asset"))
	if err != nil {
		t.Fatal(err)
	}
	exported, err := source.ExportPackage(ctx, topic.ID, ExportRequest{OperationID: "export", Kind: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.ExportBytes(topic.ID, exported.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"import_validated", "import_reserved", "import_copied", "import_activated"} {
		for _, kind := range []string{"topic", "asset", "index", "catalog"} {
			t.Run(phase+"/"+kind, func(t *testing.T) {
				dest := t.TempDir()
				r := objectlocation.For(dest)
				store := New(dest)
				r.Fault = func(point string) error {
					if point == phase {
						return os.ErrClosed
					}
					return nil
				}
				if _, err = store.ImportPackage(ctx, "import", raw); !errors.Is(err, os.ErrClosed) {
					t.Fatal(err)
				}
				r.Fault = nil
				path := "topics/" + topic.ID + "/index.md"
				switch kind {
				case "topic":
					if _, err = store.Create(ctx, models.TopicCreateRequest{OperationID: "foreign", Name: "Import guard", Mode: "collection", Description: "Created after interrupted import"}); err != nil {
						t.Fatal(err)
					}
					path = "topics/" + topic.ID + "/topic.yaml"
				case "asset":
					path = resource.Path
				case "catalog":
					path = catalogPath(topic.ID)
				}
				if kind != "topic" {
					if err = objectlocation.WriteFile(dest, path, []byte("FOREIGN DATA"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(filepath.Join(dest, path))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.RetryImport(ctx, "import"); !errors.Is(err, objectlocation.ErrConflict) {
					t.Fatalf("foreign %s accepted: %v", kind, err)
				}
				if err = New(dest).RecoverImports(ctx); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(filepath.Join(dest, path))
				if err != nil || string(after) != string(before) {
					t.Fatalf("foreign data overwritten: %s %v", after, err)
				}
			})
		}
	}
}
func TestBackupRejectsBrokenFormalHistory(t *testing.T) {
	root, store, _, id, first := finalizedFixture(t)
	if err := markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes += "\nnew working revision"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	l, err := objectlocation.For(root).Resolve(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, l.Placement.DocumentRoot, "revisions", first.RevisionID, "body.md")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ExportPackage(context.Background(), "finalized", ExportRequest{OperationID: "broken", Kind: "backup"}); err == nil {
		t.Fatal("damaged history exported as a complete backup")
	}
}
func TestBackupPreservesExternalAndMissingRelations(t *testing.T) {
	for _, kind := range []string{"global", "other-owner", "missing", "mixed"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store := New(root)
			topic, err := store.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Relations", Mode: "collection"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Create(ctx, models.TopicCreateRequest{OperationID: "other", Name: "Other", Mode: "collection"})
			if err != nil {
				t.Fatal(err)
			}
			entries := []models.CatalogEntry{}
			owners := []string{""}
			if kind == "other-owner" {
				owners = []string{"other"}
			}
			if kind == "mixed" {
				owners = []string{"", "other", topic.ID}
			}
			for i, owner := range owners {
				id := "20261023-relation-" + string(rune('a'+i))
				if err = markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: owner}, models.ThoughtContent{AINotes: "reference body"}); err != nil {
					t.Fatal(err)
				}
				entries = append(entries, models.CatalogEntry{ID: id, GroupID: "default", Title: id, Required: true, Binding: models.CatalogBinding{Kind: "thought", ThoughtID: id, VersionPolicy: "latest_finalized"}})
			}
			c, err := store.Catalog(ctx, topic.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.WriteCatalog(ctx, topic.ID, models.CatalogWriteRequest{OperationID: "bind", ExpectedGeneration: c.Generation, Groups: c.Groups, Entries: entries}); err != nil {
				t.Fatal(err)
			}
			if kind == "missing" {
				if err = markdown.DeleteThought(root, entries[0].Binding.ThoughtID); err != nil {
					t.Fatal(err)
				}
			}
			exported, err := store.ExportPackage(ctx, topic.ID, ExportRequest{OperationID: "export", Kind: "backup"})
			if err != nil {
				t.Fatal(err)
			}
			if len(exported.Manifest.Relations) != len(entries) {
				t.Fatal(exported.Manifest.Relations)
			}
			raw, err := store.ExportBytes(topic.ID, exported.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, hasExternal := range []bool{false, true} {
				dest := t.TempDir()
				if hasExternal && kind != "missing" {
					for _, entry := range entries {
						if entry.Binding.ThoughtID == "20261023-relation-c" {
							continue
						}
						if err = markdown.WriteThought(dest, models.Thought{ID: entry.Binding.ThoughtID}, models.ThoughtContent{AINotes: "destination original"}); err != nil {
							t.Fatal(err)
						}
					}
				}
				imported := New(dest)
				if _, err = imported.ImportPackage(ctx, "import", raw); err != nil {
					t.Fatal(err)
				}
				view, err := imported.CatalogView(ctx, topic.ID)
				if err != nil {
					t.Fatal(err)
				}
				if view.RequiredCount != len(entries) || len(view.Entries) != len(entries) {
					t.Fatal(view)
				}
				if !hasExternal || kind == "missing" {
					if view.Entries[0].BindingStatus != "missing" {
						t.Fatal(view.Entries[0])
					}
				}
				if hasExternal && kind != "missing" {
					_, body, err := markdown.ReadThought(dest, entries[0].Binding.ThoughtID)
					if err != nil || body.AINotes != "destination original" {
						t.Fatal(body, err)
					}
				}
			}
		})
	}
}
func TestDraftSourceFreshnessDoesNotReenterNamespaceLease(t *testing.T) {
	root, store, flow, id, _ := finalizedFixture(t)
	ctx := context.Background()
	r := objectlocation.For(root)
	sourceID := "20261023-source"
	if err := markdown.WriteThought(root, models.Thought{ID: sourceID}, models.ThoughtContent{AINotes: "source body"}); err != nil {
		t.Fatal(err)
	}
	current, err := flow.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	data, err := flow.Store.Revision(id, current.Revision.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	data.Sources.Sources = append(data.Sources.Sources, models.DocumentSource{ID: "S2", Type: "thought", ResourceID: sourceID, Hash: "old", VerificationStatus: "verified"})
	if _, err = flow.CreateRevision(id, current.Head.Generation, data); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Catalog(ctx, "finalized")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.WriteCatalog(ctx, "finalized", models.CatalogWriteRequest{OperationID: "bind", ExpectedGeneration: catalog.Generation, Groups: catalog.Groups, Entries: []models.CatalogEntry{{ID: "entry", GroupID: "default", Title: "Entry", Binding: models.CatalogBinding{Kind: "thought", ThoughtID: id, VersionPolicy: "latest_finalized"}}}}); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	flow.Options.SourceHash = func(source models.DocumentSource) (string, error) {
		_, body, err := markdown.ReadThought(root, source.ResourceID)
		return documentstore.HashJSON(body), err
	}
	flow.Options.SourceHashUnderLease = func(source models.DocumentSource) (string, error) {
		close(entered)
		<-resume
		raw, err := markdown.ReadThoughtRawUnderLease(root, source.ResourceID)
		if err != nil {
			return "", err
		}
		_, body := markdown.ParseThought(raw)
		return documentstore.HashJSON(body), nil
	}
	done := make(chan error, 1)
	go func() { _, err := store.EntryContent(ctx, "finalized", "entry", "draft", ""); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("source check not reached")
	}
	writer := make(chan struct{})
	go func() { r.Namespace.Lock(); r.Namespace.Unlock(); close(writer) }()
	deadline := time.Now().Add(3 * time.Second)
	for r.Namespace.TryRLock() {
		r.Namespace.RUnlock()
		if time.Now().After(deadline) {
			close(resume)
			t.Fatal("writer not queued")
		}
		time.Sleep(time.Millisecond)
	}
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader deadlocked behind its own namespace lease")
	}
	select {
	case <-writer:
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not finish")
	}
}
func TestRemoteImagesAreDisclosedAsOfflinePartial(t *testing.T) {
	root, store, flow, id, _ := finalizedFixture(t)
	ctx := context.Background()
	if err := markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes += "\n![Remote](https://example.org/remote.png)"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final := finalizeFixture(t, flow, id, "remote-final")
	snap, err := store.CreateSnapshot(ctx, "finalized", SnapshotRequest{OperationID: "snapshot", ThoughtID: id, RevisionID: final.RevisionID, FinalizationID: final.FinalizationID})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Complete != "partial" || len(snap.ResourceIssues) != 1 {
		t.Fatal(snap)
	}
	c, err := store.Catalog(ctx, "finalized")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.WriteCatalog(ctx, "finalized", models.CatalogWriteRequest{OperationID: "bind", ExpectedGeneration: c.Generation, RequiredDeliveryLevel: "proposal", Groups: c.Groups, Entries: []models.CatalogEntry{{ID: "snapshot", GroupID: "default", Title: "Snapshot", Required: true, Binding: models.CatalogBinding{Kind: "snapshot", SnapshotID: snap.ID}}}}); err != nil {
		t.Fatal(err)
	}
	content, err := store.EntryContent(ctx, "finalized", "snapshot", "final", snap.RevisionID)
	if err != nil || content.OfflineStatus != "partial" || len(content.ResourceIssues) != 1 {
		t.Fatal(content, err)
	}
	exported, err := store.ExportPackage(ctx, "finalized", ExportRequest{OperationID: "delivery", Kind: "delivery"})
	if err != nil {
		t.Fatal(err)
	}
	if exported.Manifest.OfflineStatus != "partial" || len(exported.Manifest.ResourceIssues) != 1 || !strings.Contains(exported.Manifest.ResourceIssues[0].Locator, "remote.png") {
		t.Fatal(exported.Manifest)
	}
}

func TestSnapshotSearchIsTopicOnlyAndTracksCatalogImmediately(t *testing.T) {
	root, store, flow, id, _ := finalizedFixture(t)
	ctx := context.Background()
	if err := markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes += "\nSnapshotUniqueNeedle"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final := finalizeFixture(t, flow, id, "search-final")
	snap, err := store.CreateSnapshot(ctx, "finalized", SnapshotRequest{OperationID: "snapshot", ThoughtID: id, RevisionID: final.RevisionID, FinalizationID: final.FinalizationID})
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.Catalog(ctx, "finalized")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.WriteCatalog(ctx, "finalized", models.CatalogWriteRequest{OperationID: "bind", ExpectedGeneration: c.Generation, Groups: c.Groups, Entries: []models.CatalogEntry{{ID: "snapshot", GroupID: "default", Title: "Snapshot", Binding: models.CatalogBinding{Kind: "snapshot", SnapshotID: snap.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = markdown.DeleteThought(root, id); err != nil {
		t.Fatal(err)
	}
	index, err := searchdb.Open(ctx, filepath.Join(t.TempDir(), "index.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	index.SetWorkspaceRoot(root)
	if _, err = index.ReindexWorkspace(ctx, root); err != nil {
		t.Fatal(err)
	}
	scoped := models.SearchQuery{Scope: "topic", TopicID: "finalized", Query: "SnapshotUniqueNeedle", Mode: "keyword"}
	found, err := index.Search(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	if found.Total != 1 || found.Items[0].SnapshotID != snap.ID || found.Items[0].EntryID != "snapshot" || found.Items[0].VersionKind != "snapshot" || found.Items[0].ThoughtID != "" || found.Items[0].IndexedRevisionID != snap.RevisionID {
		t.Fatal(found)
	}
	global, err := index.Search(ctx, models.SearchQuery{Scope: "global", Query: "SnapshotUniqueNeedle", Mode: "keyword"})
	if err != nil || global.Total != 0 {
		t.Fatal(global, err)
	}
	if _, err = store.WriteCatalog(ctx, "finalized", models.CatalogWriteRequest{OperationID: "unlink", ExpectedGeneration: bound.Catalog.Generation, Groups: c.Groups, Entries: []models.CatalogEntry{}}); err != nil {
		t.Fatal(err)
	}
	found, err = index.Search(ctx, scoped)
	if err != nil || found.Total != 0 {
		t.Fatal("unlinked snapshot lingered", found, err)
	}
}

func TestBackupRejectsJournalWithoutHeadOrProjectionProfile(t *testing.T) {
	root, store, _, id, _ := finalizedFixture(t)
	location, err := objectlocation.For(root).Resolve(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, location.Placement.DocumentRoot, "head.json")); err != nil {
		t.Fatal(err)
	}
	raw := "---\nid: " + id + "\n---\n\nExternal edit removed profile metadata.\n"
	if err = os.WriteFile(filepath.Join(root, location.Placement.ThoughtPath), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ExportPackage(context.Background(), "finalized", ExportRequest{OperationID: "no-head", Kind: "backup"}); err == nil {
		t.Fatal("formal journal silently exported as an ordinary note")
	}
}
