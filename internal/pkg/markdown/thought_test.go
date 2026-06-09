package markdown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestWriteAndReadThought(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC)
	thought := models.Thought{
		ID:            "20260609-143010-8f3a",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     "Test title",
		Path:          filepath.ToSlash(ThoughtRelativePath("20260609-143010-8f3a")),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("hello"),
		UserTags:      []string{"idea"},
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	content := models.ThoughtContent{Original: "hello"}

	if err := WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	gotThought, gotContent, err := ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if gotThought.ID != thought.ID {
		t.Fatalf("thought id = %q, want %q", gotThought.ID, thought.ID)
	}
	if gotThought.DisplayTitle != "Test title" {
		t.Fatalf("display title = %q", gotThought.DisplayTitle)
	}
	if gotContent.Original != "hello" {
		t.Fatalf("original = %q", gotContent.Original)
	}
}

func TestWriteThoughtPreservesUnknownFrontMatter(t *testing.T) {
	root := t.TempDir()
	thoughtID := "20260609-143010-8f3a"
	relPath := filepath.ToSlash(ThoughtRelativePath(thoughtID))
	targetPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	oldRaw := `---
id: "20260609-143010-8f3a"
type: "text"
source: "manual"
path: "thoughts/2026/06/20260609-143010-8f3a.md"
summary: "old summary"
embedding_ref: "duckdb:thought_embeddings/20260609-143010-8f3a"
external_meta:
  owner: "research"
  confidence: "draft"
review_flags:
  - "human-review"
errors: []
---

## Original

hello
`
	if err := os.WriteFile(targetPath, []byte(oldRaw), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	now := time.Date(2026, 6, 9, 15, 0, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          relPath,
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("hello updated"),
		Summary:       "new summary",
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusMatched,
	}
	if err := WriteThought(root, thought, models.ThoughtContent{Original: "hello updated"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(raw)
	for _, expected := range []string{
		`summary: "new summary"`,
		`embedding_ref: "duckdb:thought_embeddings/20260609-143010-8f3a"`,
		`external_meta:`,
		`  owner: "research"`,
		`review_flags:`,
		`  - "human-review"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("updated thought missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, `summary: "old summary"`) {
		t.Fatalf("known summary field was not replaced:\n%s", text)
	}
	if strings.Count(text, "errors: []") != 1 {
		t.Fatalf("known errors field should not be duplicated:\n%s", text)
	}
}
