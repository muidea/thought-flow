package objectlocation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

func setup(t *testing.T) (string, *objectlocation.Registry) {
	t.Helper()
	root := t.TempDir()
	r := objectlocation.For(root)
	_, e := topicstore.New(root).Create(context.Background(), models.TopicCreateRequest{OperationID: "create-collection", Mode: "collection", Name: "Collection"})
	if e != nil {
		t.Fatal(e)
	}
	return root, r
}
func note(t *testing.T, root, id, owner string) {
	t.Helper()
	if e := markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: owner, UserTitle: "Title"}, models.ThoughtContent{AINotes: "immutable content"}); e != nil {
		t.Fatal(e)
	}
}
func TestMigrationCommitRecoveryAndSourceHashGuard(t *testing.T) {
	for _, phase := range []string{"migration_staged", "migration_promoted", "migration_committed", "migration_retired"} {
		t.Run(phase, func(t *testing.T) {
			root, r := setup(t)
			note(t, root, "20261023-note", "")
			source, e := r.Resolve(context.Background(), "20261023-note")
			if e != nil {
				t.Fatal(e)
			}
			if e = objectlocation.WriteFile(root, source.Placement.EvidenceRoot+"/canary.txt", []byte("private evidence"), 0600); e != nil {
				t.Fatal(e)
			}
			p, e := r.PreviewMigration(context.Background(), "20261023-note", "collection")
			if e != nil {
				t.Fatal(e)
			}
			before, _, e := markdown.ReadThought(root, p.ObjectID)
			if e != nil {
				t.Fatal(e)
			}
			r.Fault = func(actual string) error {
				if actual == phase {
					return errors.New("power loss")
				}
				return nil
			}
			req := models.MigrationRequest{OperationID: "move", PreviewID: p.ID, PlanHash: p.PlanHash, ExpectedObjectGeneration: p.Source.Generation}
			if _, e = r.ExecuteMigration(context.Background(), req); e == nil {
				t.Fatal("fault not reached")
			}
			r.Fault = nil
			if e = r.RecoverMigrations(); e != nil {
				t.Fatal(e)
			}
			l, e := r.Resolve(context.Background(), p.ObjectID)
			if e != nil || l.OwnerTopicID != "collection" {
				t.Fatal(l, e)
			}
			evidencePath := filepath.Join(root, l.Placement.EvidenceRoot)
			info, e := os.Stat(evidencePath)
			if e != nil {
				t.Fatal(e)
			}
			if info.Mode().Perm()&0077 != 0 {
				t.Fatalf("private evidence directory permissions = %o", info.Mode().Perm())
			}
			raw, e := os.ReadFile(filepath.Join(evidencePath, "canary.txt"))
			if e != nil || string(raw) != "private evidence" {
				t.Fatal("evidence changed during recovery", e)
			}
			after, content, e := markdown.ReadThought(root, p.ObjectID)
			if e != nil || after.ID != before.ID || content.AINotes != "immutable content" {
				t.Fatal(after, content, e)
			}
			if _, e = os.Stat(filepath.Join(root, p.Source.Placement.ThoughtPath)); !os.IsNotExist(e) {
				t.Fatal("source still present", e)
			}
			if _, e = r.ExecuteMigration(context.Background(), req); e != nil {
				t.Fatal("replay", e)
			}
		})
	}
	root, r := setup(t)
	note(t, root, "20261023-edit", "")
	p, e := r.PreviewMigration(context.Background(), "20261023-edit", "collection")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, p.Source.Placement.ThoughtPath), []byte("external edit"), 0644); e != nil {
		t.Fatal(e)
	}
	_, e = r.ExecuteMigration(context.Background(), models.MigrationRequest{OperationID: "stale-move", PreviewID: p.ID, PlanHash: p.PlanHash, ExpectedObjectGeneration: p.Source.Generation})
	if !errors.Is(e, objectlocation.ErrConflict) {
		t.Fatal("stale plan accepted", e)
	}
}
func TestOwnerSearchPolicyAndContextualResources(t *testing.T) {
	root, r := setup(t)
	note(t, root, "20261023-owned", "collection")
	l, e := r.Resolve(context.Background(), "20261023-owned")
	if e != nil {
		t.Fatal(e)
	}
	tstore := topicstore.New(root)
	topic, e := tstore.Get(context.Background(), "collection")
	if e != nil {
		t.Fatal(e)
	}
	_, e = tstore.Settings(context.Background(), topic.ID, models.TopicSettingsRequest{OperationID: "hide", ExpectedGeneration: topic.Generation, Name: topic.Name, SearchPolicy: models.TopicSearchPolicy{TopicGlobalVisible: true, OwnedThoughtsGlobalDefault: false}})
	if e != nil {
		t.Fatal(e)
	}
	if r.GlobalEligible(l) {
		t.Fatal("owner policy ignored")
	}
	l, e = r.SetSearchPolicy(l.ObjectID, models.SearchPolicyRequest{OperationID: "include", ExpectedGeneration: l.Generation, GlobalSearch: "include"})
	if e != nil || !r.GlobalEligible(l) {
		t.Fatal(l, e)
	}
	resource, e := r.UploadResource(context.Background(), topic.ID, l.ObjectID, "upload", "note.txt", l.Generation, []byte("shareable text"))
	if e != nil {
		t.Fatal(e)
	}
	_, raw, e := r.ReadResource(context.Background(), resource.ID, l.ObjectID)
	if e != nil || string(raw) != "shareable text" {
		t.Fatal(e)
	}
	note(t, root, "20261023-other", "")
	if _, _, e = r.ReadResource(context.Background(), resource.ID, "20261023-other"); !os.IsNotExist(e) {
		t.Fatal("cross-object resource leaked", e)
	}
	if _, e = r.UploadResource(context.Background(), topic.ID, l.ObjectID, "bad-upload", "x.svg", l.Generation, []byte("<svg/>")); e == nil {
		t.Fatal("active content accepted")
	}
}
func TestSingleWriterAndLayoutGuard(t *testing.T) {
	root := t.TempDir()
	release, e := objectlocation.AcquireWriter(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if second, e := objectlocation.AcquireWriter(root); e == nil {
		second()
		t.Fatal("second writer accepted")
	}
	other := t.TempDir()
	if e = objectlocation.WriteJSON(other, ".content-registry/schema.json", map[string]int{"layout_version": 999, "minimum_writer_version": 999}, false); e != nil {
		t.Fatal(e)
	}
	if e = objectlocation.For(other).Initialize(); !errors.Is(e, objectlocation.ErrRecovery) {
		t.Fatal(e)
	}
}
func TestSymlinkAndTraversalFailClosed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "escape")); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"../outside", "escape/file", "a/../../escape", "/absolute", "a\\file"} {
		if _, e := objectlocation.SafePath(root, path); e == nil {
			t.Fatal(path)
		}
	}
}

