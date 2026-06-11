package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/thoughtlock"
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

// patchServiceForTest wires a capture service against a temp workspace
// and pre-populates one thought so the PATCH tests have a target.
func patchServiceForTest(t *testing.T) (*Service, models.ThoughtSnapshot) {
	t.Helper()
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
	t.Cleanup(func() { hub.Terminate(context.Background()) })
	locker := thoughtlock.New(5 * time.Second)
	service := NewService(ws, jobstore.New(ws.JobsPath), hub, WithLocker(locker))
	result, err := service.Capture(context.Background(), models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Content: "Patch me",
		Title:   "Original",
		Tags:    []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	snapshot, err := service.GetThought(context.Background(), result.Thought.ID)
	if err != nil {
		t.Fatalf("GetThought() error = %v", err)
	}
	return service, snapshot
}

func TestPatchThought_TitleUpdatesUserTitlePreservesExtractedTitle(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	newTitle := "Renamed title"
	req := models.ThoughtPatchRequest{Title: &newTitle}
	rawBody := []byte(`{"title":"Renamed title"}`)
	updated, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", req, rawBody)
	if err != nil {
		t.Fatalf("PatchThought() error = %v", err)
	}
	if updated.Thought.UserTitle != "Renamed title" {
		t.Fatalf("UserTitle = %q, want %q", updated.Thought.UserTitle, "Renamed title")
	}
	if updated.Thought.ExtractedTitle == "Renamed title" {
		t.Fatalf("ExtractedTitle should not be overwritten by PATCH title")
	}
}

func TestPatchThought_RejectsUnknownField(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	rawBody := []byte(`{"bogus_field":"value"}`)
	_, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", models.ThoughtPatchRequest{}, rawBody)
	if !errors.Is(err, ErrInvalidPatchField) {
		t.Fatalf("expected ErrInvalidPatchField, got %v", err)
	}
}

func TestPatchThought_RejectsEmptyTitle(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	empty := "   "
	req := models.ThoughtPatchRequest{Title: &empty}
	rawBody := []byte(`{"title":"   "}`)
	_, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", req, rawBody)
	if err == nil {
		t.Fatalf("expected error for empty title")
	}
}

func TestPatchThought_AppendsAINotesTimestamped(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	note := "Captured an additional point."
	req := models.ThoughtPatchRequest{AINotesAppend: &note}
	rawBody := []byte(`{"ai_notes_append":"Captured an additional point."}`)
	updated, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", req, rawBody)
	if err != nil {
		t.Fatalf("PatchThought() error = %v", err)
	}
	if !strings.Contains(updated.Content.AINotes, "Captured an additional point.") {
		t.Fatalf("AINotes missing new paragraph: %q", updated.Content.AINotes)
	}
	if !strings.Contains(updated.Content.AINotes, "## AI Notes") {
		t.Fatalf("expected AI Notes section header, got %q", updated.Content.AINotes)
	}
}

func TestPatchThought_ReplacesTags(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	tags := []string{"c", "b", "a", ""}
	req := models.ThoughtPatchRequest{Tags: &tags}
	rawBody := []byte(`{"tags":["c","b","a",""]}`)
	updated, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", req, rawBody)
	if err != nil {
		t.Fatalf("PatchThought() error = %v", err)
	}
	// Empty entries are dropped, the rest are sorted.
	want := []string{"a", "b", "c"}
	if len(updated.Thought.UserTags) != len(want) {
		t.Fatalf("UserTags = %v, want %v", updated.Thought.UserTags, want)
	}
	for idx, tag := range want {
		if updated.Thought.UserTags[idx] != tag {
			t.Fatalf("UserTags[%d] = %q, want %q", idx, updated.Thought.UserTags[idx], tag)
		}
	}
}

func TestPatchThought_ReplacesTopicIDs(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	ids := []string{"topic-2", "topic-1"}
	req := models.ThoughtPatchRequest{TopicIDs: &ids}
	rawBody := []byte(`{"topic_ids":["topic-2","topic-1"]}`)
	updated, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", req, rawBody)
	if err != nil {
		t.Fatalf("PatchThought() error = %v", err)
	}
	if len(updated.Thought.TopicIDs) != 2 || updated.Thought.TopicIDs[0] != "topic-2" || updated.Thought.TopicIDs[1] != "topic-1" {
		t.Fatalf("TopicIDs = %v", updated.Thought.TopicIDs)
	}
}

func TestPatchThought_RequiresSessionID(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	_, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "", models.ThoughtPatchRequest{}, nil)
	if err == nil {
		t.Fatalf("expected error for missing session id")
	}
}

func TestPatchThought_LockedByOtherSession(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	if err := service.locker.Acquire(snapshot.Thought.ID, "session-other"); err != nil {
		t.Fatalf("setup Acquire: %v", err)
	}
	defer service.locker.Release(snapshot.Thought.ID, "session-other")
	rawBody := []byte(`{}`)
	_, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", models.ThoughtPatchRequest{}, rawBody)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}

func TestPatchThought_ReentrantSameSession(t *testing.T) {
	service, snapshot := patchServiceForTest(t)
	if err := service.locker.Acquire(snapshot.Thought.ID, "session-A"); err != nil {
		t.Fatalf("setup Acquire: %v", err)
	}
	defer service.locker.Release(snapshot.Thought.ID, "session-A")
	newTitle := "Re-entered"
	rawBody := []byte(`{"title":"Re-entered"}`)
	if _, err := service.PatchThought(context.Background(), snapshot.Thought.ID, "session-A", models.ThoughtPatchRequest{Title: &newTitle}, rawBody); err != nil {
		t.Fatalf("expected re-entrant PatchThought to succeed, got %v", err)
	}
}

func TestPatchThought_NotFound(t *testing.T) {
	service, _ := patchServiceForTest(t)
	rawBody := []byte(`{}`)
	_, err := service.PatchThought(context.Background(), "missing-id", "session-A", models.ThoughtPatchRequest{}, rawBody)
	if err == nil {
		t.Fatalf("expected error for missing thought")
	}
}
