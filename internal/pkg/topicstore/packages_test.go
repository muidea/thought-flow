package topicstore

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func TestBackupRoundTripPreservesOwnerCatalogAndPrivateBoundary(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	topic, e := store.Create(ctx, models.TopicCreateRequest{OperationID: "create-backup", Name: "Backup", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	id := "20261023-backup"
	if e = markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: topic.ID, UserTitle: "Original"}, models.ThoughtContent{AINotes: "Independent original"}); e != nil {
		t.Fatal(e)
	}
	c, e := store.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.WriteCatalog(ctx, topic.ID, models.CatalogWriteRequest{OperationID: "catalog-backup", ExpectedGeneration: c.Generation, Groups: c.Groups, Entries: []models.CatalogEntry{{ID: "entry", GroupID: "default", Title: "Entry", Required: true, Binding: models.CatalogBinding{Kind: "thought", ThoughtID: id, VersionPolicy: "latest_finalized"}}}})
	if e != nil {
		t.Fatal(e)
	}
	l, e := objectlocation.For(root).Resolve(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if e = objectlocation.WriteFile(root, l.Placement.EvidenceRoot+"/canary.txt", []byte("PRIVATE-CANARY"), 0600); e != nil {
		t.Fatal(e)
	}
	resource, e := objectlocation.For(root).UploadResource(ctx, topic.ID, id, "upload-backup", "file.txt", l.Generation, []byte("shared resource"))
	if e != nil {
		t.Fatal(e)
	}
	result, e := store.ExportPackage(ctx, topic.ID, ExportRequest{OperationID: "backup", Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := store.ExportBytes(topic.ID, result.ID)
	if e != nil {
		t.Fatal(e)
	}
	manifest, files, e := ReadPackage(raw)
	if e != nil {
		t.Fatal(e)
	}
	for path, data := range files {
		if bytes.Contains(data, []byte("PRIVATE-CANARY")) || bytes.Contains([]byte(path), []byte(".private")) {
			t.Fatal("private canary included", path)
		}
	}
	if manifest.Private {
		t.Fatal("backup incorrectly marked private")
	}
	dest := t.TempDir()
	restored, e := New(dest).ImportPackage(ctx, "restore", raw)
	if e != nil {
		t.Fatal(e)
	}
	if restored.Phase != "completed" {
		t.Fatal(restored)
	}
	thought, content, e := markdown.ReadThought(dest, id)
	if e != nil || thought.OwnerTopicID != topic.ID || content.AINotes != "Independent original" {
		t.Fatal(thought, content, e)
	}
	restoredView, e := New(dest).CatalogView(ctx, topic.ID)
	if e != nil || len(restoredView.Entries) != 1 || restoredView.Entries[0].Entry.ID != "entry" {
		t.Fatal(restoredView, e)
	}
	_, data, e := objectlocation.For(dest).ReadResource(ctx, resource.ID, id)
	if e != nil || string(data) != "shared resource" {
		t.Fatal(string(data), e)
	}
	if _, e = New(dest).ImportPackage(ctx, "restore-again", raw); e == nil {
		t.Fatal("ID conflict accepted")
	}
	if _, e = New(dest).ImportPackage(ctx, "restore", raw); e != nil {
		t.Fatal("idempotent replay", e)
	}
	if e = os.Remove(filepath.Join(dest, objectlocation.RecordPath(id))); e != nil {
		t.Fatal(e)
	}
	if e = objectlocation.For(dest).Rebuild(); e != nil {
		t.Fatal(e)
	}
	if _, _, e = markdown.ReadThought(dest, id); e != nil {
		t.Fatal("locator rebuild", e)
	}
}
func TestPackageRejectsTraversalSymlinksAndExtraFiles(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "topics/t/../../outside", "valid/file"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			w, e := zw.Create(name)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = w.Write([]byte("bad"))
			zw.Close()
			if _, _, e := ReadPackage(buf.Bytes()); e == nil {
				t.Fatal("unmanifested or unsafe file accepted")
			}
		})
	}
}

