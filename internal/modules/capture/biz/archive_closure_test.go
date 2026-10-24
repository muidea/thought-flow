package biz

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

func archiveFixture(t *testing.T, family string, version int) (*ScratchpadService, *scratchpad.Store, *Service) {
	t.Helper()
	root := t.TempDir()
	store := scratchpad.New(filepath.Join(root, "sessions"))
	registry, e := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	capture := NewService(&models.Workspace{ID: "test", RootPath: root}, nil, nil)
	svc := NewScratchpadService(store, WithCapture(capture), WithDocumentProfiles(registry, nil, 10, 1))
	_, e = store.Save(scratchpad.Scratchpad{SessionID: "s", Content: "Original material", Messages: []scratchpad.Message{{Role: "user", Text: "Original material"}}, ArchiveStrategy: scratchpad.ArchiveStrategyNew, SessionContext: scratchpad.SessionContext{CandidateTitle: "Title", CandidateBody: "Original material", CandidateProfileID: family, CandidateProfileVersion: version}})
	if e != nil {
		t.Fatal(e)
	}
	return svc, store, capture
}
func publishFixture(t *testing.T, svc *ScratchpadService, store *scratchpad.Store) scratchpad.Scratchpad {
	t.Helper()
	sp, e := svc.BeginPreview("s", false)
	if e != nil {
		t.Fatal(e)
	}
	p, e := svc.PrepareArchive(context.Background(), sp, nil)
	if e != nil {
		t.Fatal(e)
	}
	sp, e = svc.PublishPreview(sp, p)
	if e != nil {
		t.Fatal(e)
	}
	return sp
}
func TestArchiveDraftMaterializesAndUpgradesSameIdentity(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	publishFixture(t, svc, store)
	draft, e := svc.SaveDraft(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	current, e := capture.GetThought(context.Background(), draft.Thought.ID)
	if e != nil || !current.Thought.CaptureDraft {
		t.Fatalf("draft not materialized: %+v %v", current, e)
	}
	sp, _ := scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions")).Get("s")
	if sp.CommittedThoughtID != "" || sp.DraftThoughtID != draft.Thought.ID || len(sp.Messages) == 0 {
		t.Fatalf("invalid draft state: %+v", sp)
	}
	archived, e := svc.Commit(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	current, e = capture.GetThought(context.Background(), archived.Thought.ID)
	if e != nil || current.Thought.CaptureDraft || archived.Thought.ID != draft.Thought.ID {
		t.Fatalf("identity/marker changed: %+v %v", current, e)
	}
	ids, e := markdown.ListThoughtIDs(capture.workspace.RootPath)
	if e != nil || len(ids) != 1 {
		t.Fatalf("projections=%v %v", ids, e)
	}
}
func TestArchiveTypedDraftUsesCanonicalCandidate(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.design-doc", 2)
	sp := publishFixture(t, svc, store)
	if sp.Operation.State != "needs_input" {
		t.Fatalf("missing-input preview: %+v", sp.Operation)
	}
	draft, e := svc.SaveDraft(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	current, e := capture.GetThought(context.Background(), draft.Thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	if current.Thought.Document == nil || !current.Thought.CaptureDraft || draft.Thought.ID != sp.ObjectID {
		t.Fatalf("draft identity %+v", current)
	}
	if _, e := os.Stat(filepath.Join(capture.workspace.RootPath, current.Thought.Path)); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.Commit(context.Background(), "s"); !errors.Is(e, ErrArchiveFormatInvalid) {
		t.Fatalf("invalid draft archived: %v", e)
	}
}
func TestArchivePreflightNoGenerationAndParameterCAS(t *testing.T) {
	svc, store, _ := archiveFixture(t, "builtin.research-report", 2)
	gen := &stubDocumentGenerator{}
	svc.documentGenerator = gen
	sp := publishFixture(t, svc, store)
	if gen.calls != 0 || sp.ArchivePreview.Validation.Status != models.ArchiveValidationInvalid {
		t.Fatalf("preflight generated: %d %+v", gen.calls, sp)
	}
	if len(sp.ArchivePreview.Validation.Issues) < 6 {
		t.Fatal("real required keys absent")
	}
	if _, e := svc.PatchParameters("s", sp.SessionGeneration, map[string]string{"unknown": "x"}, nil); !errors.Is(e, ErrCaptureParameters) {
		t.Fatal(e)
	}
	saved, e := svc.PatchParameters("s", sp.SessionGeneration, map[string]string{"audience": "engineers"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := svc.PatchParameters("s", sp.SessionGeneration, map[string]string{"audience": "stale"}, nil); !errors.Is(e, scratchpad.ErrConflict) {
		t.Fatal(e)
	}
	merged := captureContextToScratchpad(ai.CaptureContextResult{DocumentParameters: map[string]string{"audience": "model overwrite"}}, saved)
	if merged.DocumentParameters["audience"] != "engineers" || !merged.ParameterValues["audience"].Confirmed {
		t.Fatalf("confirmed value replaced: %+v", merged)
	}
	cleared, e := svc.PatchParameters("s", saved.SessionGeneration, map[string]string{"audience": ""}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if cleared.SessionContext.DocumentParameters["audience"] != "" || cleared.InputGeneration <= saved.InputGeneration {
		t.Fatal("clear not recorded")
	}
}
func TestArchiveResolvedIssueNotReopenedAndDiagnosticsNonBlocking(t *testing.T) {
	svc, _, capture := archiveFixture(t, "builtin.note", 1)
	sp, e := svc.UpdateSessionContext("s", scratchpad.SessionContext{CandidateProfileID: "builtin.note", CandidateProfileVersion: 1, Issues: mergeIssues(nil, []models.CaptureIssue{{Kind: "contradiction", Message: "different versions"}, {Kind: "limitation", Message: "not verified"}, {Kind: "diagnostic", Message: "provider unavailable"}}, nil)})
	if e != nil {
		t.Fatal(e)
	}
	if len(sp.SessionContext.Conflicts) != 1 {
		t.Fatalf("diagnostics blocked: %+v", sp.SessionContext)
	}
	fixed, e := svc.PatchParameters("s", sp.SessionGeneration, nil, map[string]string{sp.SessionContext.Issues[0].ID: "Use edition 2025, confirmed by user"})
	if e != nil {
		t.Fatal(e)
	}
	merged := captureContextToScratchpad(ai.CaptureContextResult{Conflicts: []string{"different versions"}}, fixed)
	svc.normalizeProfileContext(&merged)
	if len(merged.Conflicts) != 0 || merged.Issues[0].Status != "resolved" {
		t.Fatalf("resolution lost: %+v", merged)
	}
	reloaded, _ := scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions")).Get("s")
	if reloaded.SessionContext.Issues[0].Resolution == "" {
		t.Fatal("resolution not durable")
	}
}
func TestArchiveStaleChangesNeverPublishCandidateHead(t *testing.T) {
	for _, change := range []string{"parameter", "profile", "strategy", "target", "delete"} {
		t.Run(change, func(t *testing.T) {
			svc, store, capture := archiveFixture(t, "builtin.design-doc", 2)
			initial, e := svc.BeginPreview("s", false)
			if e != nil {
				t.Fatal(e)
			}
			p, e := svc.PrepareArchive(context.Background(), initial, nil)
			if e != nil {
				t.Fatal(e)
			}
			if change == "delete" {
				e = store.Delete("s")
			} else {
				_, e = store.Update("s", func(sp *scratchpad.Scratchpad) error {
					switch change {
					case "parameter":
						sp.SessionContext.DocumentParameters = map[string]string{"audience": "new"}
					case "profile":
						sp.SessionContext.CandidateProfileID = "builtin.article"
					case "strategy":
						sp.ArchiveStrategy = scratchpad.ArchiveStrategySupplement
					case "target":
						sp.SourceThoughtID = "different"
					}
					return nil
				})
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e := svc.PublishPreview(initial, p); !errors.Is(e, ErrArchivePreviewStale) {
				t.Fatalf("stale accepted: %v", e)
			}
			if _, e := capture.Documents().Store.Head(initial.ObjectID); !os.IsNotExist(e) {
				t.Fatalf("stale candidate published: %v", e)
			}
		})
	}
}
func TestArchiveMarkerRetryPreservesLaterEdits(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	svc.store = &auditFailCommittedStore{Store: store}
	publishFixture(t, svc, store)
	saved, e := svc.Commit(context.Background(), "s")
	if e == nil {
		t.Fatal("expected injected failure")
	}
	edit := "User's later edit"
	_, e = capture.PatchThought(context.Background(), saved.Thought.ID, "editor", models.ThoughtPatchRequest{Body: &edit}, []byte(`{"body":"User's later edit"}`))
	if e != nil {
		t.Fatal(e)
	}
	restored := NewScratchpadService(scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions")), WithCapture(capture), WithDocumentProfiles(svc.profiles, nil, 10, 1))
	if _, e := restored.Commit(context.Background(), "s"); !errors.Is(e, scratchpad.ErrConflict) {
		t.Fatalf("edited target acknowledged: %v", e)
	}
	current, e := capture.GetThought(context.Background(), saved.Thought.ID)
	if e != nil || !strings.Contains(current.Content.AINotes, edit) {
		t.Fatalf("later edit lost: %+v %v", current, e)
	}
}
func TestArchiveSaveDoesNotConsumeLaterMessages(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	blocked := &auditConcurrentCapture{stubCapture: &stubCapture{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	svc.capture = blocked
	publishFixture(t, svc, store)
	done := make(chan error, 1)
	go func() { _, e := svc.Commit(context.Background(), "s"); done <- e }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("save not started")
	}
	_, e := svc.AppendMessage("s", "user", "LATER INPUT")
	if e != nil {
		t.Fatal(e)
	}
	close(blocked.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	restored, _ := scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions")).Get("s")
	if !strings.Contains(restored.Content, "LATER INPUT") || len(restored.Messages) < 2 {
		t.Fatalf("later message consumed: %+v", restored)
	}
	if active, ok := store.LastActive(); !ok || active.SessionID != "s" {
		t.Fatal("pending input cannot be restored")
	}
}
func TestArchiveDraftRejectsCredentialLinks(t *testing.T) {
	svc, store, _ := archiveFixture(t, "builtin.design-doc", 2)
	_, e := store.Update("s", func(sp *scratchpad.Scratchpad) error {
		sp.SessionContext.CandidateBody = "[unsafe](https://example.com/?token=SECRET)"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	publishFixture(t, svc, store)
	if _, e := svc.SaveDraft(context.Background(), "s"); !errors.Is(e, documentstore.ErrInvalid) {
		t.Fatalf("unsafe draft materialized: %v", e)
	}
}

type astRepairGenerator struct {
	requests []ai.DocumentGenerationRequest
}

func (g *astRepairGenerator) GenerateDocument(ctx context.Context, req ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	g.requests = append(g.requests, req)
	sections := map[string]models.DocumentSection{}
	for _, section := range req.Profile.Sections {
		sections[section.Key] = models.DocumentSection{Content: strings.Repeat("本章节说明独立的研究结果及具体分析边界。", 30)}
	}
	return models.DocumentDraft{Title: "Research findings", Summary: "Research summary", Sections: sections}, nil
}
func TestArchiveFinalASTIssuesEnterBoundedRepair(t *testing.T) {
	svc, store, _ := archiveFixture(t, "builtin.research-report", 2)
	gen := &astRepairGenerator{}
	svc.documentGenerator = gen
	sp, _ := store.Get("s")
	profile, e := svc.profiles.Resolve(models.DocumentProfileRef{ProfileID: "builtin.research-report", Version: 2})
	if e != nil {
		t.Fatal(e)
	}
	values := map[string]string{}
	for _, input := range profile.Inputs {
		if input.Required {
			values[input.Key] = "explicit user decision"
		}
	}
	_, e = svc.PatchParameters("s", sp.SessionGeneration, values, nil, []string{"https://example.org/real-user-provided-source"})
	if e != nil {
		t.Fatal(e)
	}
	result := publishFixture(t, svc, store)
	if len(gen.requests) != 2 {
		t.Fatalf("expected bounded generation + repair, got %d", len(gen.requests))
	}
	found := false
	for _, issue := range gen.requests[1].RepairIssues {
		if issue.Code == "research_chain" {
			found = true
		}
		if issue.Code == "contract_schema" {
			t.Fatal("preview fabricated an invalid contract")
		}
	}
	if !found || result.ArchivePreview.Validation.Status == models.ArchiveValidationValid {
		t.Fatalf("AST errors not repaired/blocked: %+v", gen.requests[1].RepairIssues)
	}
	for _, source := range result.ArchivePreview.Sources.Sources {
		if source.VerificationStatus != "unverified" || source.Locator != "https://example.org/real-user-provided-source" {
			t.Fatalf("fabricated evidence: %+v", source)
		}
	}
}

type finishFailureStore struct {
	*scratchpad.Store
	failed bool
}

func (s *finishFailureStore) Update(id string, change func(*scratchpad.Scratchpad) error) (scratchpad.Scratchpad, error) {
	return s.Store.Update(id, func(sp *scratchpad.Scratchpad) error {
		if e := change(sp); e != nil {
			return e
		}
		if !s.failed && sp.SaveCommand != nil && sp.SaveCommand.State == "completed" {
			s.failed = true
			return errors.New("injected finish failure")
		}
		return nil
	})
}
func TestArchiveFinishFailureNeverReclassifiesCreateAsUpdate(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	svc.store = &finishFailureStore{Store: store}
	publishFixture(t, svc, store)
	result, e := svc.Commit(context.Background(), "s")
	if e == nil {
		t.Fatal("finish failure not injected")
	}
	persisted, _ := store.Get("s")
	if persisted.CommittedThoughtID == "" || persisted.SaveCommand.Update {
		t.Fatal("expected post-marker failure of create command")
	}
	edit := "later manual edit after marker"
	_, e = capture.PatchThought(context.Background(), result.Thought.ID, "editor", models.ThoughtPatchRequest{Body: &edit}, []byte(`{"body":"later manual edit after marker"}`))
	if e != nil {
		t.Fatal(e)
	}
	restored := NewScratchpadService(scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions")), WithCapture(capture), WithDocumentProfiles(svc.profiles, nil, 10, 1))
	if _, e := restored.Commit(context.Background(), "s"); !errors.Is(e, scratchpad.ErrConflict) {
		t.Fatalf("replayed command overwrote edit: %v", e)
	}
	got, e := capture.GetThought(context.Background(), result.Thought.ID)
	if e != nil || !strings.Contains(got.Content.AINotes, edit) {
		t.Fatalf("later edit lost: %+v %v", got, e)
	}
}
func TestArchiveTypedProjectionFailureRestartsWithOneDraft(t *testing.T) {
	for _, point := range []string{"revision_written", "head_published"} {
		t.Run(point, func(t *testing.T) {
			svc, store, capture := archiveFixture(t, "builtin.design-doc", 2)
			initial := publishFixture(t, svc, store)
			flow := capture.Documents()
			flow.Store.Fault = func(stage string) error {
				if stage == point {
					return errors.New("injected " + stage)
				}
				return nil
			}
			if _, e := svc.SaveDraft(context.Background(), "s"); e == nil {
				t.Fatal("fault not triggered")
			}
			flow.Store.Fault = nil
			restored := NewScratchpadService(scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions")), WithCapture(capture), WithDocumentProfiles(svc.profiles, nil, 10, 1))
			saved, e := restored.SaveDraft(context.Background(), "s")
			if e != nil {
				t.Fatal(e)
			}
			if saved.Thought.ID != initial.ObjectID {
				t.Fatal("fault created second identity")
			}
			ids, e := markdown.ListThoughtIDs(capture.workspace.RootPath)
			if e != nil || len(ids) != 1 {
				t.Fatalf("projections=%v %v", ids, e)
			}
			got, e := capture.GetThought(context.Background(), saved.Thought.ID)
			if e != nil || !got.Thought.CaptureDraft || got.Thought.Document == nil {
				t.Fatalf("draft not recovered: %+v %v", got, e)
			}
		})
	}
}
func TestArchiveSaveOutboxUsesStableEventIdentityOnReplay(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	hub := newRecordingEventHub(false)
	capture.eventHub = hub
	publishFixture(t, svc, store)
	_, e := svc.Commit(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	persisted, _ := store.Get("s")
	if len(hub.events) != 2 {
		t.Fatalf("unexpected effects %d", len(hub.events))
	}
	before := []string{}
	for _, ev := range hub.events {
		before = append(before, ev.Data().(models.DomainEvent).EventID)
	}
	if e := capture.ReplayCaptureSave(persisted.SaveCommand.OperationID); e != nil {
		t.Fatal(e)
	}
	for i, ev := range hub.events[2:] {
		if ev.Data().(models.DomainEvent).EventID != before[i] {
			t.Fatal("replay created a different event identity")
		}
	}
}

func TestArchiveConcurrentDiskSavesReuseOneThought(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	hub := newRecordingEventHub(false)
	capture.eventHub = hub
	publishFixture(t, svc, store)
	type outcome struct {
		id  string
		err error
	}
	done := make(chan outcome, 8)
	for i := 0; i < 8; i++ {
		go func() { r, e := svc.Commit(context.Background(), "s"); done <- outcome{r.Thought.ID, e} }()
	}
	id := ""
	for i := 0; i < 8; i++ {
		r := <-done
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id == "" {
			id = r.id
		}
		if r.id != id {
			t.Fatal("concurrent save used different IDs")
		}
	}
	ids, e := markdown.ListThoughtIDs(capture.workspace.RootPath)
	if e != nil || len(ids) != 1 {
		t.Fatalf("projections=%v %v", ids, e)
	}
	if len(hub.events) != 2 {
		t.Fatalf("duplicated first-save effects: %d", len(hub.events))
	}
}

type backlinkFailureCapture struct {
	*Service
	parent string
	failed bool
}

func (c *backlinkFailureCapture) PatchThought(ctx context.Context, id, session string, patch models.ThoughtPatchRequest, raw []byte) (models.ThoughtSnapshot, error) {
	out, e := c.Service.PatchThought(ctx, id, session, patch, raw)
	if e == nil && id == c.parent && !c.failed {
		c.failed = true
		return out, errors.New("injected after backlink")
	}
	return out, e
}
func TestArchiveSupplementBacklinkInterruptionIsIdempotent(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	parent, e := capture.Capture(context.Background(), models.CaptureCommand{Type: "text", Title: "Parent", Content: "Parent material"})
	if e != nil {
		t.Fatal(e)
	}
	svc.capture = &backlinkFailureCapture{Service: capture, parent: parent.Thought.ID}
	if _, e = svc.SetArchiveStrategy("s", scratchpad.ArchiveStrategySupplement, parent.Thought.ID); e != nil {
		t.Fatal(e)
	}
	pending, e := svc.BeginPreview("s", false)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := capture.GetThought(context.Background(), parent.Thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := svc.PrepareArchive(context.Background(), pending, &snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.PublishPreview(pending, preview); e != nil {
		t.Fatal(e)
	}
	first, e := svc.Commit(context.Background(), "s")
	if e == nil {
		t.Fatal("backlink failure not injected")
	}
	restored := NewScratchpadService(scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions")), WithCapture(capture), WithDocumentProfiles(svc.profiles, nil, 10, 1))
	second, e := restored.Commit(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	if first.Thought.ID != second.Thought.ID {
		t.Fatal("supplement duplicated")
	}
	got, e := capture.GetThought(context.Background(), parent.Thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Thought.RelatedThoughtIDs) != 1 || got.Thought.RelatedThoughtIDs[0] != first.Thought.ID {
		t.Fatalf("backlink duplicated: %+v", got.Thought.RelatedThoughtIDs)
	}
	ids, e := markdown.ListThoughtIDs(capture.workspace.RootPath)
	if e != nil || len(ids) != 2 {
		t.Fatalf("parent/child projections=%v %v", ids, e)
	}
	_ = store
}
func TestArchiveTypedDraftPreservesExactFrozenInputsAndSources(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.research-report", 2)
	sp, _ := store.Get("s")
	_, e := svc.PatchParameters("s", sp.SessionGeneration, map[string]string{"audience": "manufacturing engineers", "purpose": "explain integration"}, nil, []string{"https://example.org/standard"})
	if e != nil {
		t.Fatal(e)
	}
	initial := publishFixture(t, svc, store)
	saved, e := svc.SaveDraft(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	got, e := capture.GetThought(context.Background(), saved.Thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	p := initial.ArchivePreview
	if strings.TrimSpace(got.Content.AINotes) != strings.TrimSpace(p.Body) || documentstore.HashJSON(got.Thought.DocumentParameters) != documentstore.HashJSON(p.Parameters) || documentstore.HashJSON(got.Thought.DocumentSources) != documentstore.HashJSON(p.Sources) || got.Thought.DocumentProfile.ContentHash != p.DocumentProfile.ContentHash {
		t.Fatalf("frozen contract lost: %+v", got)
	}
	if _, e = capture.Documents().Export(saved.Thought.ID, got.Thought.Document.RevisionID, "final"); e == nil {
		t.Fatal("private draft exported as final")
	}
	if _, e = capture.Documents().Finalize(saved.Thought.ID, got.Thought.Document.Generation, documentflow.FinalizeRequest{OperationID: "must-not-finalize"}); e == nil {
		t.Fatal("private draft finalized")
	}
}

type traceableResearchGenerator struct{}

func (traceableResearchGenerator) GenerateDocument(_ context.Context, req ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	sections := map[string]models.DocumentSection{}
	for i, section := range req.Profile.Sections {
		sections[section.Key] = models.DocumentSection{Content: fmt.Sprintf("Q1 F1 C1 [S1]: 章节 %d 的受控演练材料。", i) + strings.Repeat("研究演练说明 ISA-95 的业务计划与制造运营边界；这是固定测试材料，来源未核验。", 6)}
	}
	return models.DocumentDraft{Title: "ISA-95 controlled exercise", Sections: sections, References: []models.DocumentReference{{ID: "S1", SourceLink: req.Context.SourceLinks[0]}}}, nil
}
func TestISA95ClosureNeedsInputsThenDraftThenSameThoughtArchive(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.research-report", 2)
	_, e := store.Update("s", func(sp *scratchpad.Scratchpad) error {
		sp.Content = "请将 ISA-95 调研归档"
		sp.SessionContext.CandidateBody = "ISA-95 讨论：企业业务计划与制造运营集成，具体条款尚需核验。"
		sp.SessionContext.ArchiveReadiness = "ready"
		sp.SessionContext.Issues = mergeIssues(nil, []models.CaptureIssue{{Kind: "limitation", Message: "标准原文尚未核验"}, {Kind: "diagnostic", Message: "旧 response_format 错误"}}, nil)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	initial := publishFixture(t, svc, store)
	if initial.Operation.State != "needs_input" {
		t.Fatal("model readiness bypassed actual input requirements")
	}
	draft, e := svc.SaveDraft(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	current, _ := store.Get("s")
	values := map[string]string{"research_question": "ISA-95 集成边界是什么", "purpose": "内部理解与讨论", "scope": "业务计划与制造运营", "comparison": "不适用", "cutoff": "2026-10-23", "audience": "制造业工程师"}
	_, e = svc.PatchParameters("s", current.SessionGeneration, values, nil, []string{"https://example.org/controlled-isa95-fixture"})
	if e != nil {
		t.Fatal(e)
	}
	svc.documentGenerator = traceableResearchGenerator{}
	pending, e := svc.BeginPreview("s", false)
	if e != nil {
		t.Fatal(e)
	}
	if e = svc.PreviewAndArchive(context.Background(), pending); e != nil {
		t.Fatal(e)
	}
	ready, _ := store.Get("s")
	if ready.ArchivePreview.Validation.Status != models.ArchiveValidationValid {
		t.Fatalf("controlled fixture failed strict gate: %+v", ready.ArchivePreview.Validation)
	}
	saved, e := svc.Commit(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	if saved.Thought.ID != draft.Thought.ID {
		t.Fatal("supplemented draft created a second Thought")
	}
	got, e := capture.GetThought(context.Background(), saved.Thought.ID)
	if e != nil || got.Thought.CaptureDraft {
		t.Fatal("draft marker not cleared", e)
	}
	if got.Thought.DocumentSources.Sources[0].VerificationStatus != "unverified" {
		t.Fatal("test source falsely marked verified")
	}
	documents := capture.Documents()
	snap, e := documents.Get(saved.Thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	revision, e := documents.Store.Revision(saved.Thought.ID, snap.Head.WorkingRevisionID)
	if e != nil {
		t.Fatal(e)
	}
	if revision.Projection == nil || revision.Projection.Thought.CaptureDraft {
		t.Fatal("archive failed to clear the canonical draft permission marker")
	}
	report, e := documents.Validate(context.Background(), saved.Thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation)
	if e != nil {
		t.Fatal(e)
	}
	request := documentflow.FinalizeRequest{RevisionID: report.RevisionID, ReportID: report.ReportID, OperationID: "controlled-isa95-final", DeliveryLevel: "proposal"}
	for _, issue := range report.Issues {
		if issue.HumanConfirmation {
			request.Confirmations = append(request.Confirmations, models.DocumentConfirmation{IssueID: issue.ID, Reason: "Controlled fixture reviewed; sources remain unverified, proposal use only."})
		}
	}
	if _, e = documents.Finalize(saved.Thought.ID, snap.Head.Generation, request); e != nil {
		t.Fatalf("archived draft remains impossible to finalize: %v", e)
	}

}

func TestArchiveOlderPreviewCannotReplaceNewCandidateHead(t *testing.T) {
	svc, _, capture := archiveFixture(t, "builtin.design-doc", 2)
	first, e := svc.BeginPreview("s", false)
	if e != nil {
		t.Fatal(e)
	}
	old, e := svc.PrepareArchive(context.Background(), first, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.AppendMessage("s", "user", "New generation material"); e != nil {
		t.Fatal(e)
	}
	next, e := svc.BeginPreview("s", false)
	if e != nil {
		t.Fatal(e)
	}
	newer, e := svc.PrepareArchive(context.Background(), next, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.PublishPreview(next, newer); e != nil {
		t.Fatal(e)
	}
	before, e := capture.Documents().Store.Head(next.ObjectID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.PublishPreview(first, old); !errors.Is(e, ErrArchivePreviewStale) {
		t.Fatalf("older preview accepted: %v", e)
	}
	after, e := capture.Documents().Store.Head(next.ObjectID)
	if e != nil || before.WorkingRevisionID != after.WorkingRevisionID || before.Generation != after.Generation {
		t.Fatalf("older preview changed HEAD: %+v %+v %v", before, after, e)
	}
}

func TestArchiveQueuedContextCannotOverwriteNewlyConfirmedParameters(t *testing.T) {
	svc, store, _ := archiveFixture(t, "builtin.research-report", 2)
	provider := &stubCaptureContextProvider{result: ai.CaptureContextResult{DocumentParameters: map[string]string{"audience": "model stale value"}}}
	svc.contextAI = provider
	_, _ = store.Update("s", func(sp *scratchpad.Scratchpad) error {
		sp.ArchiveIntent = scratchpad.ArchiveIntentLLM
		sp.SessionContext.ArchiveIntent = scratchpad.ArchiveIntentLLM
		return nil
	})
	before, _ := store.Get("s")
	request := captureContextEnrichRequest{SessionID: "s", ContentAtSchedule: before.Content, ExistingContext: before.SessionContext, InputGeneration: before.InputGeneration}
	confirmed, e := svc.PatchParameters("s", before.SessionGeneration, map[string]string{"audience": "user confirmed"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if confirmed.ArchiveIntent == scratchpad.ArchiveIntentLLM {
		t.Fatal("input change retained authorization to automatically archive a new generation")
	}
	svc.handleCaptureContextEnrichRequest(request)
	after, _ := store.Get("s")
	if provider.calls != 0 || after.SessionGeneration != confirmed.SessionGeneration || after.SessionContext.DocumentParameters["audience"] != "user confirmed" {
		t.Fatalf("queued stale context modified confirmed input: %+v", after)
	}
	svc.markContextEnrichmentFailed("s", before.Content, errors.New("old provider failure"), before.InputGeneration)
	after, _ = store.Get("s")
	if after.SessionGeneration != confirmed.SessionGeneration {
		t.Fatal("late failure modified the new input generation")
	}
}
