package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/composedraft"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
)

// stubCapture is an in-memory implementation of CaptureSink. It
// records every call so the test can assert the source was set to
// "compose" and the title/tags/content flowed through unchanged.
type stubCapture struct {
	mu      sync.Mutex
	results []models.CaptureResult
	calls   []models.CaptureCommand
	err     error
}

func (s *stubCapture) Capture(_ context.Context, cmd models.CaptureCommand) (models.CaptureResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return models.CaptureResult{}, s.err
	}
	thought := models.Thought{
		ID:           "compose-saved-" + cmd.Title,
		DisplayTitle: cmd.Title,
		UserTitle:    cmd.Title,
		Type:         cmd.Type,
		Path:         "thoughts/compose/" + cmd.Title + ".md",
	}
	s.calls = append(s.calls, cmd)
	s.results = append(s.results, models.CaptureResult{Thought: thought})
	return s.results[len(s.results)-1], nil
}

// stubSynthesis returns a fixed body so the test can assert the
// stored draft content is the LLM output, not the request body.
type stubSynthesis struct {
	body    string
	model   string
	calls   int
	lastReq ai.SynthesisRequest
}

func (s *stubSynthesis) Synthesize(_ context.Context, req ai.SynthesisRequest) (models.SynthesisDraft, error) {
	s.calls++
	s.lastReq = req
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          "syn-" + req.Format,
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        req.Goal,
		Format:      req.Format,
		Content:     s.body,
		SourceLinks: req.SourceLinks,
		Model:       s.model,
		Status:      "draft",
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func writeThought(t *testing.T, root, id, title, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "thoughts", "2026", "06"), 0o755); err != nil {
		t.Fatalf("mkdir thoughts: %v", err)
	}
	now := time.Now().UTC()
	md := strings.Join([]string{
		"---",
		"id: " + id,
		"title: " + title,
		"created_at: " + now.Format(time.RFC3339),
		"---",
		"",
		"# " + title,
		"",
		body,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "thoughts", "2026", "06", id+".md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write thought: %v", err)
	}
}

func newTestService(t *testing.T) (*Service, *stubCapture, *stubSynthesis) {
	t.Helper()
	root := t.TempDir()
	ws := &models.Workspace{ID: "test", RootPath: root, RuntimePath: filepath.Join(root, ".thoughtflow"), JobsPath: filepath.Join(root, ".thoughtflow", "jobs")}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	sink := &stubCapture{}
	synth := &stubSynthesis{body: "# Compose\n\nHello.", model: "stub-model"}
	svc := NewService(ws, composedraft.New(root), jobstore.New(ws.JobsPath), nil, synth, sink)
	svc.SetModel(synth.model)
	return svc, sink, synth
}

func TestServiceCreateDraftHappyPath(t *testing.T) {
	svc, _, synth := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed 1", "body 1")
	writeThought(t, root, "20260609-0001-bbbb", "Seed 2", "body 2")

	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa", Title: "Seed 1"},
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-bbbb", Title: "Seed 2"},
		},
		Goal:   "Sketch the plan",
		Format: models.ComposeFormatOutline,
	})
	if err != nil {
		t.Fatalf("CreateDraft error = %v", err)
	}
	if draft.ID == "" {
		t.Fatalf("draft.ID empty")
	}
	if draft.Content != "# Compose\n\nHello." {
		t.Fatalf("content = %q", draft.Content)
	}
	if draft.Model != "stub-model" {
		t.Fatalf("model = %q", draft.Model)
	}
	if draft.Status != models.ComposeStatusDraft {
		t.Fatalf("status = %q", draft.Status)
	}
	if len(draft.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(draft.Sources))
	}
	if synth.calls != 1 {
		t.Fatalf("synth.calls = %d, want 1", synth.calls)
	}
	if len(synth.lastReq.Snapshots) != 2 {
		t.Fatalf("snapshots len = %d, want 2", len(synth.lastReq.Snapshots))
	}
	if synth.lastReq.Goal != "Sketch the plan" {
		t.Fatalf("goal = %q", synth.lastReq.Goal)
	}
	if synth.lastReq.Format != models.ComposeFormatOutline {
		t.Fatalf("format = %q", synth.lastReq.Format)
	}
}

