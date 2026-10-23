package objectlocation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/workspace"
)

func TestStaleMigrationIsTerminalAndDoesNotBlockRestartOrNewPlan(t *testing.T) {
	ctx := context.Background()
	for _, phase := range []string{"before-copy", "migration_staged", "migration_promoted"} {
		t.Run(phase, func(t *testing.T) {
			root, r := setup(t)
			id := "20261023-stale-plan"
			note(t, root, id, "")
			note(t, root, "20261023-unrelated", "")
			preview, err := r.PreviewMigration(ctx, id, "collection")
			if err != nil {
				t.Fatal(err)
			}
			req := models.MigrationRequest{OperationID: "stale", PreviewID: preview.ID, PlanHash: preview.PlanHash, ExpectedObjectGeneration: preview.Source.Generation}
			if phase != "before-copy" {
				r.Fault = func(point string) error {
					if point == phase {
						return os.ErrClosed
					}
					return nil
				}
				if _, err = r.ExecuteMigration(ctx, req); !errors.Is(err, os.ErrClosed) {
					t.Fatal(err)
				}
				r.Fault = nil
			}
			if err = markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error { c.AINotes = "updated source"; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err = r.ExecuteMigration(ctx, req); !errors.Is(err, objectlocation.ErrConflict) {
				t.Fatal(err)
			}
			restarted := &objectlocation.Registry{Root: root}
			if err = restarted.Initialize(); err != nil {
				t.Fatal(err)
			}
			if err = restarted.RecoverMigrations(); err != nil {
				t.Fatal("normal conflict blocked startup", err)
			}
			if _, body, err := markdown.ReadThought(root, id); err != nil || body.AINotes != "updated source" {
				t.Fatal(body, err)
			}
			if err = markdown.UpdateThought(root, "20261023-unrelated", func(_ *models.Thought, c *models.ThoughtContent) error { c.AINotes = "still writable"; return nil }); err != nil {
				t.Fatal(err)
			}
			op, err := r.Operation("stale")
			if err != nil || op.State != "conflicted" {
				t.Fatal(op, err)
			}
			if _, err = r.RetryMigration("stale"); !errors.Is(err, objectlocation.ErrConflict) {
				t.Fatal(err)
			}
			next, err := r.PreviewMigration(ctx, id, "collection")
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Conflicts) > 0 {
				t.Fatal(next.Conflicts)
			}
			if _, err = r.ExecuteMigration(ctx, models.MigrationRequest{OperationID: "fresh", PreviewID: next.ID, PlanHash: next.PlanHash, ExpectedObjectGeneration: next.Source.Generation}); err != nil {
				t.Fatal(err)
			}
			if err = restarted.RecoverMigrations(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRelativeDiscoveryCannotPromoteRuntimeCanaries(t *testing.T) {
	for _, runtime := range []string{"runtime", ".thoughtflow", "attachments/runtime"} {
		t.Run(runtime, func(t *testing.T) {
			ctx := context.Background()
			root, r := setup(t)
			_, err := workspace.Open(ctx, appconfig.Config{Workspace: appconfig.WorkspaceConfig{ContentDir: root}, Runtime: appconfig.RuntimeConfig{StateDir: filepath.Join(root, runtime)}})
			if err != nil {
				t.Fatal(err)
			}
			id := "20261023-private"
			note(t, root, id, "")
			secret := runtime + "/document-evidence/" + id + "/hash.txt"
			if err = objectlocation.WriteFile(root, secret, []byte("PRIVATE-CANARY"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
				c.AINotes = "[private](../../../" + secret + ")"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err = r.PreviewMigration(ctx, id, "collection"); !errors.Is(err, objectlocation.ErrUnsafe) {
				t.Fatal("private source promoted", err)
			}
			for _, path := range markdown.GitPaths(root, id) {
				if strings.Contains(path, "hash.txt") {
					t.Fatal("private source entered Git", path)
				}
			}
			bindings, err := r.Bindings(id)
			if err != nil || len(bindings.Bindings) != 0 {
				t.Fatal(bindings, err)
			}
			if _, _, err = r.ReadResource(ctx, "res-canary", id); err == nil {
				t.Fatal("private source exposed")
			}
			source, err := os.ReadFile(filepath.Join(root, secret))
			if err != nil || string(source) != "PRIVATE-CANARY" {
				t.Fatal(err)
			}
		})
	}
}
func TestAttachmentsCanMigrateBothWaysAndBeShared(t *testing.T) {
	ctx := context.Background()
	root, r := setup(t)
	if err := objectlocation.WriteFile(root, "attachments/shared.txt", []byte("shared bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"20261023-shared-a", "20261023-shared-b"} {
		note(t, root, id, "")
		if err := markdown.UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
			c.AINotes = "[shared](../../../attachments/shared.txt)"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for i, target := range []string{"collection", "", "collection"} {
			plan, err := r.PreviewMigration(ctx, id, target)
			if err != nil || len(plan.Conflicts) > 0 {
				t.Fatal(plan.Conflicts, err)
			}
			if _, err = r.ExecuteMigration(ctx, models.MigrationRequest{OperationID: id + string(rune('a'+i)), PreviewID: plan.ID, PlanHash: plan.PlanHash, ExpectedObjectGeneration: plan.Source.Generation}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestRebuildClearsResolvedAmbiguity(t *testing.T) {
	root, r := setup(t)
	id := "20261023-ambiguous"
	note(t, root, id, "")
	original, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, original.Placement.ThoughtPath))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := "topics/collection/thoughts/" + id + ".md"
	if err = objectlocation.WriteFile(root, duplicate, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err = r.Rebuild(); !errors.Is(err, objectlocation.ErrAmbiguous) {
		t.Fatal(err)
	}
	ambiguous, err := r.Lookup(id)
	if !errors.Is(err, objectlocation.ErrAmbiguous) {
		t.Fatal(err)
	}
	// Keep exactly the path recorded by the ambiguity diagnosis.
	remove := duplicate
	if ambiguous.Placement.ThoughtPath == duplicate {
		remove = original.Placement.ThoughtPath
	}
	if err = os.Remove(filepath.Join(root, remove)); err != nil {
		t.Fatal(err)
	}
	if err = r.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if location, err := r.Resolve(context.Background(), id); err != nil || location.Lifecycle != "active" {
		t.Fatal(location, err)
	}
	if _, _, err = markdown.ReadThought(root, id); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationConflictPreservesForeignIdenticalTarget(t *testing.T) {
	ctx := context.Background()
	root, r := setup(t)
	id := "20261023-foreign-target"
	note(t, root, id, "")
	plan, err := r.PreviewMigration(ctx, id, "collection")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, plan.Source.Placement.ThoughtPath))
	if err != nil {
		t.Fatal(err)
	}
	if err = objectlocation.WriteFile(root, plan.Target.Placement.ThoughtPath, raw, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = r.ExecuteMigration(ctx, models.MigrationRequest{OperationID: "foreign-target", PreviewID: plan.ID, PlanHash: plan.PlanHash, ExpectedObjectGeneration: plan.Source.Generation})
	if !errors.Is(err, objectlocation.ErrConflict) {
		t.Fatal(err)
	}
	if err = r.RecoverMigrations(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, plan.Target.Placement.ThoughtPath))
	if err != nil || string(after) != string(raw) {
		t.Fatalf("foreign target changed: %s %v", after, err)
	}
}

func TestInitializeCanRepairAmbiguityWithoutSchema(t *testing.T) {
	root, r := setup(t)
	id := "20261023-repair-initialize"
	note(t, root, id, "")
	location, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	location.Lifecycle = "ambiguous"
	if err = objectlocation.WriteJSON(root, objectlocation.RecordPath(id), location, false); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, ".content-registry/schema.json")); err != nil {
		t.Fatal(err)
	}
	restarted := &objectlocation.Registry{Root: root}
	done := make(chan error, 1)
	go func() { done <- restarted.Initialize() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initialize reentered registry mutex")
	}
	restored, err := restarted.Resolve(context.Background(), id)
	if err != nil || restored.Lifecycle != "active" {
		t.Fatal(restored, err)
	}
}
