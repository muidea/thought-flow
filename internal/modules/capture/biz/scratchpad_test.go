package biz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

// memoryScratchpad is the in-memory test double for the scratchpad
// store. It mirrors the production store's contract (Get returns
// zero-value on missing, Delete is idempotent, Save upserts) but
// skips the file system so tests run in microseconds.
type memoryScratchpad struct {
	mu    sync.Mutex
	items map[string]scratchpad.Scratchpad
}

func newMemoryScratchpad() *memoryScratchpad {
	return &memoryScratchpad{items: map[string]scratchpad.Scratchpad{}}
}

func (m *memoryScratchpad) Get(id string) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sp, ok := m.items[id]; ok {
		return sp, nil
	}
	return scratchpad.Scratchpad{SessionID: id}, nil
}

func (m *memoryScratchpad) Save(sp scratchpad.Scratchpad) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[sp.SessionID] = sp
	return sp, nil
}

func (m *memoryScratchpad) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *memoryScratchpad) MarkCommitted(id, thoughtID string) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp, ok := m.items[id]
	if !ok {
		return scratchpad.Scratchpad{}, errors.New("scratchpad not found")
	}
	now := time.Now().UTC()
	sp.CommittedThoughtID = thoughtID
	sp.CommittedAt = &now
	m.items[id] = sp
	return sp, nil
}

func (m *memoryScratchpad) Reset(id string) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp, ok := m.items[id]
	if !ok {
		return scratchpad.Scratchpad{}, errors.New("scratchpad not found")
	}
	sp.Content = ""
	sp.Messages = nil
	sp.Draft = scratchpad.Draft{}
	m.items[id] = sp
	return sp, nil
}

func TestScratchpadServiceAppendMessageAccumulatesContent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	if _, err := svc.AppendMessage("s1", "user", "first thought"); err != nil {
		t.Fatalf("first AppendMessage: %v", err)
	}
	if _, err := svc.AppendMessage("s1", "ai", "ok"); err != nil {
		t.Fatalf("second AppendMessage: %v", err)
	}
	if _, err := svc.AppendMessage("s1", "user", "more"); err != nil {
		t.Fatalf("third AppendMessage: %v", err)
	}
	sp, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sp.Content != "first thought\n\nmore" {
		t.Fatalf("content = %q, want %q", sp.Content, "first thought\n\nmore")
	}
	if len(sp.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(sp.Messages))
	}
	if sp.Messages[1].Role != "ai" {
		t.Fatalf("messages[1].Role = %q, want ai", sp.Messages[1].Role)
	}
}

func TestScratchpadServiceAppendMessageRejectsEmptyFields(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	if _, err := svc.AppendMessage("", "user", "hi"); err == nil {
		t.Fatalf("empty session id should error")
	}
	if _, err := svc.AppendMessage("s1", "user", "   "); err == nil {
		t.Fatalf("whitespace text should error")
	}
}

func TestScratchpadServiceAppendDraftMergesAndProjects(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	_, err := svc.AppendDraft("s1", scratchpad.Draft{
		TitleSet:    "renamed",
		TagsAdded:   []string{"ai", "draft"},
		TopicIDs:    []string{"topic-1"},
		RefineRequested: true,
	})
	if err != nil {
		t.Fatalf("AppendDraft: %v", err)
	}
	sp, _ := store.Get("s1")
	if sp.Title != "renamed" {
		t.Fatalf("Title = %q, want renamed", sp.Title)
	}
	if len(sp.Tags) != 2 || sp.Tags[0] != "ai" {
		t.Fatalf("Tags = %v, want [ai draft]", sp.Tags)
	}
	if len(sp.TopicHints) != 1 || sp.TopicHints[0] != "topic-1" {
		t.Fatalf("TopicHints = %v", sp.TopicHints)
	}
	if !sp.Draft.RefineRequested {
		t.Fatalf("RefineRequested not set")
	}
	if sp.Draft.TitleSet != "renamed" {
		t.Fatalf("Draft.TitleSet = %q", sp.Draft.TitleSet)
	}
}

