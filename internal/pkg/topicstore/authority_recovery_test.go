package topicstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func TestAmbiguityPreservesAuthorityAndValidatesRecovery(t *testing.T) {
	for _, month := range []string{"09", "11"} {
		for _, damage := range []string{"none", "only-copy", "binding", "revision", "head"} {
			t.Run(month+"/"+damage, func(t *testing.T) {
				root, _, flow, id, _ := finalizedFixture(t)
				ctx := context.Background()
				r := objectlocation.For(root)
				l, err := r.Resolve(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				resource, err := r.UploadResource(ctx, "finalized", id, "attachment", "sample.txt", l.Generation, []byte("keep this attachment"))
				if err != nil {
					t.Fatal(err)
				}
				plan, err := r.PreviewMigration(ctx, id, "")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.ExecuteMigration(ctx, models.MigrationRequest{OperationID: "move-global", PreviewID: plan.ID, PlanHash: plan.PlanHash, ExpectedObjectGeneration: plan.Source.Generation}); err != nil {
					t.Fatal(err)
				}
				l, err = r.Resolve(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				l, err = r.SetSearchPolicy(id, models.SearchPolicyRequest{OperationID: "policy", ExpectedGeneration: l.Generation, GlobalSearch: "include"})
				if err != nil {
					t.Fatal(err)
				}
				// No backup snapshot can supply the binding metadata for this global object.
				if err = os.Remove(filepath.Join(root, "topics/finalized/storage.snapshot.json")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				original, err := os.ReadFile(filepath.Join(root, l.Placement.ThoughtPath))
				if err != nil {
					t.Fatal(err)
				}
				head, err := flow.Store.Head(id)
				if err != nil {
					t.Fatal(err)
				}
				revision, err := flow.Store.Revision(id, head.WorkingRevisionID)
				if err != nil {
					t.Fatal(err)
				}
				bindings, err := r.Bindings(id)
				if err != nil {
					t.Fatal(err)
				}
				if l.ResourceBindingsRef == nil || l.Generation <= 1 {
					t.Fatal(l)
				}
				copyPath := "thoughts/2026/" + month + "/" + id + ".md"
				if err = objectlocation.WriteFile(root, copyPath, original, 0644); err != nil {
					t.Fatal(err)
				}
				if err = r.Rebuild(); !errors.Is(err, objectlocation.ErrAmbiguous) {
					t.Fatal(err)
				}
				var diagnosed models.ObjectLocation
				if err = objectlocation.ReadJSON(root, objectlocation.RecordPath(id), &diagnosed); err != nil {
					t.Fatal(err)
				}
				expected := l
				expected.Lifecycle = "ambiguous"
				expected.Generation++
				if objectlocation.HashJSON(diagnosed) != objectlocation.HashJSON(expected) {
					t.Fatal("authority metadata replaced", diagnosed, l)
				}
				remove := copyPath
				if damage == "only-copy" {
					remove = l.Placement.ThoughtPath
				}
				if err = os.Remove(filepath.Join(root, remove)); err != nil {
					t.Fatal(err)
				}
				switch damage {
				case "binding":
					err = os.WriteFile(filepath.Join(root, l.ResourceBindingsRef.Path), []byte("{}"), 0644)
				case "revision":
					err = os.WriteFile(filepath.Join(root, l.Placement.DocumentRoot, "revisions", head.WorkingRevisionID, "body.md"), []byte("damaged"), 0644)
				case "head":
					err = os.Remove(filepath.Join(root, l.Placement.DocumentRoot, "head.json"))
				}
				if err != nil {
					t.Fatal(err)
				}
				recoveredErr := r.Rebuild()
				if damage != "none" {
					if recoveredErr == nil {
						t.Fatal("unsafe recovery accepted")
					}
					if _, err = r.Resolve(ctx, id); !errors.Is(err, objectlocation.ErrAmbiguous) {
						t.Fatal("object escaped isolation", err)
					}
					return
				}
				if recoveredErr != nil {
					t.Fatal(recoveredErr)
				}
				restored, err := r.Resolve(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				expected = l
				expected.Generation = diagnosed.Generation + 1
				expected.LastOperationID = "registry-rebuild"
				if objectlocation.HashJSON(restored) != objectlocation.HashJSON(expected) {
					t.Fatal("recovery lost authority metadata", restored, expected)
				}
				after, err := os.ReadFile(filepath.Join(root, l.Placement.ThoughtPath))
				if err != nil || string(after) != string(original) {
					t.Fatal("body changed", err)
				}
				afterHead, err := flow.Store.Head(id)
				if err != nil || afterHead != head {
					t.Fatal("HEAD changed", afterHead, err)
				}
				afterRevision, err := flow.Store.Revision(id, head.WorkingRevisionID)
				if err != nil || documentstore.HashJSON(afterRevision) != documentstore.HashJSON(revision) {
					t.Fatal("revision changed", err)
				}
				afterBindings, err := r.Bindings(id)
				if err != nil || objectlocation.HashJSON(afterBindings) != objectlocation.HashJSON(bindings) {
					t.Fatal("bindings changed", err)
				}
				_, raw, err := r.ReadResource(ctx, resource.ID, id)
				if err != nil || string(raw) != "keep this attachment" {
					t.Fatal("attachment unavailable", err)
				}
			})
		}
	}
}

func TestFirstImportRecoveryConflictContinuesOtherTasks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := New(root)
	packages := map[string][]byte{}
	for _, name := range []string{"Conflicting", "Healthy"} {
		topic, err := source.Create(ctx, models.TopicCreateRequest{OperationID: "create-" + name, Name: name, Mode: "collection"})
		if err != nil {
			t.Fatal(err)
		}
		exported, err := source.ExportPackage(ctx, topic.ID, ExportRequest{OperationID: "export-" + name, Kind: "backup"})
		if err != nil {
			t.Fatal(err)
		}
		packages[name], err = source.ExportBytes(topic.ID, exported.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	dest := t.TempDir()
	store := New(dest)
	r := objectlocation.For(dest)
	r.Fault = func(phase string) error {
		if phase == "import_validated" {
			return os.ErrClosed
		}
		return nil
	}
	for _, name := range []string{"Conflicting", "Healthy"} {
		if _, err := store.ImportPackage(ctx, "import-"+name, packages[name]); !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
	}
	r.Fault = nil
	topic, err := store.Create(ctx, models.TopicCreateRequest{OperationID: "foreign", Name: "Conflicting", Mode: "collection", Description: "foreign topic"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := []byte("User content must survive the FIRST startup.")
	path := "topics/" + topic.ID + "/index.md"
	if err = objectlocation.WriteFile(dest, path, foreign, 0644); err != nil {
		t.Fatal(err)
	}
	// No RetryImport before recovery: this is the first discovery of the conflict.
	restarted := New(dest)
	if err = restarted.RecoverImports(ctx); err != nil {
		t.Fatal("first recovery blocked startup", err)
	}
	op, err := r.Operation("import-Conflicting")
	if err != nil || op.State != "conflicted" || op.Error == "" {
		t.Fatal(op, err)
	}
	var state ImportResult
	if err = objectlocation.ReadJSON(dest, importBase(op.ID)+"/state.json", &state); err != nil || state.Phase != "conflicted" {
		t.Fatal(state, err)
	}
	if _, err = restarted.Get(ctx, "healthy"); err != nil {
		t.Fatal("subsequent import not recovered", err)
	}
	if err = restarted.RecoverCollections(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dest, path))
	if err != nil || string(raw) != string(foreign) {
		t.Fatal("foreign content changed", err)
	}
	id := "20261023-unrelated-recovery"
	if err = markdown.WriteThought(dest, models.Thought{ID: id}, models.ThoughtContent{AINotes: "still writable"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = markdown.ReadThought(dest, id); err != nil {
		t.Fatal(err)
	}
	if err = restarted.RecoverImports(ctx); err != nil {
		t.Fatal("second recovery", err)
	}
}

func TestImportRecoveryDoesNotIgnorePersistenceOrCorruption(t *testing.T) {
	for _, failure := range []string{"operation-write", "state-write", "conflict-not-durable", "corrupt-state"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store := New(root)
			topic, err := store.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Conflicting", Mode: "collection"})
			if err != nil {
				t.Fatal(err)
			}
			exported, err := store.ExportPackage(ctx, topic.ID, ExportRequest{OperationID: "export", Kind: "backup"})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := store.ExportBytes(topic.ID, exported.ID)
			if err != nil {
				t.Fatal(err)
			}
			dest := t.TempDir()
			r := objectlocation.For(dest)
			target := New(dest)
			r.Fault = func(phase string) error {
				if phase == "import_validated" {
					return os.ErrClosed
				}
				return nil
			}
			if _, err = target.ImportPackage(ctx, "import", raw); !errors.Is(err, os.ErrClosed) {
				t.Fatal(err)
			}
			r.Fault = nil
			if _, err = target.Create(ctx, models.TopicCreateRequest{OperationID: "foreign", Name: "Conflicting", Mode: "collection"}); err != nil {
				t.Fatal(err)
			}
			if failure == "operation-write" {
				path := filepath.Join(dest, ".content-registry/private/operations/import.json")
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if failure == "state-write" || failure == "conflict-not-durable" {
				r.Fault = func(phase string) error {
					if phase != "import_conflict_prepared" {
						return nil
					}
					if failure == "conflict-not-durable" {
						return objectlocation.ErrConflict
					}
					path := filepath.Join(dest, importBase("import"), "state.json")
					if err := os.Remove(path); err != nil {
						return err
					}
					return os.Mkdir(path, 0700)
				}
			} else {
				if err = os.WriteFile(filepath.Join(dest, importBase("import"), "state.json"), []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = New(dest).RecoverImports(ctx); err == nil {
				t.Fatal("unsafe startup failure ignored")
			}
		})
	}
}