func TestImportCrashPhasesRemainInvisibleAndRecover(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := New(root)
	topic, e := s.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Recover", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	id := "20261023-import-recovery"
	if e = markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: topic.ID}, models.ThoughtContent{AINotes: "durable source"}); e != nil {
		t.Fatal(e)
	}
	result, e := s.ExportPackage(ctx, topic.ID, ExportRequest{OperationID: "export", Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.ExportBytes(topic.ID, result.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, phase := range []string{"import_validated", "import_reserved", "import_copied", "import_activated"} {
		t.Run(phase, func(t *testing.T) {
			dest := t.TempDir()
			registry := objectlocation.For(dest)
			registry.Fault = func(point string) error {
				if point == phase {
					return os.ErrClosed
				}
				return nil
			}
			if _, e := New(dest).ImportPackage(ctx, "import", raw); !errors.Is(e, os.ErrClosed) {
				t.Fatal("missing injected interruption", e)
			}
			if _, e := registry.Resolve(ctx, id); !os.IsNotExist(e) {
				t.Fatal("partial object became visible", e)
			}
			if _, e := New(dest).Get(ctx, topic.ID); !os.IsNotExist(e) {
				t.Fatal("partial topic became visible", e)
			}
			registry.Fault = nil
			if e := New(dest).RecoverImports(ctx); e != nil {
				t.Fatal(e)
			}
			_, body, e := markdown.ReadThought(dest, id)
			if e != nil || body.AINotes != "durable source" {
				t.Fatal(body, e)
			}
			if e = markdown.UpdateThought(dest, id, func(_ *models.Thought, c *models.ThoughtContent) error {
				c.AINotes = "edited after recovery"
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			if _, e = New(dest).ImportPackage(ctx, "import", raw); e != nil {
				t.Fatal(e)
			}
			_, body, e = markdown.ReadThought(dest, id)
			if e != nil || body.AINotes != "edited after recovery" {
				t.Fatal("replay overwrote live edit", body, e)
			}
		})
	}
}

func TestSnapshotAssetsSurviveOfflineDeliveryAndBackup(t *testing.T) {
	root, s, flow, id, _ := finalizedFixture(t)
	ctx := context.Background()
	r := objectlocation.For(root)
	l, e := r.Resolve(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	resource, e := r.UploadResource(ctx, "finalized", id, "upload", "attachment.txt", l.Generation, []byte("Offline attachment bytes"))
	if e != nil {
		t.Fatal(e)
	}
	if e = markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes += "\n\n[Attachment](resource://" + resource.ID + ")"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	final := finalizeFixture(t, flow, id, "attachment-final")
	snap, e := s.CreateSnapshot(ctx, "finalized", SnapshotRequest{OperationID: "copy", ThoughtID: id, RevisionID: final.RevisionID, FinalizationID: final.FinalizationID})
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.Catalog(ctx, "finalized")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.WriteCatalog(ctx, "finalized", models.CatalogWriteRequest{OperationID: "bind-copy", ExpectedGeneration: c.Generation, Groups: c.Groups, RequiredDeliveryLevel: "proposal", Entries: []models.CatalogEntry{{ID: "snapshot", GroupID: "default", Title: "Independent snapshot", Required: true, Binding: models.CatalogBinding{Kind: "snapshot", SnapshotID: snap.ID}}}})
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"delivery", "backup"} {
		exported, e := s.ExportPackage(ctx, "finalized", ExportRequest{OperationID: kind, Kind: kind})
		if e != nil {
			t.Fatal(kind, e)
		}
		raw, e := s.ExportBytes("finalized", exported.ID)
		if e != nil {
			t.Fatal(e)
		}
		zr, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if e != nil {
			t.Fatal(e)
		}
		contents := map[string][]byte{}
		for _, f := range zr.File {
			rc, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(rc)
			rc.Close()
			if e != nil {
				t.Fatal(e)
			}
			contents[f.Name] = data
		}
		if kind == "delivery" {
			if !bytes.Contains(contents["index.html"], []byte("Independent snapshot")) {
				t.Fatal("no offline viewer")
			}
			if !bytes.Contains(contents["documents/snapshot.html"], []byte("../assets/"+resource.ID)) {
				t.Fatal("offline link unresolved")
			}
			if string(contents["assets/"+resource.ID+".txt"]) != "Offline attachment bytes" {
				t.Fatal("offline attachment missing")
			}
		} else {
			dest := t.TempDir()
			if _, e = New(dest).ImportPackage(ctx, "restore-snapshot", raw); e != nil {
				t.Fatal(e)
			}
			data, _, e := New(dest).ReadSnapshotResource(ctx, "finalized", snap.ID, resource.ID)
			if e != nil || string(data) != "Offline attachment bytes" {
				t.Fatal(string(data), e)
			}
		}
	}
}

func TestHighlyRepetitiveBackupCanBeReadByItsImporter(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := New(root)
	topic, e := s.Create(ctx, models.TopicCreateRequest{OperationID: "repeat-create", Name: "Repetitive", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	if e = markdown.WriteThought(root, models.Thought{ID: "20261023-repeat", OwnerTopicID: topic.ID}, models.ThoughtContent{AINotes: strings.Repeat("Repeated ordinary notes and whitespace.\n", 10000)}); e != nil {
		t.Fatal(e)
	}
	result, e := s.ExportPackage(ctx, topic.ID, ExportRequest{OperationID: "repeat-export", Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.ExportBytes(topic.ID, result.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = New(t.TempDir()).ImportPackage(ctx, "repeat-import", raw); e != nil {
		t.Fatal("exporter produced an invalid backup", e)
	}
}

func TestPendingImportCannotExposeFinalizedDocumentThroughExport(t *testing.T) {
	_, s, flow, id, final := finalizedFixture(t)
	ctx := context.Background()
	before, e := flow.Export(id, final.RevisionID, "final")
	if e != nil {
		t.Fatal(e)
	}
	exported, e := s.ExportPackage(ctx, "finalized", ExportRequest{OperationID: "pending-export", Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.ExportBytes("finalized", exported.ID)
	if e != nil {
		t.Fatal(e)
	}
	dest := t.TempDir()
	r := objectlocation.For(dest)
	r.Fault = func(phase string) error {
		if phase == "import_activated" {
			return os.ErrClosed
		}
		return nil
	}
	if _, e = New(dest).ImportPackage(ctx, "pending-import", raw); !errors.Is(e, os.ErrClosed) {
		t.Fatal(e)
	}
	restoredFlow := documentflow.For(dest)
	t.Cleanup(restoredFlow.Close)
	if _, e = restoredFlow.Export(id, final.RevisionID, "final"); !os.IsNotExist(e) {
		t.Fatal("pending final exposed", e)
	}
	r.Fault = nil
	if e = New(dest).RecoverImports(ctx); e != nil {
		t.Fatal(e)
	}
	after, e := restoredFlow.Export(id, final.RevisionID, "final")
	if e != nil || after != before {
		t.Fatal("completed import unavailable", e)
	}
}
