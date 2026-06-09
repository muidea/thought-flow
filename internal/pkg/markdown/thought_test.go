package markdown

import (
	"path/filepath"
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
