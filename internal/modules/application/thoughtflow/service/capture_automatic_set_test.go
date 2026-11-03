package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muidea/magicCommon/event"
	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetflow"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/testutil/designfixture"
)

type captureArchivePlanner struct {
	calls            atomic.Int32
	clarify          bool
	entered, release chan struct{}
}

func (p *captureArchivePlanner) PlanDocumentSet(ctx context.Context, capture ds.CaptureSnapshot, profile ds.Profile, _ *ds.SharedDesign) (ds.SharedDesign, error) {
	n := p.calls.Add(1)
	if profile.Purpose != ds.EngineeringPurpose {
		return ds.SharedDesign{}, fmt.Errorf("incorrect automatic profile: %s", profile.ID)
	}
	if p.entered != nil {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ds.SharedDesign{}, ctx.Err()
		}
	}
	_, shared := designfixture.Capture()
	if p.clarify && n == 1 {
		shared.Questions = []ds.Question{{ID: "confirm-recovery", Kind: "question", Message: "中断后应保留已确认订单吗？", Blocking: true}}
	}
	return shared, nil
}

func automaticSetFixture(t *testing.T, planner *captureArchivePlanner) (*Service, *scratchpad.Store) {
	t.Helper()
	s, _ := documentHTTPFixture(t, "design")
	s.documentSets.Close()
	store := scratchpad.New(filepath.Join(s.workspace.RootPath, "automatic-sessions"))
	s.scratchpad = store
	hub := event.NewHub(16)
	t.Cleanup(func() { hub.Terminate(context.Background()) })
	profile := ds.Builtins()[2]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		material := req.Messages[len(req.Messages)-1].Content
		if !strings.Contains(material, `"output_kind":"document_set"`) || !strings.Contains(material, profile.ID) || !strings.Contains(material, profile.Hash) {
			t.Error("automatic profile catalog is incomplete")
		}
		intent := "none"
		userContent := strings.SplitN(strings.SplitN(material, "Accumulated user content:\n", 2)[1], "\n\nReturn the capture response JSON now.", 2)[0]
		if strings.HasSuffix(strings.TrimSpace(userContent), "请将以上讨论归档") {
			intent = "llm"
		} else if strings.Contains(material, "Pending accepted archive awaiting an answer: true") && strings.HasSuffix(strings.TrimSpace(userContent), "是，中断后保留已确认订单。") {
			intent = "llm"
		}
		result := map[string]any{"assistant_reply": "将按上下文匹配的格式整理设计。", "candidate_profile_id": profile.ID, "candidate_profile_version": profile.Version, "candidate_body": "Order requests require authentication and durable recovery.", "archive_intent": intent, "goal": "Recoverable orders"}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(raw)}}}})
	}))
	t.Cleanup(server.Close)
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{APIKey: "test", BaseURL: server.URL, ChatModel: "fixture", Timeout: time.Minute}, appconfig.EmbeddingConfig{})
	s.scratchpadSvc = capturebiz.NewScratchpadService(store, capturebiz.WithEventHub(hub), capturebiz.WithCaptureContextProvider(provider), capturebiz.WithCapture(s.captureService), capturebiz.WithDocumentProfiles(s.documentProfiles, engineeringHTTPGenerator{}, 10, 0))
	s.config.DocumentProfiles.Enabled = true
	s.configureDocumentSets() // Installs the production unified catalog callback.
	s.documentSets.Close()
	s.documentSets = documentsetflow.New(s.workspace.RootPath, documentsetflow.Options{Enabled: true, Planner: planner, Generator: engineeringHTTPGenerator{}, Reviewer: engineeringHTTPReviewer{}, Snapshot: s.captureSetSnapshot, SnapshotUnderLease: func(id string) (ds.CaptureSnapshot, error) { return s.captureSetSnapshotUnderLease(id, true) }, ModelHash: "automatic-test"})
	s.scratchpadSvc.SetArchiveScheduler(s.startCaptureArchive)
	return s, store
}
func sendAutomaticCapture(t *testing.T, s *Service, text string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"role": "user", "text": text})
	req := httptest.NewRequest("POST", "/api/capture/sessions/engineering-capture/messages", strings.NewReader(string(raw)))
	res := httptest.NewRecorder()
	s.handleSessionMessage(req.Context(), res, req)
	if res.Code != http.StatusOK {
		t.Fatal(res.Code, res.Body.String())
	}
}
func awaitAutomaticCapture(t *testing.T, store *scratchpad.Store, condition func(scratchpad.Scratchpad) bool) scratchpad.Scratchpad {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var sp scratchpad.Scratchpad
	for time.Now().Before(deadline) {
		sp, _ = store.Get("engineering-capture")
		if condition(sp) {
			return sp
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("capture did not converge: operation=%+v, run=%s, context=%+v", sp.Operation, sp.DocumentSetRunID, sp.SessionContext)
	return sp
}
func TestCaptureChatArchiveAutomaticallyMatchesPlansAndGeneratesSet(t *testing.T) {
	planner := &captureArchivePlanner{}
	s, store := automaticSetFixture(t, planner)
	capture, _ := designfixture.Capture()
	sendAutomaticCapture(t, s, capture.Messages[0].Text)
	awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.SessionContext.FrozenSetProfile != nil })
	sendAutomaticCapture(t, s, "请将以上讨论归档")
	sp := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.DocumentSetRunID != "" })
	var run ds.Run
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = s.documentSets.Store.Run(sp.DocumentSetRunID)
		if run.State == "committed" || run.State == "failed_terminal" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	if planner.calls.Load() != 1 || len(run.Candidate.Members) != 6 {
		t.Fatal("unexpected planning/generation", planner.calls.Load(), len(run.Candidate.Members))
	}
	for _, a := range run.Candidate.Members {
		if _, body, err := markdown.ReadThought(s.workspace.RootPath, a.DocumentID); err != nil || body.AINotes == "" {
			t.Fatal("missing generated thought", a.Key, err)
		}
	}
	reloaded, err := scratchpad.New(filepath.Join(s.workspace.RootPath, "automatic-sessions")).Get(sp.SessionID)
	if err != nil || reloaded.DocumentSetRunID != run.ID {
		t.Fatal("run binding not durable", err)
	}
	// Replaying the accepted operation must reuse the set and run, without a second planner call.
	if err := s.startCaptureArchive(sp); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(sp.SessionID)
	if after.DocumentSetRunID != run.ID || planner.calls.Load() != 1 {
		t.Fatal("archive replay duplicated generation")
	}
}
func TestCaptureArchiveClarificationContinuesFromChatWithoutGenerateClick(t *testing.T) {
	planner := &captureArchivePlanner{clarify: true}
	s, store := automaticSetFixture(t, planner)
	capture, _ := designfixture.Capture()
	sendAutomaticCapture(t, s, capture.Messages[0].Text)
	awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.SessionContext.FrozenSetProfile != nil })
	sendAutomaticCapture(t, s, "请将以上讨论归档")
	before := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.Operation != nil && sp.Operation.State == "needs_input" })
	if !strings.Contains(before.Operation.Error, "保留已确认订单") {
		t.Fatal("missing concrete clarification", before.Operation.Error)
	}
	// No new archive command and no profile/plan/run HTTP calls from the client.
	sendAutomaticCapture(t, s, "是，中断后保留已确认订单。")
	after := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool {
		return sp.DocumentSetRunID != "" && sp.DocumentSetRunID != before.DocumentSetRunID
	})
	if after.DocumentSetID != before.DocumentSetID || planner.calls.Load() != 2 {
		t.Fatal("clarification failed to resume the same set")
	}
}