func TestServiceCreateDraftRejectsEmptySources(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.CreateDraft(context.Background(), models.ComposeRequest{})
	if err == nil || !strings.Contains(err.Error(), "sources are required") {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceCreateDraftDedupeSourcesAndLinks(t *testing.T) {
	svc, _, _ := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed 1", "body 1")

	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa", SourceLink: "thoughts/a.md"},
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa", SourceLink: "thoughts/a.md"},
			{SourceType: models.ComposeSourceTypeSearchResult, SourceID: "s1", SourceLink: "thoughts/b.md"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if len(draft.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(draft.Sources))
	}
	if len(draft.SourceLinks) != 2 {
		t.Fatalf("links len = %d, want 2", len(draft.SourceLinks))
	}
}

func TestServiceCreateDraftSkipsMissingThoughts(t *testing.T) {
	svc, _, synth := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed 1", "body")

	// Both sources point at thought IDs, but only the first exists
	// on disk. The draft must still be created from the surviving
	// snapshot (the second source is silently dropped, never errored).
	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa"},
			{SourceType: models.ComposeSourceTypeThought, SourceID: "missing-2026-xxxx"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if len(synth.lastReq.Snapshots) != 1 {
		t.Fatalf("snapshots len = %d, want 1", len(synth.lastReq.Snapshots))
	}
	if len(draft.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2 (both kept; only hydration failed)", len(draft.Sources))
	}
}

func TestServiceCreateDraftAppendsPromptToGoal(t *testing.T) {
	svc, _, synth := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed", "body")

	_, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa"}},
		Goal:    "Make a plan",
		Prompt:  "Keep it under 200 words",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if !strings.Contains(synth.lastReq.Goal, "Make a plan") || !strings.Contains(synth.lastReq.Goal, "Keep it under 200 words") {
		t.Fatalf("goal missing pieces: %q", synth.lastReq.Goal)
	}
}

func TestServiceListAndGetDraft(t *testing.T) {
	svc, _, _ := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed 1", "body 1")
	created, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa"}},
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	list, err := svc.ListDrafts(context.Background())
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}
	got, err := svc.GetDraft(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetDraft: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("got = %+v", got)
	}
}

func TestServiceSaveDraftWritesThoughtWithComposeSource(t *testing.T) {
	svc, sink, _ := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed", "seed body")

	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa"}},
		Goal:    "Save me",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	result, err := svc.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{
		Title: "Renamed compose",
		Tags:  []string{"compose", "essay"},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("capture calls = %d, want 1", len(sink.calls))
	}
	if got := sink.calls[0].Source; got != models.ThoughtSourceCompose {
		t.Fatalf("source = %q, want compose", got)
	}
	if sink.calls[0].Title != "Renamed compose" {
		t.Fatalf("title = %q", sink.calls[0].Title)
	}
	if result.Thought.ID == "" {
		t.Fatalf("result.Thought.ID empty")
	}

	// Re-fetching the draft shows the saved_thought_id and saved_at
	// have been written to disk.
	loaded, err := svc.GetDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatalf("GetDraft after save: %v", err)
	}
	if loaded.Status != models.ComposeStatusSaved {
		t.Fatalf("status = %q", loaded.Status)
	}
	if loaded.SavedThoughtID != result.Thought.ID {
		t.Fatalf("saved_thought_id = %q, want %q", loaded.SavedThoughtID, result.Thought.ID)
	}
	if loaded.SavedAt == nil {
		t.Fatalf("saved_at nil")
	}
}

func TestServiceSaveDraftRejectsAlreadySaved(t *testing.T) {
	svc, _, _ := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed", "body")
	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa"}},
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if _, err := svc.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	_, err = svc.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{})
	if err == nil || !strings.Contains(err.Error(), "already saved") {
		t.Fatalf("second save err = %v, want already saved", err)
	}
}