func TestScratchpadServiceAppendDraftTagsAddedAndRemovedDedupe(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	// First round: add ai + draft.
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{TagsAdded: []string{"ai", "draft"}}); err != nil {
		t.Fatalf("AppendDraft add: %v", err)
	}
	// Second round: add ai again (idempotent) + remove draft.
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{
		TagsAdded:   []string{"ai", "extra"},
		TagsRemoved: []string{"draft"},
	}); err != nil {
		t.Fatalf("AppendDraft remove: %v", err)
	}
	sp, _ := store.Get("s1")
	want := []string{"ai", "extra"}
	if len(sp.Tags) != 2 || sp.Tags[0] != "ai" || sp.Tags[1] != "extra" {
		t.Fatalf("Tags = %v, want %v", sp.Tags, want)
	}
	if len(sp.Draft.TagsAdded) != 3 {
		// ai was added twice but union dedupes the persisted TagAdded
		// list at append time too — check the top-level Tags only here.
		t.Fatalf("Draft.TagsAdded len = %d, want 3 (union keeps first seen)", len(sp.Draft.TagsAdded))
	}
}

func TestScratchpadServiceAppendDraftNotesAppendToContent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "hi"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{NotesAppended: []string{"my note"}}); err != nil {
		t.Fatalf("AppendDraft: %v", err)
	}
	sp, _ := store.Get("s1")
	if sp.Content != "hi\n\nmy note" {
		t.Fatalf("Content = %q", sp.Content)
	}
	if len(sp.Draft.NotesAppended) != 1 {
		t.Fatalf("NotesAppended len = %d", len(sp.Draft.NotesAppended))
	}
}

func TestScratchpadServiceBuildCaptureCommandFlattens(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "hello world"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{
		TitleSet:  "My Title",
		TagsAdded: []string{"a"},
	}); err != nil {
		t.Fatalf("AppendDraft: %v", err)
	}
	sp, _ := store.Get("s1")
	cmd, err := svc.BuildCaptureCommand(sp)
	if err != nil {
		t.Fatalf("BuildCaptureCommand: %v", err)
	}
	if cmd.Content != "hello world" {
		t.Fatalf("Content = %q", cmd.Content)
	}
	if cmd.Title != "My Title" {
		t.Fatalf("Title = %q", cmd.Title)
	}
	if len(cmd.Tags) != 1 || cmd.Tags[0] != "a" {
		t.Fatalf("Tags = %v", cmd.Tags)
	}
	if cmd.Source != "scratchpad-commit" {
		t.Fatalf("Source = %q", cmd.Source)
	}
}

func TestScratchpadServiceBuildCaptureCommandInferURLType(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "see https://example.com for details"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp, _ := store.Get("s1")
	cmd, err := svc.BuildCaptureCommand(sp)
	if err != nil {
		t.Fatalf("BuildCaptureCommand: %v", err)
	}
	if cmd.Type != models.ThoughtTypeURL {
		t.Fatalf("Type = %q, want url", cmd.Type)
	}
}

func TestScratchpadServiceBuildCaptureCommandDefaultsToTextType(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "just a plain text thought, no url"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp, _ := store.Get("s1")
	cmd, err := svc.BuildCaptureCommand(sp)
	if err != nil {
		t.Fatalf("BuildCaptureCommand: %v", err)
	}
	if cmd.Type != models.ThoughtTypeText {
		t.Fatalf("Type = %q, want text", cmd.Type)
	}
}

func TestScratchpadServiceBuildCaptureCommandRejectsEmptyContent(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	_, err := svc.BuildCaptureCommand(scratchpad.Scratchpad{SessionID: "s1"})
	if err == nil {
		t.Fatalf("empty content should error")
	}
}