func TestMigrationPreservesRelativeResourcesAndSharedOriginal(t *testing.T) {
	root, r := setup(t)
	id := "20261023-relative"
	note(t, root, id, "")
	l, e := r.Resolve(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	_ = l
	attachment := "attachments/shared.txt"
	if e = objectlocation.WriteFile(root, attachment, []byte("shared text"), 0644); e != nil {
		t.Fatal(e)
	}
	if e = markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes = "[attachment](../../../attachments/shared.txt)"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(filepath.Join(root, l.Placement.ThoughtPath))
	if e != nil {
		t.Fatal(e)
	}
	p, e := r.PreviewMigration(context.Background(), id, "collection")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.ExecuteMigration(context.Background(), models.MigrationRequest{OperationID: "move-relative", PreviewID: p.ID, PlanHash: p.PlanHash, ExpectedObjectGeneration: p.Source.Generation}); e != nil {
		t.Fatal(e)
	}
	l, e = r.Resolve(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(filepath.Join(root, l.Placement.ThoughtPath))
	if e != nil || string(after) != string(before) {
		t.Fatal("immutable body changed", e)
	}
	bindings, e := r.Bindings(id)
	if e != nil || len(bindings.Bindings) != 1 || bindings.Bindings[0].OriginalLocator != "../../../attachments/shared.txt" {
		t.Fatal(bindings, e)
	}
	_, raw, e := r.ReadResource(context.Background(), bindings.Bindings[0].ResourceID, id)
	if e != nil || string(raw) != "shared text" {
		t.Fatal(string(raw), e)
	}
	if _, e = os.Stat(filepath.Join(root, attachment)); e != nil {
		t.Fatal("shared original removed", e)
	}
}

func TestMigrationPermissionFailureLeavesSourceAndRebuildRecognizesUncommittedCopies(t *testing.T) {
	ctx := context.Background()
	root, r := setup(t)
	id := "20261023-permission"
	note(t, root, id, "")
	plan, e := r.PreviewMigration(ctx, id, "collection")
	if e != nil {
		t.Fatal(e)
	}
	// A real filesystem obstruction, independent of root's permission bypass.
	obstruction := filepath.Join(root, "topics", "collection", "thoughts")
	if e = os.WriteFile(obstruction, []byte("not a directory"), 0644); e != nil {
		t.Fatal(e)
	}
	req := models.MigrationRequest{OperationID: "permission", PreviewID: plan.ID, PlanHash: plan.PlanHash, ExpectedObjectGeneration: plan.Source.Generation}
	if _, e = r.ExecuteMigration(ctx, req); e == nil {
		t.Fatal("obstructed target accepted")
	}
	if l, e := r.Resolve(ctx, id); e != nil || l.OwnerTopicID != "" {
		t.Fatal("source authority changed", l, e)
	}
	if _, _, e = markdown.ReadThought(root, id); e != nil {
		t.Fatal("source unreadable", e)
	}
	if e = os.Remove(obstruction); e != nil {
		t.Fatal(e)
	}
	r.Fault = func(phase string) error {
		if phase == "migration_promoted" {
			return os.ErrClosed
		}
		return nil
	}
	if _, e = r.ExecuteMigration(ctx, req); !errors.Is(e, os.ErrClosed) {
		t.Fatal(e)
	}
	r.Fault = nil
	if e = r.Rebuild(); e != nil {
		t.Fatal("durable migration copies mistaken for independent originals", e)
	}
	if e = r.RecoverMigrations(); e != nil {
		t.Fatal(e)
	}
}

func TestIndependentDuplicateIdentityFailsClosed(t *testing.T) {
	root, r := setup(t)
	id := "20261023-duplicate"
	note(t, root, id, "")
	l, e := r.Resolve(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(root, l.Placement.ThoughtPath))
	if e != nil {
		t.Fatal(e)
	}
	if e = objectlocation.WriteFile(root, "thoughts/duplicate/"+id+".md", raw, 0644); e != nil {
		t.Fatal(e)
	}
	if e = r.Rebuild(); !errors.Is(e, objectlocation.ErrAmbiguous) {
		t.Fatal(e)
	}
	if _, e = r.Resolve(context.Background(), id); !errors.Is(e, objectlocation.ErrAmbiguous) {
		t.Fatal("duplicate authority accepted", e)
	}
}

func TestRegistryRepairCannotWidenLostSearchPolicy(t *testing.T) {
	root, r := setup(t)
	id := "20261023-lost-policy"
	note(t, root, id, "")
	l, e := r.Resolve(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.SetSearchPolicy(id, models.SearchPolicyRequest{OperationID: "exclude", ExpectedGeneration: l.Generation, GlobalSearch: "exclude"}); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, objectlocation.RecordPath(id))); e != nil {
		t.Fatal(e)
	}
	if e = r.Rebuild(); e != nil {
		t.Fatal(e)
	}
	restored, e := r.Resolve(context.Background(), id)
	if e != nil || r.GlobalEligible(restored) {
		t.Fatal("repair widened unknown policy", restored, e)
	}
}

// A paused private copy must not retain namespace/object locks. Writes made
// during that interval are permitted, but must invalidate a stale commit.
func TestMigrationCopyAllowsWritesAndRevalidatesBeforeCommit(t *testing.T) {
	for _, change := range []string{"unrelated", "source", "journal", "policy"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			root, r := setup(t)
			id := "20261023-copy-source"
			note(t, root, id, "")
			note(t, root, "20261023-unrelated", "")
			plan, err := r.PreviewMigration(ctx, id, "collection")
			if err != nil {
				t.Fatal(err)
			}
			staged, resume := make(chan struct{}), make(chan struct{})
			r.Fault = func(phase string) error {
				if phase == "migration_staged" {
					close(staged)
					<-resume
				}
				return nil
			}
			done := make(chan error, 1)
			go func() {
				_, err := r.ExecuteMigration(ctx, models.MigrationRequest{OperationID: "copy-with-writes", PreviewID: plan.ID, PlanHash: plan.PlanHash, ExpectedObjectGeneration: plan.Source.Generation})
				done <- err
			}()
			select {
			case <-staged:
			case err := <-done:
				t.Fatalf("migration stopped before staging: %v", err)
			case <-time.After(5 * time.Second):
				close(resume)
				t.Fatal("migration did not reach staging")
			}
			changed := make(chan error, 1)
			go func() {
				var err error
				switch change {
				case "unrelated", "source":
					target := id
					if change == "unrelated" {
						target = "20261023-unrelated"
					}
					err = markdown.UpdateThought(root, target, func(_ *models.Thought, content *models.ThoughtContent) error {
						content.AINotes = "new content while migration stages"
						return nil
					})
				case "journal":
					release := r.ReadLease(id)
					err = objectlocation.WriteFile(root, plan.Source.Placement.DocumentRoot+"/reports/new.json", []byte("{}"), 0644)
					release()
				case "policy":
					store := topicstore.New(root)
					topic, e := store.Get(ctx, "collection")
					if e != nil {
						changed <- e
						return
					}
					_, err = store.Settings(ctx, topic.ID, models.TopicSettingsRequest{OperationID: "change-during-copy", ExpectedGeneration: topic.Generation, Name: topic.Name, SearchPolicy: models.TopicSearchPolicy{TopicGlobalVisible: true, OwnedThoughtsGlobalDefault: false}})
				}
				changed <- err
			}()
			var mutationErr error
			blocked := false
			select {
			case mutationErr = <-changed:
			case <-time.After(5 * time.Second):
				blocked = true
			}
			close(resume)
			migrationErr := <-done
			r.Fault = nil
			if blocked {
				<-changed
				t.Fatal("staging blocked a normal write")
			}
			if mutationErr != nil {
				t.Fatal(mutationErr)
			}
			if change == "unrelated" {
				if migrationErr != nil {
					t.Fatal(migrationErr)
				}
			} else {
				if !errors.Is(migrationErr, objectlocation.ErrConflict) {
					t.Fatalf("stale migration accepted: %v", migrationErr)
				}
				location, err := r.Resolve(ctx, id)
				if err != nil || location.OwnerTopicID != "" {
					t.Fatalf("authority changed: %+v %v", location, err)
				}
			}
		})
	}
}
