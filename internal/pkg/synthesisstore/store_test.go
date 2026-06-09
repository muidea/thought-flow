package synthesisstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestStoreSaveListGetAndMarkSaved(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx := context.Background()

	draft, err := store.SaveDraft(ctx, models.SynthesisDraft{
		ID:          "job-synthesis-test",
		ThoughtIDs:  []string{"20260609-143010-8f3a"},
		Goal:        "Research outline",
		Format:      "outline",
		Content:     "# Research outline\n\nDraft body.",
		SourceLinks: []string{"thoughts/2026/06/20260609-143010-8f3a.md"},
		Model:       "local-rule",
		CreatedAt:   time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("SaveDraft() error = %v", err)
	}
	if draft.Status != "draft" || draft.UpdatedAt.IsZero() || len(draft.History) != 1 {
		t.Fatalf("draft = %#v", draft)
	}
	path := filepath.Join(root, "synthesis", "drafts", "job-synthesis-test.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if !strings.Contains(string(raw), "status: draft") || !strings.Contains(string(raw), "draft created") {
		t.Fatalf("unexpected draft YAML:\n%s", string(raw))
	}

	drafts, err := store.ListDrafts(ctx)
	if err != nil {
		t.Fatalf("ListDrafts() error = %v", err)
	}
	if len(drafts) != 1 || drafts[0].ID != draft.ID {
		t.Fatalf("drafts = %#v", drafts)
	}
	loaded, err := store.GetDraft(ctx, draft.ID)
	if err != nil {
		t.Fatalf("GetDraft() error = %v", err)
	}
	if loaded.Goal != draft.Goal || loaded.Status != "draft" {
		t.Fatalf("loaded = %#v", loaded)
	}

	saved, err := store.MarkSaved(ctx, draft.ID, "# Research outline\n\nAccepted.", models.Thought{ID: "20260609-150000-saved"})
	if err != nil {
		t.Fatalf("MarkSaved() error = %v", err)
	}
	if saved.Status != "saved" || saved.SavedThoughtID != "20260609-150000-saved" || saved.SavedAt == nil {
		t.Fatalf("saved = %#v", saved)
	}
	if len(saved.History) != 2 || saved.History[1].Status != "saved" {
		t.Fatalf("history = %#v", saved.History)
	}
}
