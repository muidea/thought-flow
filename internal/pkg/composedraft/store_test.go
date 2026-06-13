package composedraft

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func makeDraft() models.ComposeDraft {
	return models.ComposeDraft{
		ID: "job-compose-test",
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-143010-8f3a", Title: "Seed thought"},
			{SourceType: models.ComposeSourceTypeSearchResult, SourceID: "search-1", Snippet: "matching snippet"},
		},
		Goal:        "Research outline",
		Format:      models.ComposeFormatOutline,
		Content:     "# Research outline\n\nDraft body.",
		SourceLinks: []string{"thoughts/2026/06/20260609-143010-8f3a.md"},
		Model:       "local-rule",
		CreatedAt:   time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC),
	}
}

func TestStoreSaveListGetAndMarkSaved(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx := context.Background()

	draft, err := store.SaveDraft(ctx, makeDraft())
	if err != nil {
		t.Fatalf("SaveDraft() error = %v", err)
	}
	if draft.Status != models.ComposeStatusDraft {
		t.Fatalf("status = %q, want draft", draft.Status)
	}
	if draft.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt zero")
	}
	if len(draft.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(draft.History))
	}

	path := filepath.Join(root, "compose", "drafts", "job-compose-test.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if !strings.Contains(string(raw), "status: draft") {
		t.Fatalf("yaml missing status: draft\n%s", string(raw))
	}
	if !strings.Contains(string(raw), "draft created") {
		t.Fatalf("yaml missing draft created history\n%s", string(raw))
	}
	if !strings.Contains(string(raw), "source_type: thought") {
		t.Fatalf("yaml missing thought source\n%s", string(raw))
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
	if loaded.Goal != draft.Goal || loaded.Format != models.ComposeFormatOutline {
		t.Fatalf("loaded = %#v", loaded)
	}
	if len(loaded.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(loaded.Sources))
	}

	saved, err := store.MarkSaved(ctx, draft.ID, "# Research outline\n\nAccepted.", models.Thought{ID: "20260609-150000-saved"})
	if err != nil {
		t.Fatalf("MarkSaved() error = %v", err)
	}
	if saved.Status != models.ComposeStatusSaved {
		t.Fatalf("status = %q, want saved", saved.Status)
	}
	if saved.SavedThoughtID != "20260609-150000-saved" || saved.SavedAt == nil {
		t.Fatalf("saved meta = %#v", saved)
	}
	if len(saved.History) != 2 || saved.History[1].Status != models.ComposeStatusSaved {
		t.Fatalf("history = %#v", saved.History)
	}
}

func TestStoreRejectsEmptyID(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	_, err := store.SaveDraft(context.Background(), models.ComposeDraft{Content: "x"})
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("err = %v, want id is required", err)
	}
}

func TestStoreRejectsEmptySources(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	_, err := store.SaveDraft(context.Background(), models.ComposeDraft{ID: "x", Content: "y"})
	if err == nil || !strings.Contains(err.Error(), "sources are required") {
		t.Fatalf("err = %v, want sources are required", err)
	}
}

func TestStoreRejectsEmptyContent(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	_, err := store.SaveDraft(context.Background(), models.ComposeDraft{
		ID:      "x",
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "a"}},
	})
	if err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("err = %v, want content is required", err)
	}
}

func TestStoreRejectsInvalidID(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	for _, id := range []string{"a/b", "a\\b", "../escape"} {
		_, err := store.GetDraft(context.Background(), id)
		if err == nil {
			t.Fatalf("GetDraft(%q) should error", id)
		}
	}
}

func TestStoreListEmptyDir(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	drafts, err := store.ListDrafts(context.Background())
	if err != nil {
		t.Fatalf("ListDrafts() error = %v", err)
	}
	if len(drafts) != 0 {
		t.Fatalf("len = %d, want 0", len(drafts))
	}
}

func TestStoreListSortsByUpdatedAtDescending(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	old := makeDraft()
	old.ID = "old"
	old.UpdatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.SaveDraft(context.Background(), old); err != nil {
		t.Fatalf("save old: %v", err)
	}
	newer := makeDraft()
	newer.ID = "newer"
	newer.UpdatedAt = time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	if _, err := store.SaveDraft(context.Background(), newer); err != nil {
		t.Fatalf("save newer: %v", err)
	}
	drafts, err := store.ListDrafts(context.Background())
	if err != nil {
		t.Fatalf("ListDrafts() error = %v", err)
	}
	if len(drafts) != 2 || drafts[0].ID != "newer" || drafts[1].ID != "old" {
		t.Fatalf("order = %v", []string{drafts[0].ID, drafts[1].ID})
	}
}

func TestStoreDeleteRemovesFile(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	draft := makeDraft()
	if _, err := store.SaveDraft(context.Background(), draft); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Delete(context.Background(), draft.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.GetDraft(context.Background(), draft.ID); err == nil {
		t.Fatalf("expected GetDraft to error after delete")
	}
	// Idempotent: deleting again is fine.
	if err := store.Delete(context.Background(), draft.ID); err != nil {
		t.Fatalf("Delete (idempotent): %v", err)
	}
}

func TestStoreNormalizesEmptyFormat(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	draft := makeDraft()
	draft.Format = ""
	out, err := store.SaveDraft(context.Background(), draft)
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if out.Format != models.ComposeFormatSummary {
		t.Fatalf("format = %q, want summary", out.Format)
	}
}