func TestScratchpadServiceBuildCaptureCommandRejectsAlreadyCommitted(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = svc.AppendMessage("s1", "user", "hello")
	sp, _ := store.Get("s1")
	sp.CommittedThoughtID = "thought-1"
	_, err := svc.BuildCaptureCommand(sp)
	if !errors.Is(err, ErrAlreadyCommitted) {
		t.Fatalf("err = %v, want ErrAlreadyCommitted", err)
	}
}

func TestScratchpadServiceResetAfterCommitClearsVolatileKeepsLink(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = svc.AppendMessage("s1", "user", "draft")
	_, _ = store.MarkCommitted("s1", "thought-1")
	reset, err := svc.ResetAfterCommit("s1")
	if err != nil {
		t.Fatalf("ResetAfterCommit: %v", err)
	}
	if reset.Content != "" || len(reset.Messages) != 0 {
		t.Fatalf("volatile fields not cleared: %+v", reset)
	}
	if reset.CommittedThoughtID != "thought-1" {
		t.Fatalf("committed link lost: %+v", reset)
	}
}

func TestScratchpadServiceResetAfterCommitOnUncommittedIsPlainReset(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = svc.AppendMessage("s1", "user", "draft")
	reset, err := svc.ResetAfterCommit("s1")
	if err != nil {
		t.Fatalf("ResetAfterCommit: %v", err)
	}
	if reset.Content != "" {
		t.Fatalf("Content = %q", reset.Content)
	}
	if reset.CommittedThoughtID != "" {
		t.Fatalf("should still be uncommitted, got %+v", reset)
	}
}

func TestScratchpadServiceRejectsNilStore(t *testing.T) {
	svc := NewScratchpadService(nil)
	if _, err := svc.AppendMessage("s1", "user", "x"); !errors.Is(err, ErrScratchpadUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{}); !errors.Is(err, ErrScratchpadUnavailable) {
		t.Fatalf("AppendDraft err = %v", err)
	}
	if _, err := svc.ResetAfterCommit("s1"); !errors.Is(err, ErrScratchpadUnavailable) {
		t.Fatalf("ResetAfterCommit err = %v", err)
	}
}

func TestTrimNonEmpty(t *testing.T) {
	got := trimNonEmpty([]string{"a", "  ", "b", "", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("trimNonEmpty = %v", got)
	}
}

func TestUnionStringsPreservesOrderDedupe(t *testing.T) {
	got := unionStrings([]string{"a", "b"}, []string{"b", "c", "a"})
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("union = %v, want %v", got, want)
	}
}

func TestSubtractStringsRemovesAllOccurrences(t *testing.T) {
	got := subtractStrings([]string{"a", "b", "a", "c"}, []string{"a"})
	want := []string{"b", "c"}
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("subtract = %v, want %v", got, want)
	}
}

// stubCapture records Capture / PatchThought calls so the commit
// pipeline can be exercised without a real capture Service.
type stubCapture struct {
	captureCalls   int
	patchCalls     int
	patchReq       models.ThoughtPatchRequest
	captureResult  models.CaptureResult
	patchResult    models.ThoughtSnapshot
	patchErr       error
	captureErr     error
	lastPatchRaw   []byte
	lastSessionID  string
}

func (s *stubCapture) Capture(_ context.Context, cmd models.CaptureCommand) (models.CaptureResult, error) {
	s.captureCalls++
	if s.captureErr != nil {
		return models.CaptureResult{}, s.captureErr
	}
	return s.captureResult, nil
}

func (s *stubCapture) PatchThought(_ context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	s.patchCalls++
	s.patchReq = request
	s.lastPatchRaw = rawBody
	s.lastSessionID = sessionID
	if s.patchErr != nil {
		return models.ThoughtSnapshot{}, s.patchErr
	}
	return s.patchResult, nil
}

