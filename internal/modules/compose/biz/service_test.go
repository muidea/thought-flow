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
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
)

type composeDocumentGenerator struct{}

func (composeDocumentGenerator) GenerateDocument(_ context.Context, req ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	sections := make(map[string]models.DocumentSection, len(req.Profile.Sections))
	for _, section := range req.Profile.Sections {
		sections[section.Key] = models.DocumentSection{Content: strings.Repeat("完整内容。", 40)}
	}
	return models.DocumentDraft{Title: "Generated Design", Summary: "Summary", Sections: sections}, nil
}

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
	svc := NewService(ws, composedraft.New(root), jobstore.New(ws.JobsPath), nil, nil, synth, sink)
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

func TestServiceCreateDraftAndSaveUsesDocumentProfile(t *testing.T) {
	svc, sink, _ := newTestService(t)
	registry, err := documentprofile.NewRegistry(t.TempDir(), models.DocumentProfileBuiltinNote, documentprofile.Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	svc.SetDocumentProfiles(registry, composeDocumentGenerator{}, 1)
	writeThought(t, svc.workspace.RootPath, "20260609-0001-profile", "Seed", "source body")
	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources:        []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-profile"}},
		Goal:           "Produce a design",
		ProfileID:      models.DocumentProfileBuiltinDesignDoc,
		ProfileVersion: 1,
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if draft.DocumentProfile == nil || draft.DocumentProfile.ProfileID != models.DocumentProfileBuiltinDesignDoc {
		t.Fatalf("document profile = %+v", draft.DocumentProfile)
	}
	if draft.Validation == nil || draft.Validation.Status != models.ArchiveValidationValid || draft.DocumentDraft == nil {
		t.Fatalf("structured draft validation = %+v draft=%+v", draft.Validation, draft.DocumentDraft)
	}
	if _, err := svc.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if len(sink.calls) != 1 || sink.calls[0].DocumentProfile == nil || sink.calls[0].DocumentProfile.ContentHash != draft.DocumentProfile.ContentHash {
		t.Fatalf("capture command profile = %+v", sink.calls)
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

func TestServiceCreateDraftSupportsNonThoughtSources(t *testing.T) {
	svc, _, synth := newTestService(t)

	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{
			{
				SourceType: models.ComposeSourceTypeSearchResult,
				SourceID:   "search-result-1",
				Title:      "Search result",
				SourceLink: "thoughts/2026/06/search-result-1.md",
			},
			{
				SourceType: models.ComposeSourceTypeTopicSection,
				SourceID:   "topic-a#context",
				Title:      "Topic context",
				SourceLink: "topics/topic-a/index.md#context",
			},
			{
				SourceType: models.ComposeSourceTypeCaptureSession,
				SourceID:   "session-1",
				Title:      "Capture session",
				SourceLink: "capture/session-1",
			},
		},
		Goal: "Compose from runtime sources",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if len(draft.Sources) != 3 {
		t.Fatalf("sources len = %d, want 3", len(draft.Sources))
	}
	if len(draft.SourceLinks) != 3 {
		t.Fatalf("source links len = %d, want 3", len(draft.SourceLinks))
	}
	if len(synth.lastReq.Snapshots) != 3 {
		t.Fatalf("snapshots len = %d, want 3", len(synth.lastReq.Snapshots))
	}
	if got := synth.lastReq.Snapshots[0].Thought.Source; got != models.ComposeSourceTypeSearchResult {
		t.Fatalf("first snapshot source = %q", got)
	}
	if got := synth.lastReq.Snapshots[1].Content.Links; got != "topics/topic-a/index.md#context" {
		t.Fatalf("topic source link = %q", got)
	}
	if !strings.Contains(synth.lastReq.Snapshots[2].Content.Original, "capture_session") {
		t.Fatalf("capture context missing discriminator: %q", synth.lastReq.Snapshots[2].Content.Original)
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

func TestServiceCreateDraftUsesNonThoughtSourcesWhenThoughtMissing(t *testing.T) {
	svc, _, synth := newTestService(t)

	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "missing-2026-xxxx"},
			{SourceType: models.ComposeSourceTypeSearchResult, SourceID: "search-result-1", Title: "Search result"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if len(draft.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(draft.Sources))
	}
	if len(synth.lastReq.Snapshots) != 1 {
		t.Fatalf("snapshots len = %d, want 1", len(synth.lastReq.Snapshots))
	}
	if got := synth.lastReq.Snapshots[0].Thought.ID; got != "search-result-1" {
		t.Fatalf("hydrated snapshot id = %q", got)
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
	if sink.calls[0].Title != "Compose" {
		t.Fatalf("title = %q", sink.calls[0].Title)
	}
	if len(sink.calls[0].Tags) == 0 || sink.calls[0].Tags[0] != "compose" {
		t.Fatalf("tags = %v", sink.calls[0].Tags)
	}
}

func TestServiceSaveDraftKeepsSourcesOutOfBodyAndPassesLinks(t *testing.T) {
	svc, sink, _ := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-aaaa", "Seed", "body")
	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{
			SourceType: models.ComposeSourceTypeThought,
			SourceID:   "20260609-0001-aaaa",
			Title:      "Seed",
			SourceLink: "thoughts/2026/06/20260609-0001-aaaa.md",
		}},
		Goal: "Make an outline",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	_, err = svc.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{
		Content: "# Final Compose\n\nBody.\n\n### Sources\n\n- [[thoughts/2026/06/20260609-0001-aaaa.md]]",
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("calls = %d", len(sink.calls))
	}
	if strings.Contains(sink.calls[0].Content, "### Sources") {
		t.Fatalf("source appendix should be stripped from content: %q", sink.calls[0].Content)
	}
	if sink.calls[0].Title != "Final Compose" {
		t.Fatalf("title = %q", sink.calls[0].Title)
	}
	if len(sink.calls[0].Links) != 1 || sink.calls[0].Links[0] != "thoughts/2026/06/20260609-0001-aaaa.md" {
		t.Fatalf("links = %v", sink.calls[0].Links)
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

func TestServiceDeleteDraftRemovesFileAndIsIdempotent(t *testing.T) {
	svc, _, _ := newTestService(t)
	writeThought(t, svc.workspace.RootPath, "20260609-0001-del", "Delete seed", "body")
	draft, err := svc.CreateDraft(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-del"}},
		Goal:    "to delete",
		Format:  models.ComposeFormatSummary,
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if err := svc.DeleteDraft(context.Background(), draft.ID); err != nil {
		t.Fatalf("DeleteDraft: %v", err)
	}
	if _, err := svc.GetDraft(context.Background(), draft.ID); err == nil {
		t.Fatalf("expected missing draft after delete")
	}
	// Idempotent second delete.
	if err := svc.DeleteDraft(context.Background(), draft.ID); err != nil {
		t.Fatalf("DeleteDraft(idempotent): %v", err)
	}
	if err := svc.DeleteDraft(context.Background(), ""); err == nil {
		t.Fatalf("empty draft id should error")
	}
}

func TestServiceDeleteDraftCancelsInflightGenerate(t *testing.T) {
	svc, _, synth := newTestService(t)
	gate := make(chan struct{})
	slow := &blockingSynthesis{stub: synth, gate: gate}
	svc.synthesis = slow
	writeThought(t, svc.workspace.RootPath, "20260609-0001-delgen", "Delgen seed", "body")

	job, err := svc.CreateDraftAsync(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-delgen"}},
		Goal:    "delete while generating",
		Format:  models.ComposeFormatSummary,
	})
	if err != nil {
		t.Fatalf("CreateDraftAsync: %v", err)
	}
	// Placeholder exists while blocked.
	if _, err := svc.GetDraft(context.Background(), job.ResourceID); err != nil {
		t.Fatalf("GetDraft(placeholder): %v", err)
	}
	if err := svc.DeleteDraft(context.Background(), job.ResourceID); err != nil {
		t.Fatalf("DeleteDraft: %v", err)
	}
	close(gate)
	workerDone := make(chan struct{})
	go func() {
		svc.workerWG.Wait()
		close(workerDone)
	}()
	select {
	case <-workerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("compose generation worker did not stop after draft deletion")
	}

	deadline := time.Now().Add(3 * time.Second)
	var final models.Job
	for time.Now().Before(deadline) {
		final, err = svc.jobs.Get(job.ID)
		if err == nil && (final.Status == models.JobStatusCanceled || final.Status == models.JobStatusSucceeded || final.Status == models.JobStatusFailed) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Draft must stay gone — worker must not recreate it.
	if _, err := svc.GetDraft(context.Background(), job.ResourceID); err == nil {
		t.Fatalf("deleted draft was recreated by worker")
	}
	if final.Status != models.JobStatusCanceled && final.Status != models.JobStatusFailed {
		// canceled is preferred; failed is acceptable if synthesize raced before delete guard.
		t.Fatalf("job status = %q, want canceled (or failed)", final.Status)
	}
}

func TestServiceCreateDraftAsyncQueuesJobAndMaterialisesDraft(t *testing.T) {
	svc, _, synth := newTestService(t)
	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-async", "Async seed", "async body")
	if _, err := svc.SaveBasket(context.Background(), []models.ComposeSource{
		{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-async", Title: "Async seed"},
		{SourceType: models.ComposeSourceTypeSearchResult, SourceID: "keep-next", Title: "Keep for next draft"},
	}); err != nil {
		t.Fatalf("SaveBasket: %v", err)
	}

	job, err := svc.CreateDraftAsync(context.Background(), models.ComposeRequest{
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-async", Title: "Async seed"},
		},
		Goal:   "Async goal",
		Format: models.ComposeFormatSummary,
	})
	if err != nil {
		t.Fatalf("CreateDraftAsync: %v", err)
	}
	if job.ID == "" || job.Type != models.JobTypeComposeGenerate {
		t.Fatalf("job = %#v", job)
	}
	if job.ResourceID == "" {
		t.Fatalf("job.ResourceID empty")
	}
	// Placeholder should exist immediately.
	placeholder, err := svc.GetDraft(context.Background(), job.ResourceID)
	if err != nil {
		t.Fatalf("GetDraft(placeholder): %v", err)
	}
	if placeholder.Status != models.ComposeStatusGenerating && placeholder.Status != models.ComposeStatusDraft {
		t.Fatalf("placeholder status = %q", placeholder.Status)
	}
	if placeholder.RequestFingerprint == "" {
		t.Fatalf("placeholder fingerprint empty")
	}

	deadline := time.Now().Add(3 * time.Second)
	var ready models.ComposeDraft
	var storedJob models.Job
	for time.Now().Before(deadline) {
		ready, err = svc.GetDraft(context.Background(), job.ResourceID)
		if err == nil && ready.Status == models.ComposeStatusFailed {
			t.Fatalf("draft failed: %#v", ready)
		}
		storedJob, err = svc.jobs.Get(job.ID)
		if err == nil &&
			ready.Status == models.ComposeStatusDraft &&
			strings.TrimSpace(ready.Content) != "" &&
			storedJob.Status == models.JobStatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ready.Status != models.ComposeStatusDraft || !strings.Contains(ready.Content, "Hello") {
		t.Fatalf("ready draft = %#v", ready)
	}
	if synth.calls < 1 {
		t.Fatalf("synth.calls = %d", synth.calls)
	}
	if storedJob.Status != models.JobStatusSucceeded {
		t.Fatalf("job status = %q", storedJob.Status)
	}
	basket, err := svc.GetBasket(context.Background())
	if err != nil {
		t.Fatalf("GetBasket: %v", err)
	}
	if len(basket.Sources) != 1 || basket.Sources[0].SourceID != "keep-next" {
		t.Fatalf("basket after successful generation = %#v", basket.Sources)
	}
}

func TestServiceCreateDraftAsyncDedupesIdenticalInflightRequests(t *testing.T) {
	svc, _, synth := newTestService(t)
	// Slow synthesis so both clicks land while the first job is still open.
	synth.body = "# Slow compose\n\nBody."
	gate := make(chan struct{})
	slow := &blockingSynthesis{stub: synth, gate: gate}
	svc.synthesis = slow

	root := svc.workspace.RootPath
	writeThought(t, root, "20260609-0001-dedupe", "Dedupe seed", "dedupe body")
	req := models.ComposeRequest{
		Sources: []models.ComposeSource{
			{SourceType: models.ComposeSourceTypeThought, SourceID: "20260609-0001-dedupe", Title: "Dedupe seed"},
		},
		Goal:   "Dedupe goal",
		Format: models.ComposeFormatOutline,
	}

	job1, err := svc.CreateDraftAsync(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateDraftAsync(1): %v", err)
	}
	job2, err := svc.CreateDraftAsync(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateDraftAsync(2): %v", err)
	}
	if job1.ID != job2.ID {
		t.Fatalf("expected same job, got %q vs %q", job1.ID, job2.ID)
	}
	if job1.ResourceID != job2.ResourceID {
		t.Fatalf("expected same draft resource, got %q vs %q", job1.ResourceID, job2.ResourceID)
	}

	close(gate)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		draft, err := svc.GetDraft(context.Background(), job1.ResourceID)
		if err == nil && draft.Status == models.ComposeStatusDraft && strings.TrimSpace(draft.Content) != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Only one LLM call for the shared fingerprint.
	if slow.calls() != 1 {
		t.Fatalf("synth calls = %d, want 1", slow.calls())
	}
	drafts, err := svc.ListDrafts(context.Background())
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	count := 0
	for _, draft := range drafts {
		if draft.Goal == "Dedupe goal" || draft.RequestFingerprint != "" {
			// Count drafts created for this request fingerprint.
			if draft.ID == job1.ResourceID {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected single draft for fingerprint, drafts=%#v", drafts)
	}
}

func TestServiceRecoverGeneratingDraftsResumesPersistedJob(t *testing.T) {
	svc, _, synth := newTestService(t)
	root := svc.workspace.RootPath
	const thoughtID = "20260609-0001-recover"
	writeThought(t, root, thoughtID, "Recovery seed", "recovery body")

	now := time.Now().UTC()
	fingerprint := composeRequestFingerprint([]models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: thoughtID, Title: "Recovery seed"}}, models.ComposeRequest{
		Goal:   "Recover generation",
		Prompt: "Keep the original instruction",
		Format: models.ComposeFormatSummary,
	})
	placeholder, err := svc.draftStore.SaveDraft(context.Background(), models.ComposeDraft{
		ID:                 "compose-recover-1",
		Sources:            []models.ComposeSource{{SourceType: models.ComposeSourceTypeThought, SourceID: thoughtID, Title: "Recovery seed"}},
		Goal:               "Recover generation",
		Format:             models.ComposeFormatSummary,
		Content:            "",
		Status:             models.ComposeStatusGenerating,
		GenerationPrompt:   "Keep the original instruction",
		RequestFingerprint: fingerprint,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if err != nil {
		t.Fatalf("SaveDraft(placeholder): %v", err)
	}
	job, err := svc.jobs.Create(models.JobTypeComposeGenerate, models.ResourceTypeComposeDraft, placeholder.ID, "interrupted by restart")
	if err != nil {
		t.Fatalf("Create(job): %v", err)
	}
	placeholder.JobID = job.ID
	if _, err := svc.draftStore.SaveDraft(context.Background(), placeholder); err != nil {
		t.Fatalf("SaveDraft(job id): %v", err)
	}

	// Construct a fresh service to model the process that survived the restart.
	restarted := NewService(svc.workspace, svc.draftStore, svc.jobs, nil, nil, synth, nil)
	restarted.SetModel(synth.model)
	recovered, err := restarted.RecoverGeneratingDrafts()
	if err != nil {
		t.Fatalf("RecoverGeneratingDrafts: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		draft, draftErr := restarted.GetDraft(context.Background(), placeholder.ID)
		storedJob, jobErr := restarted.jobs.Get(job.ID)
		if draftErr == nil && jobErr == nil && draft.Status == models.ComposeStatusDraft && storedJob.Status == models.JobStatusSucceeded {
			if !strings.Contains(draft.Content, "Hello") {
				t.Fatalf("recovered draft content = %q", draft.Content)
			}
			if synth.lastReq.Goal != "Recover generation\n\nKeep the original instruction" {
				t.Fatalf("recovered prompt lost: %q", synth.lastReq.Goal)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("recovered job %s did not complete", job.ID)
}

type blockingSynthesis struct {
	stub *stubSynthesis
	gate chan struct{}
	mu   sync.Mutex
	n    int
}

func (b *blockingSynthesis) Synthesize(ctx context.Context, req ai.SynthesisRequest) (models.SynthesisDraft, error) {
	b.mu.Lock()
	b.n++
	b.mu.Unlock()
	select {
	case <-b.gate:
	case <-ctx.Done():
		return models.SynthesisDraft{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return models.SynthesisDraft{}, errors.New("blocking synthesis timed out")
	}
	return b.stub.Synthesize(ctx, req)
}

func (b *blockingSynthesis) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

func TestComposeRequestFingerprintStableAndSensitive(t *testing.T) {
	sources := []models.ComposeSource{
		{SourceType: "thought", SourceID: "b"},
		{SourceType: "thought", SourceID: "a"},
	}
	req := models.ComposeRequest{Goal: "G", Format: "summary", ProfileID: "p", ProfileVersion: 1}
	// Source order must not change the fingerprint.
	left := composeRequestFingerprint(sources, req)
	right := composeRequestFingerprint([]models.ComposeSource{sources[1], sources[0]}, req)
	if left == "" || left != right {
		t.Fatalf("fingerprint unstable: %q vs %q", left, right)
	}
	other := composeRequestFingerprint(sources, models.ComposeRequest{Goal: "G2", Format: "summary", ProfileID: "p", ProfileVersion: 1})
	if other == left {
		t.Fatalf("fingerprint should change with goal")
	}
	changedSource := []models.ComposeSource{{SourceType: "thought", SourceID: "b", Title: "Updated title"}, sources[1]}
	if composeRequestFingerprint(changedSource, req) == left {
		t.Fatalf("fingerprint should change with source context")
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