func TestServiceSaveDraftDefaultTitleAndTags(t *testing.T) {
	svc, sink, _ := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed", "body")
	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa", Title: "Seed"},
		},
		Goal: "Make an outline",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if _, err := svc.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("calls = %d", len(sink.calls))
	}
	if sink.calls[0].Title != "Make an outline" {
		t.Fatalf("title = %q", sink.calls[0].Title)
	}
	if len(sink.calls[0].Tags) == 0 || sink.calls[0].Tags[0] != "compose" {
		t.Fatalf("tags = %v", sink.calls[0].Tags)
	}
}

func TestServiceSaveDraftCaptureError(t *testing.T) {
	svc, sink, _ := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed", "body")
	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-aaaa"}},
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	sink.err = errors.New("disk full")
	_, err = svc.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	loaded, _ := svc.GetDraft(context.Background(), draft.ID)
	if loaded.Status != models.ComposeStatusDraft {
		t.Fatalf("status flipped to %q on failed save", loaded.Status)
	}
}

func TestFirstNonEmptyAndDedupes(t *testing.T) {
	if firstNonEmpty("", "  ", "x", "y") != "x" {
		t.Fatalf("firstNonEmpty wrong")
	}
	if firstNonEmpty("", "  ") != "" {
		t.Fatalf("firstNonEmpty all-empty")
	}
	got := dedupeStrings([]string{"a", "b", "a", "c", "b", ""})
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("dedupe = %v", got)
	}
}

func TestDedupeSourcesStable(t *testing.T) {
	src := []models.ComposeSource{
		{SourceType: "thought", SourceID: "b", SourceLink: "thoughts/b.md"},
		{SourceType: "thought", SourceID: "a", SourceLink: "thoughts/a.md"},
		{SourceType: "thought", SourceID: "a", SourceLink: "thoughts/a.md"},
		{SourceType: "", SourceID: ""},
		{SourceType: "search_result", SourceID: "s", SourceLink: "thoughts/s.md"},
	}
	deduped, links := dedupeSources(src)
	if len(deduped) != 3 {
		t.Fatalf("deduped = %d, want 3", len(deduped))
	}
	if strings.Join(links, ",") != "thoughts/a.md,thoughts/b.md,thoughts/s.md" {
		t.Fatalf("links = %v", links)
	}
}

func TestDeriveComposeTitleAndTags(t *testing.T) {
	draft := models.ComposeDraft{
		Goal: "Top-level goal\nWith more lines",
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "x", Title: ""},
			{SourceType: models.ComposeSourceTypeTopicSection, SourceID: "t"},
		},
	}
	if deriveComposeTitle(draft) != "Top-level goal" {
		t.Fatalf("title = %q", deriveComposeTitle(draft))
	}
	tags := deriveComposeTags(draft)
	if tags[0] != "compose" || !containsString(tags, "topic") {
		t.Fatalf("tags = %v", tags)
	}
	// No goal → fall back to first source title
	draft.Goal = ""
	if deriveComposeTitle(draft) != "x" {
		t.Fatalf("fallback title = %q", deriveComposeTitle(draft))
	}
}

func TestConvertHistoryEmpty(t *testing.T) {
	if out := convertHistory(nil); out != nil {
		t.Fatalf("expected nil, got %v", out)
	}
}

func TestConvertHistoryCopiesFields(t *testing.T) {
	now := time.Now().UTC()
	in := []models.SynthesisDraftHistory{
		{Status: "draft", Message: "created", At: now},
		{Status: "saved", Message: "saved", ThoughtID: "t-1", At: now},
	}
	out := convertHistory(in)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	if out[1].ThoughtID != "t-1" {
		t.Fatalf("thought_id lost: %+v", out[1])
	}
	if out[0].Status != "draft" {
		t.Fatalf("status lost: %+v", out[0])
	}
}

func TestServiceSaveDraftRejectsMissingService(t *testing.T) {
	// nil service: the helpers must guard against this so a half-
	// wired module (e.g. capture not yet initialised) returns a
	// descriptive error instead of panicking.
	var s *Service
	_, err := s.SaveDraft(context.Background(), "x", models.ComposeSaveRequest{})
	if err == nil {
		t.Fatalf("expected error from nil service")
	}
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