func TestScratchpadServiceCommitFreshFiresCaptureAndMarksCommitted(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{
		captureResult: models.CaptureResult{
			Thought: models.Thought{ID: "thought-1", Type: models.ThoughtTypeText},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.AppendMessage("s1", "user", "draft content"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Thought.ID != "thought-1" {
		t.Fatalf("thought id = %q", result.Thought.ID)
	}
	if captureStub.captureCalls != 1 {
		t.Fatalf("Capture called %d times, want 1", captureStub.captureCalls)
	}
	sp, _ := store.Get("s1")
	if sp.CommittedThoughtID != "thought-1" {
		t.Fatalf("CommittedThoughtID = %q, want thought-1", sp.CommittedThoughtID)
	}
	if sp.CommittedAt == nil {
		t.Fatalf("CommittedAt not set")
	}
	if sp.Content != "" || len(sp.Messages) != 0 {
		t.Fatalf("ResetAfterCommit did not clear volatile fields: %+v", sp)
	}
}

func TestScratchpadServiceCommitRepeatAppendsToExistingThought(t *testing.T) {
	store := newMemoryScratchpad()
	// Pre-stage: scratchpad already committed to thought-1.
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:          "s1",
		Content:            "first round",
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
	})
	// User adds more content + a rename + a tag.
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "first round\n\nmore thoughts",
		Draft: scratchpad.Draft{
			TitleSet:    "renamed",
			TagsAdded:   []string{"new-tag"},
		},
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
	})

	captureStub := &stubCapture{}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.captureCalls != 0 {
		t.Fatalf("Capture should not run on repeat commit, called %d", captureStub.captureCalls)
	}
	if captureStub.patchCalls != 1 {
		t.Fatalf("PatchThought called %d, want 1", captureStub.patchCalls)
	}
	if captureStub.patchReq.Title == nil || *captureStub.patchReq.Title != "renamed" {
		t.Fatalf("Title = %v, want renamed", captureStub.patchReq.Title)
	}
	if captureStub.patchReq.Tags == nil || len(*captureStub.patchReq.Tags) != 1 {
		t.Fatalf("Tags = %v", captureStub.patchReq.Tags)
	}
	if captureStub.lastSessionID != "s1" {
		t.Fatalf("session id = %q, want s1", captureStub.lastSessionID)
	}
	if result.Thought.ID != "thought-1" {
		t.Fatalf("result thought id = %q, want thought-1", result.Thought.ID)
	}
}

func TestScratchpadServiceCommitRequiresCaptureWiring(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{SessionID: "s1", Content: "x"})
	svc := NewScratchpadService(store) // no WithCapture
	_, err := svc.Commit(context.Background(), "s1")
	if err == nil || !strings.Contains(err.Error(), "not wired up") {
		t.Fatalf("err = %v, want wiring error", err)
	}
}

func TestScratchpadServiceCommitRejectsEmptyContent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store, WithCapture(&stubCapture{}))
	_, err := svc.Commit(context.Background(), "s1")
	if err == nil {
		t.Fatalf("empty content should error")
	}
}

func TestScratchpadServiceCommitRepeatWithNoDraftChangesIsNoop(t *testing.T) {
	store := newMemoryScratchpad()
	// Pre-stage: scratchpad already committed, but no new content.
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:          "s1",
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
	})
	captureStub := &stubCapture{}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.patchCalls != 0 {
		t.Fatalf("Patch should not run when nothing changed, called %d", captureStub.patchCalls)
	}
	if result.Thought.ID != "thought-1" {
		t.Fatalf("result thought id = %q", result.Thought.ID)
	}
	// Reset should still happen so the UI is in a clean state.
	sp, _ := store.Get("s1")
	if sp.Content != "" || len(sp.Draft.TagsAdded) != 0 {
		t.Fatalf("scratchpad not reset: %+v", sp)
	}
}

func ptrTime() *time.Time {
	t := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	return &t
}