func TestAutomaticSetPlanningCannotBindOverNewUserInput(t *testing.T) {
	planner := &captureArchivePlanner{entered: make(chan struct{}), release: make(chan struct{})}
	s, store := automaticSetFixture(t, planner)
	capture, _ := designfixture.Capture()
	sendAutomaticCapture(t, s, capture.Messages[0].Text)
	awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.SessionContext.FrozenSetProfile != nil })
	sendAutomaticCapture(t, s, "请将以上讨论归档")
	select {
	case <-planner.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("planning not started")
	}
	sendAutomaticCapture(t, s, "暂时保留新增需求，不归档新输入。")
	close(planner.release)
	after := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.Operation != nil && sp.Operation.State == "conflicted" })
	if after.DocumentSetRunID != "" || after.DocumentSetPlanID != "" || !strings.Contains(after.Content, "新增需求") {
		t.Fatal("stale planning consumed a newer turn", after.DocumentSetRunID)
	}
}

func TestMenuArchiveAutomaticallyClassifiesSetBeforeContextEnrichment(t *testing.T) {
	planner := &captureArchivePlanner{}
	s, store := automaticSetFixture(t, planner)
	capture, _ := designfixture.Capture()
	text := capture.Messages[0].Text + "\n\n请将以上讨论归档"
	if _, err := store.Save(scratchpad.Scratchpad{SessionID: capture.SessionID, Content: text, Messages: []scratchpad.Message{{Role: "user", Text: text}}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/capture/sessions/"+capture.SessionID+"/archive/preview", strings.NewReader(`{"commit_after_preview":true}`))
	res := httptest.NewRecorder()
	s.handleArchivePreview(req.Context(), res, req)
	if res.Code != http.StatusAccepted && res.Code != http.StatusOK {
		t.Fatal(res.Code, res.Body.String())
	}
	sp := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.DocumentSetRunID != "" })
	run, err := s.documentSets.Store.Run(sp.DocumentSetRunID)
	if err != nil || run.Plan.Profile.Purpose != ds.EngineeringPurpose || planner.calls.Load() != 1 {
		t.Fatal("menu archive bypassed automatic selection", err)
	}
	if sp.Operation.InputGeneration != sp.InputGeneration {
		t.Fatal("operation was not advanced to extracted context generation")
	}
}

func TestCapturePendingArchiveDoesNotResumeWhenUserDefers(t *testing.T) {
	planner := &captureArchivePlanner{clarify: true}
	s, store := automaticSetFixture(t, planner)
	capture, _ := designfixture.Capture()
	sendAutomaticCapture(t, s, capture.Messages[0].Text)
	awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.SessionContext.FrozenSetProfile != nil })
	sendAutomaticCapture(t, s, "请将以上讨论归档")
	before := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.Operation != nil && sp.Operation.State == "needs_input" })
	sendAutomaticCapture(t, s, "暂时不要归档，先继续讨论。")
	after := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool {
		return sp.InputGeneration > before.InputGeneration && sp.SessionContext.Goal != ""
	})
	if after.DocumentSetRunID != before.DocumentSetRunID || planner.calls.Load() != 1 || after.ArchiveIntent != scratchpad.ArchiveIntentNone {
		t.Fatal("deferred archive was resumed")
	}
}
