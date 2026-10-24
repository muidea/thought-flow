package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

func inferredResearchParameters() map[string]string {
	return map[string]string{
		"research_question": "ISA-95 如何组织企业与制造运营集成", "purpose": "个人知识整理与后续核验",
		"scope": "本次会话讨论的层级与信息模型", "comparison": "不适用（本次未进行对比）",
		"cutoff": "2026-10-24 会话材料整理日期", "audience": "本次会话读者",
	}
}

func TestAutomaticArchiveStopsUnknownGenerationAndExplicitRetryWritesOneThought(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	profile, err := svc.profiles.Resolve(models.DocumentProfileRef{ProfileID: "builtin.research-report", Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := (automaticResearchGenerator{}).GenerateDocument(context.Background(), ai.DocumentGenerationRequest{Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(draft)
	var formatted, plain atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After-Ms", "1")
		if _, found := request["response_format"]; found {
			formatted.Add(1)
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
			return
		}
		if plain.Add(1) == 1 {
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"error":{"code":"upstream","message":"bootstrap: HTTP 403 (upstream)"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer server.Close()
	svc.contextAI = automaticResearchContext{}
	svc.documentGenerator = ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
	if _, err := store.Update("s", func(sp *scratchpad.Scratchpad) error {
		sp.Content = "调研 ISA-95 并归档"
		sp.Messages = []scratchpad.Message{{Role: "user", Text: sp.Content}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc.SetArchiveScheduler(func(sp scratchpad.Scratchpad) error { return svc.PreviewAndArchive(context.Background(), sp) })
	initial, _ := store.Get("s")
	svc.handleCaptureContextEnrichRequest(captureContextEnrichRequest{SessionID: "s", InputGeneration: initial.InputGeneration, ContentAtSchedule: initial.Content})
	sp, err := store.Get("s")
	if err != nil || sp.Operation == nil || sp.Operation.State != "failed" || sp.CommittedThoughtID != "" || plain.Load() != 1 {
		t.Fatalf("unknown generation replayed: %+v %v", sp, err)
	}
	notes, e := capture.ListThoughts(context.Background())
	if e != nil || len(notes) != 0 {
		t.Fatalf("failed output was written: %+v %v", notes, e)
	}
	retry, e := svc.BeginPreview("s", true)
	if e != nil {
		t.Fatal(e)
	}
	if e = svc.PreviewAndArchive(context.Background(), retry); e != nil {
		t.Fatal(e)
	}
	sp, err = store.Get("s")
	if err != nil || sp.Operation == nil || sp.Operation.State != "saved" || sp.CommittedThoughtID == "" {
		t.Fatalf("automatic save failed: %+v err=%v", sp, err)
	}
	if formatted.Load() != 1 || plain.Load() != 2 {
		t.Fatalf("unbounded generation: formatted=%d plain=%d", formatted.Load(), plain.Load())
	}
	replayed, err := svc.Commit(context.Background(), "s")
	if err != nil || replayed.Thought.ID != sp.CommittedThoughtID {
		t.Fatalf("replay created a different identity: %+v err=%v", replayed, err)
	}
	thoughts, err := capture.ListThoughts(context.Background())
	if err != nil || len(thoughts) != 1 || thoughts[0].ID != sp.CommittedThoughtID {
		t.Fatalf("duplicate or missing Thought: %+v err=%v", thoughts, err)
	}
}

type automaticResearchContext struct{}

func (automaticResearchContext) BuildCaptureContext(_ context.Context, req ai.CaptureContextRequest) (ai.CaptureContextResult, error) {
	for _, profile := range req.AvailableProfiles {
		if profile.ID == "builtin.research-report" && profile.Version == 2 {
			return ai.CaptureContextResult{
				AssistantReply: "将依据当前上下文自动整理归档；尚未核验的内容保留说明。",
				Topic:          "ISA-95 制造运营集成", Goal: "整理本次 ISA-95 讨论供个人回顾",
				CandidateTitle: "ISA-95 制造运营集成", CandidateBody: "ISA-95 划分企业计划与制造运营职责；本次未核验标准原文。",
				CandidateProfileID: profile.ID, CandidateProfileVersion: profile.Version,
				CandidateDocumentFamily: profile.Family, ProfileContractHash: profile.ContractHash,
				DocumentParameters: map[string]string{"research_question": inferredResearchParameters()["research_question"], "scope": inferredResearchParameters()["scope"]}, ArchiveIntent: "llm", ArchiveStrategy: "new",
			}, nil
		}
	}
	return ai.CaptureContextResult{}, fmt.Errorf("research profile not supplied")
}

type automaticResearchGenerator struct{ unsafe bool }

func (g automaticResearchGenerator) GenerateDocument(_ context.Context, req ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	sections := map[string]models.DocumentSection{}
	for i, spec := range req.Profile.Sections {
		sections[spec.Key] = models.DocumentSection{Content: fmt.Sprintf("Q1 F1 C1 章节 %d：%s。根据本次会话整理 ISA-95 职责边界与信息模型，仅作为未核验的初步认识，不宣称已经阅读标准原文。", i, spec.Key)}
	}
	if g.unsafe {
		sections["findings"] = models.DocumentSection{Content: "Q1 F1 C1 [外部图片](https://example.org/image?token=secret)"}
	}
	return models.DocumentDraft{Title: "ISA-95 制造运营集成", Sections: sections, Assumptions: []string{"目标读者按本次会话的一般技术背景编写"}}, nil
}

func TestAutomaticCaptureMatchesFillsAndArchivesWithoutConfirmations(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	svc.contextAI = automaticResearchContext{}
	svc.documentGenerator = automaticResearchGenerator{}
	_, err := store.Update("s", func(sp *scratchpad.Scratchpad) error {
		sp.Content = "为我调研ISA95\n\n为我进行归档"
		sp.Messages = []scratchpad.Message{{Role: "user", Text: "为我调研ISA95"}, {Role: "ai", Text: "ISA-95 的主要职责边界与信息模型……"}, {Role: "user", Text: "为我进行归档"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduled := 0
	svc.SetArchiveScheduler(func(sp scratchpad.Scratchpad) error {
		scheduled++
		for _, value := range sp.SessionContext.ParameterValues {
			if value.Confirmed {
				t.Fatal("inferred parameter falsely marked as user confirmed")
			}
		}
		if len(sp.SessionContext.MissingProfileInputs) != 0 || sp.SessionContext.ArchiveReadiness != "ready" {
			t.Fatalf("inferred fields require repeated confirmation: %+v", sp.SessionContext)
		}
		return svc.PreviewAndArchive(context.Background(), sp)
	})
	initial, _ := store.Get("s")
	svc.handleCaptureContextEnrichRequest(captureContextEnrichRequest{
		SessionID: "s", InputGeneration: initial.InputGeneration, ContentAtSchedule: initial.Content,
	})
	sp, err := store.Get("s")
	if err != nil || scheduled != 1 || sp.Operation == nil || sp.Operation.State != "saved" || sp.CommittedThoughtID == "" {
		t.Fatalf("automatic archive did not materialize: %+v scheduled=%d err=%v", sp, scheduled, err)
	}
	thought, err := capture.GetThought(context.Background(), sp.CommittedThoughtID)
	if err != nil || thought.Thought.DocumentProfile.Family != models.DocumentFamilyResearch || thought.Thought.CaptureDraft {
		t.Fatalf("wrong persisted document type: %+v err=%v", thought, err)
	}
	if thought.Thought.DocumentParameters["research_question"] != inferredResearchParameters()["research_question"] || !strings.Contains(thought.Content.AINotes, unverifiedResearchNotice) {
		t.Fatal("inferred parameters or evidence disclosure not persisted")
	}
	if !strings.Contains(thought.Content.AINotes, "编写假设") || !strings.Contains(thought.Content.AINotes, "audience = 本次会话读者") {
		t.Fatal("automatic writing assumptions were not disclosed")
	}
	if _, err := os.Stat(filepath.Join(capture.workspace.RootPath, thought.Thought.Path)); err != nil {
		t.Fatalf("Thought Markdown absent: %v", err)
	}
	// Read after reopening the disk stores, then replay: exactly one identity.
	restoredStore := scratchpad.New(filepath.Join(capture.workspace.RootPath, "sessions"))
	restored := NewScratchpadService(restoredStore, WithCapture(capture), WithDocumentProfiles(svc.profiles, svc.documentGenerator, 10, 1))
	replayed, err := restored.Commit(context.Background(), "s")
	if err != nil || replayed.Thought.ID != sp.CommittedThoughtID {
		t.Fatalf("replay identity=%s err=%v", replayed.Thought.ID, err)
	}
	// Canonical validation and finalization continue enforcing evidence. Capture
	// warnings must never mutate a report into a successful formal delivery.
	documents := capture.Documents()
	snapshot, err := documents.Get(sp.CommittedThoughtID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := documents.Validate(context.Background(), sp.CommittedThoughtID, snapshot.Head.WorkingRevisionID, snapshot.Head.Generation)
	if err != nil || report.Status != "failed" {
		t.Fatalf("unverified document passed formal validation: %+v err=%v", report, err)
	}
	if _, err := documents.Finalize(sp.CommittedThoughtID, snapshot.Head.Generation, documentflow.FinalizeRequest{
		RevisionID: report.RevisionID, ReportID: report.ReportID, OperationID: "no-evidence-final", DeliveryLevel: "proposal",
	}); err == nil {
		t.Fatal("unverified working document finalized without evidence")
	}
}

func TestAutomaticArchiveStillBlocksMissingContextConflictsAndUnsafeLinks(t *testing.T) {
	for _, tc := range []string{"missing", "conflict", "unsafe"} {
		t.Run(tc, func(t *testing.T) {
			svc, store, capture := archiveFixture(t, "builtin.research-report", 2)
			svc.documentGenerator = automaticResearchGenerator{unsafe: tc == "unsafe"}
			_, err := svc.UpdateSessionContext("s", scratchpad.SessionContext{
				CandidateTitle: "ISA-95", CandidateBody: "ISA-95 discussion", CandidateProfileID: "builtin.research-report", CandidateProfileVersion: 2,
				DocumentParameters: inferredResearchParameters(),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Update("s", func(sp *scratchpad.Scratchpad) error {
				if tc == "missing" {
					sp.SessionContext.DocumentParameters["research_question"] = ""
				}
				if tc == "conflict" {
					sp.SessionContext.Conflicts = []string{"Two incompatible deployment decisions"}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			pending, err := svc.BeginPreview("s", true)
			if err != nil {
				t.Fatal(err)
			}
			err = svc.PreviewAndArchive(context.Background(), pending)
			current, _ := store.Get("s")
			if current.CommittedThoughtID != "" {
				t.Fatalf("blocked input archived: %+v", current)
			}
			if _, e := os.Stat(filepath.Join(capture.workspace.RootPath, "thoughts")); e == nil {
				t.Fatal("blocked input wrote Thought projection")
			}
			if err == nil && current.ArchivePreview.Validation.Status != models.ArchiveValidationInvalid {
				t.Fatalf("failure not surfaced: %+v", current)
			}
			if _, e := svc.Commit(context.Background(), "s"); e == nil {
				t.Fatal("manual commit bypassed blocked preview")
			}
		})
	}
}

func TestMenuArchiveExtractsContextBeforeAsyncReplyArrives(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.note", 1)
	svc.contextAI = automaticResearchContext{}
	svc.documentGenerator = automaticResearchGenerator{}
	_, err := store.Update("s", func(sp *scratchpad.Scratchpad) error {
		sp.Content = "为我调研 ISA-95，按当前上下文归档"
		sp.SessionContext = scratchpad.SessionContext{}
		sp.ArchiveIntent = scratchpad.ArchiveIntentMenu
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := svc.BeginPreview("s", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.PreviewAndArchive(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Get("s")
	if current.Operation.State != "saved" || current.CommittedThoughtID == "" {
		t.Fatalf("menu did not finish: %+v", current)
	}
	thought, err := capture.GetThought(context.Background(), current.CommittedThoughtID)
	if err != nil || thought.Thought.DocumentProfile.Family != models.DocumentFamilyResearch {
		t.Fatalf("early click silently used default note: %+v err=%v", thought, err)
	}
}

type delayedArchiveContext struct{ entered, release chan struct{} }

func (p delayedArchiveContext) BuildCaptureContext(ctx context.Context, req ai.CaptureContextRequest) (ai.CaptureContextResult, error) {
	close(p.entered)
	select {
	case <-p.release:
		return automaticResearchContext{}.BuildCaptureContext(ctx, req)
	case <-ctx.Done():
		return ai.CaptureContextResult{}, ctx.Err()
	}
}

func TestAutomaticArchiveExtractionCannotConsumeLaterEdits(t *testing.T) {
	svc, store, _ := archiveFixture(t, "builtin.note", 1)
	provider := delayedArchiveContext{make(chan struct{}), make(chan struct{})}
	svc.contextAI = provider
	svc.documentGenerator = automaticResearchGenerator{}
	_, err := store.Update("s", func(sp *scratchpad.Scratchpad) error { sp.SessionContext = scratchpad.SessionContext{}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	pending, err := svc.BeginPreview("s", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.PreviewAndArchive(ctx, pending) }()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("context extraction did not start")
	}
	if _, err := svc.AppendDraft("s", scratchpad.Draft{TitleSet: "New manual title"}); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrArchivePreviewStale) {
			t.Fatalf("stale context accepted: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("context extraction did not finish")
	}
	current, _ := store.Get("s")
	if current.Draft.TitleSet != "New manual title" || current.SaveCommand != nil || current.CommittedThoughtID != "" {
		t.Fatalf("new edit lost or stale archive saved: %+v", current)
	}
}

func TestAsyncContextResponseContinuesAlreadyAcceptedMenuArchive(t *testing.T) {
	svc, store, _ := archiveFixture(t, "builtin.note", 1)
	svc.documentGenerator = automaticResearchGenerator{}
	pending, err := svc.BeginPreview("s", true)
	if err != nil {
		t.Fatal(err)
	}
	scheduled := 0
	svc.SetArchiveScheduler(func(sp scratchpad.Scratchpad) error {
		scheduled++
		return svc.PreviewAndArchive(context.Background(), sp)
	})
	// The independent context response wins the race with the menu worker's
	// extraction. Its model does not have to detect another archive instruction.
	_, err = svc.updateSessionContextAt("s", scratchpad.SessionContext{
		CandidateProfileID: "builtin.research-report", CandidateProfileVersion: 2,
		CandidateTitle: "ISA-95", CandidateBody: "Current ISA-95 discussion",
		DocumentParameters: inferredResearchParameters(), ArchiveIntent: scratchpad.ArchiveIntentMenu,
	}, true, &pending.InputGeneration)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.Get("s")
	if scheduled != 1 || current.CommittedThoughtID == "" || current.Operation.State != "saved" {
		t.Fatalf("accepted menu authorization was lost: %+v scheduled=%d", current, scheduled)
	}
	if _, err := svc.PublishPreview(pending, scratchpad.ArchivePreview{}); !errors.Is(err, ErrArchivePreviewStale) {
		t.Fatalf("old menu worker can overwrite new context: %v", err)
	}
}

func TestAutomaticArchivePreservesReopenedThoughtWithoutNewUserInput(t *testing.T) {
	svc, _, capture := archiveFixture(t, "builtin.note", 1)
	svc.contextAI = automaticResearchContext{}
	original, err := capture.Capture(context.Background(), models.CaptureCommand{Type: models.ThoughtTypeText, Title: "Existing note", Content: "Original archived content"})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := svc.ReopenFromThought(context.Background(), original.Thought.ID, "reopened")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateSessionContext(sp.SessionID, scratchpad.SessionContext{CandidateTitle: "Updated title", CandidateBody: "New body from the optional editor"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := svc.BeginPreview(sp.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PreviewAndArchive(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	current, err := capture.GetThought(context.Background(), original.Thought.ID)
	if err != nil || !strings.Contains(current.Content.AINotes, "Original archived content") || !strings.Contains(current.Content.AINotes, "New body from the optional editor") {
		t.Fatalf("reopened content was lost: %+v err=%v", current, err)
	}
	if current.Thought.DocumentProfile != nil && current.Thought.DocumentProfile.Family != models.DocumentFamilyNote {
		t.Fatal("reopened note was reclassified without a new request")
	}
}
