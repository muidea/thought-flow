package biz

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
)

func TestCaptureTextCreatesAtomicMarkdown(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	hub := event.NewHub(16)
	defer hub.Terminate(context.Background())
	service := NewService(ws, jobstore.New(ws.JobsPath), hub)

	result, err := service.Capture(context.Background(), models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Content: "Remember to design from the source markdown first.",
		Title:   "Design note",
		Tags:    []string{"design", "design"},
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if result.Thought.ID == "" {
		t.Fatalf("expected thought id")
	}
	if result.Thought.CaptureStatus != models.CaptureStatusCaptured {
		t.Fatalf("capture status = %q", result.Thought.CaptureStatus)
	}
	if len(result.Thought.UserTags) != 1 || result.Thought.UserTags[0] != "design" {
		t.Fatalf("expected normalized tags, got %#v", result.Thought.UserTags)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(result.Thought.Path))); err != nil {
		t.Fatalf("expected markdown file: %v", err)
	}

	snapshot, err := service.GetThought(context.Background(), result.Thought.ID)
	if err != nil {
		t.Fatalf("GetThought() error = %v", err)
	}
	if snapshot.Content.Original != "Remember to design from the source markdown first." {
		t.Fatalf("original content = %q", snapshot.Content.Original)
	}
}

func TestCaptureRejectsInvalidURL(t *testing.T) {
	service := NewService(&models.Workspace{RootPath: t.TempDir()}, jobstore.New(t.TempDir()), nil)
	_, err := service.Capture(context.Background(), models.CaptureCommand{
		Type: models.ThoughtTypeURL,
		URL:  "not-a-url",
	})
	if err == nil {
		t.Fatalf("expected invalid url error")
	}
}
