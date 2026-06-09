package biz

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestCaptureDuplicateContentWarnsWithoutDropping(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.ThoughtsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), nil)
	command := models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Content: "Same note should produce a duplicate warning but still be saved.",
	}

	first, err := service.Capture(context.Background(), command)
	if err != nil {
		t.Fatalf("Capture(first) error = %v", err)
	}
	second, err := service.Capture(context.Background(), command)
	if err != nil {
		t.Fatalf("Capture(second) error = %v", err)
	}

	if first.Thought.ID == second.Thought.ID {
		t.Fatalf("duplicate capture should still create a new thought id")
	}
	if second.Thought.CaptureStatus != models.CaptureStatusDuplicateWarned {
		t.Fatalf("capture status = %q", second.Thought.CaptureStatus)
	}
	if len(second.Thought.Errors) != 1 ||
		second.Thought.Errors[0].Code != "thoughtflow.capture.duplicate_warned" ||
		!strings.Contains(second.Thought.Errors[0].Message, first.Thought.ID) {
		t.Fatalf("duplicate errors = %#v", second.Thought.Errors)
	}
	for _, thought := range []models.Thought{first.Thought, second.Thought} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(thought.Path))); err != nil {
			t.Fatalf("expected thought %s markdown file: %v", thought.ID, err)
		}
	}

	snapshot, err := service.GetThought(context.Background(), second.Thought.ID)
	if err != nil {
		t.Fatalf("GetThought(second) error = %v", err)
	}
	if snapshot.Thought.CaptureStatus != models.CaptureStatusDuplicateWarned || len(snapshot.Thought.Errors) != 1 {
		t.Fatalf("persisted duplicate warning = %#v", snapshot.Thought)
	}

	duplicates, err := service.FindDuplicatesByContentHash(context.Background(), first.Thought.ContentHash, second.Thought.ID)
	if err != nil {
		t.Fatalf("FindDuplicatesByContentHash() error = %v", err)
	}
	if len(duplicates) != 1 || duplicates[0].ID != first.Thought.ID {
		t.Fatalf("duplicates excluding current = %#v", duplicates)
	}
}

func TestListThoughtsReturnsWorkspaceThoughts(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.ThoughtsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), nil)

	first, err := service.Capture(context.Background(), models.CaptureCommand{Type: models.ThoughtTypeText, Content: "First listed thought"})
	if err != nil {
		t.Fatalf("Capture(first) error = %v", err)
	}
	second, err := service.Capture(context.Background(), models.CaptureCommand{Type: models.ThoughtTypeText, Content: "Second listed thought"})
	if err != nil {
		t.Fatalf("Capture(second) error = %v", err)
	}

	thoughts, err := service.ListThoughts(context.Background())
	if err != nil {
		t.Fatalf("ListThoughts() error = %v", err)
	}
	if len(thoughts) != 2 {
		t.Fatalf("thoughts = %#v", thoughts)
	}
	ids := map[string]bool{}
	for _, thought := range thoughts {
		ids[thought.ID] = true
	}
	if !ids[first.Thought.ID] || !ids[second.Thought.ID] {
		t.Fatalf("thought ids = %#v", ids)
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
