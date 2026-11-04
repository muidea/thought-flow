package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"github.com/muidea/magicCommon/event"
	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetflow"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/testutil/designfixture"
)

type captureArchivePlanner struct {
	calls            atomic.Int32
	clarify          bool
	incompleteFirst  bool
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
	if p.incompleteFirst && n == 1 {
		shared.Facts[0].Status = "proposed"
	}
	return shared, nil
}

func automaticSetFixture(t *testing.T, planner documentsetflow.Planner) (*Service, *scratchpad.Store) {
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
			} `json:"input"`
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
		json.NewEncoder(w).Encode(llmfixture.Response(string(raw)))
	}))
	t.Cleanup(server.Close)
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{APIKey: "test", BaseURL: server.URL, ChatModel: "fixture", Timeout: time.Minute}, appconfig.EmbeddingConfig{})
	s.scratchpadSvc = capturebiz.NewScratchpadService(store, capturebiz.WithEventHub(hub), capturebiz.WithCaptureContextProvider(provider), capturebiz.WithCaptureJSONDiagnostics(s.workspace.RootPath), capturebiz.WithCapture(s.captureService), capturebiz.WithDocumentProfiles(s.documentProfiles, engineeringHTTPGenerator{}, 10, 0))
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
func TestCaptureArchiveAutomaticallySavesUnconfirmedDesignWithoutGenerateClick(t *testing.T) {
	planner := &captureArchivePlanner{clarify: true, incompleteFirst: true}
	s, store := automaticSetFixture(t, planner)
	capture, _ := designfixture.Capture()
	sendAutomaticCapture(t, s, capture.Messages[0].Text)
	awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.SessionContext.FrozenSetProfile != nil })
	sendAutomaticCapture(t, s, "请将以上讨论归档")
	sp := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool {
		if sp.DocumentSetRunID == "" {
			return false
		}
		run, e := s.documentSets.Store.Run(sp.DocumentSetRunID)
		return e == nil && (run.State == "draft_saved" || run.State == "failed_terminal")
	})
	run, e := s.documentSets.Store.Run(sp.DocumentSetRunID)
	if e != nil || run.State != "draft_saved" || run.Draft == nil || len(run.Draft.Members) == 0 {
		t.Fatal(run.State, run.Error, e)
	}
	listed, e := s.captureService.ListThoughts(context.Background())
	if e != nil || !run.Draft.NotesReady {
		t.Fatal("automatic archive is not visible in Notes", len(listed), e)
	}
	listedIDs := map[string]bool{}
	for _, thought := range listed {
		listedIDs[thought.ID] = true
	}
	for _, member := range run.Draft.Members {
		if !listedIDs[member.ThoughtID] {
			t.Fatal("archived Thought missing from Notes", member.Key)
		}
		snapshot, err := s.captureService.GetThought(context.Background(), member.ThoughtID)
		if err != nil || snapshot.Content.AINotes != member.Body || snapshot.Thought.DesignDraft == nil || !snapshot.Thought.CaptureDraft {
			t.Fatal("ordinary note reader cannot read archived design", member.Key, err)
		}
	}
	if sp.Operation.State == "needs_input" || sp.Operation.Clarification != nil {
		t.Fatal("archive still demands schema confirmation")
	}
	if len(run.Plan.Shared.Questions) < 2 || run.Candidate != nil {
		t.Fatal("unapproved draft escaped formal gates")
	}
	reloaded, e := scratchpad.New(filepath.Join(s.workspace.RootPath, "automatic-sessions")).Get(sp.SessionID)
	if e != nil || reloaded.DocumentSetRunID != run.ID {
		t.Fatal("reload lost draft binding", e)
	}
	if e = s.startCaptureArchive(sp); e != nil {
		t.Fatal(e)
	}
	if planner.calls.Load() != 1 {
		t.Fatal("draft replay re-planned")
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
	before := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool {
		if sp.DocumentSetRunID == "" {
			return false
		}
		r, e := s.documentSets.Store.Run(sp.DocumentSetRunID)
		return e == nil && r.State == "draft_saved"
	})
	sendAutomaticCapture(t, s, "暂时不要归档，先继续讨论。")
	after := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool {
		return sp.InputGeneration > before.InputGeneration && sp.SessionContext.Goal != ""
	})
	if after.DocumentSetRunID != before.DocumentSetRunID || planner.calls.Load() != 1 || after.ArchiveIntent != scratchpad.ArchiveIntentNone {
		t.Fatal("deferred archive was resumed")
	}
}

func TestCaptureArchiveRequiresJSONAndValidatesPlannerOutput(t *testing.T) {
	for _, scenario := range []string{"wrapped", "escape", "escape-pair", "artifact-empty-placeholder", "html", "incomplete", "escape-pair-incomplete", "syntax-error-and-unfinished-tail", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			valid := scenario == "wrapped" || scenario == "escape" || scenario == "escape-pair" || scenario == "artifact-empty-placeholder"
			var formatted, plain atomic.Int32
			var planningInputBytes atomic.Int64
			capture, shared := designfixture.Capture()
			raw, err := json.Marshal(shared)
			if err != nil {
				t.Fatal(err)
			}
			content := "<think>Check scope. An example, not the answer: {\"facts\":[]}.</think>\n```json\n" + string(raw) + "\n```"
			switch scenario {
			case "escape":
				content = "<escape>" + string(raw)
			case "escape-pair":
				content = "<escape>" + string(raw) + "</escape>"
			case "artifact-empty-placeholder":
				if !strings.Contains(string(raw), `"artifact":{`) {
					t.Fatal("fixture has no artifact contract")
				}
				content = "<escape>" + strings.Replace(string(raw), `"artifact":{`, `"artifact":{"not_applicable":{},`, 1) + "</escape>"
			case "incomplete", "escape-pair-incomplete":
				content = `<escape>{"facts":[{"id":"REQ-1","content":"PRIVATE-PLANNING-RESPONSE-CANARY"}],"`
				if scenario == "escape-pair-incomplete" {
					content += "</escape>"
				}
			case "html":
				content = "<html><body>PRIVATE-PLANNING-RESPONSE-CANARY " + string(raw) + "</body></html>"
			case "syntax-error-and-unfinished-tail":
				content = `<escape>{"facts":[{"acceptance":{"recovery_cases":["PRIVATE-PLANNING-RESPONSE-CANARY"}},{"id":"next"}],"open_questions":["`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "archive-planner-response")
				if _, exists := req["text"]; exists {
					formatted.Add(1)
				} else {
					plain.Add(1)
					t.Error("archive planner omitted json_object response_format")
				}
				if string(req["text"]) != `{"format":{"type":"json_object"}}` {
					t.Error("incorrect response_format", string(req["text"]))
				}
				if scenario == "unsupported" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
					return
				}
				var messages []struct{ Content string }
				if err := json.Unmarshal(req["input"], &messages); err != nil || len(messages) == 0 {
					t.Error("missing planner messages", err)
					return
				}
				planningInputBytes.Store(int64(len(messages[len(messages)-1].Content)))
				_ = json.NewEncoder(w).Encode(llmfixture.Response(content))
			}))
			defer server.Close()
			provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "wrapped-planner", Timeout: time.Minute}, appconfig.EmbeddingConfig{})
			s, store := automaticSetFixture(t, provider)
			sendAutomaticCapture(t, s, capture.Messages[0].Text)
			awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.SessionContext.FrozenSetProfile != nil })
			sendAutomaticCapture(t, s, "请将以上讨论归档")
			sp := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool {
				return sp.DocumentSetRunID != "" || (sp.Operation != nil && sp.Operation.State == "failed")
			})
			if !strings.Contains(sp.Content, capture.Messages[0].Text) {
				t.Fatal("archive lost user input")
			}
			if scenario == "unsupported" {
				if sp.DocumentSetRunID != "" || sp.Operation == nil || sp.Operation.State != "failed" || !strings.Contains(sp.Operation.Error, "thoughtflow.ai.conversion_unsupported") {
					t.Fatal("unsupported provider must stop archiving without starting generation", sp.Operation)
				}
			} else if !valid {
				if sp.DocumentSetRunID != "" || !strings.Contains(sp.Operation.Error, "plan document set") || !strings.Contains(sp.Operation.Error, "thoughtflow.ai.invalid_json_response") {
					t.Fatal("invalid planner output was used", sp.Operation)
				}
				shape := "html_document"
				if scenario == "incomplete" || scenario == "escape-pair-incomplete" {
					shape = "incomplete_json_object"
					if !strings.Contains(sp.Operation.Error, "reason=incomplete_json") {
						t.Fatal("incomplete JSON was not diagnosed", sp.Operation.Error)
					}
				}
				if scenario == "syntax-error-and-unfinished-tail" {
					shape = "invalid_json_object"
					wantOffset := strings.Index(content, "}") + 1
					if !strings.Contains(sp.Operation.Error, fmt.Sprintf("json_offset=%d", wantOffset)) || !strings.Contains(sp.Operation.Error, "reason=invalid_json") || strings.Contains(sp.Operation.Error, "invalid character '<'") {
						t.Fatal("wrapper concealed the actual syntax error", sp.Operation.Error)
					}
				}
				if !strings.Contains(sp.Operation.Error, "response_shape="+shape) || !strings.Contains(sp.Operation.Error, "request_id=archive-planner-response") || strings.Contains(sp.Operation.Error, "PRIVATE-PLANNING-RESPONSE-CANARY") {
					t.Fatal("missing safe response diagnostics", sp.Operation.Error)
				}
				markers, err := filepath.Glob(filepath.Join(s.workspace.RootPath, ".content-registry/private/document-sets/*/planning/*.json"))
				if err != nil || len(markers) != 1 {
					t.Fatal("missing planning failure marker", markers, err)
				}
				var marker map[string]string
				markerRelative, _ := filepath.Rel(s.workspace.RootPath, markers[0])
				if err := objectlocation.ReadJSON(s.workspace.RootPath, filepath.ToSlash(markerRelative), &marker); err != nil || marker["diagnostic_path"] == "" {
					t.Fatal("private diagnostic is not linked to the failed operation", marker, err)
				}
				var diagnostic struct {
					OperationID string                    `json:"operation_id"`
					Stage       string                    `json:"stage"`
					Response    ai.JSONResponseDiagnostic `json:"response"`
				}
				if err := objectlocation.ReadJSON(s.workspace.RootPath, marker["diagnostic_path"], &diagnostic); err != nil || diagnostic.OperationID != sp.Operation.ID || diagnostic.Stage != "planning" || diagnostic.Response.Content != content || diagnostic.Response.ResponseHash != objectlocation.Hash([]byte(content)) {
					t.Fatal("original model completion was not preserved", err)
				}
				info, err := os.Stat(filepath.Join(s.workspace.RootPath, marker["diagnostic_path"]))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("private diagnostic permissions are not 0600", err)
				}
				res := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/api/resources/"+filepath.Base(marker["diagnostic_path"])+"/content", nil)
				s.handleTopicWorkspace(req.Context(), res, req)
				if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "PRIVATE-PLANNING-RESPONSE-CANARY") {
					t.Fatal("private diagnostic exposed by the resource API", res.Code)
				}
			} else {
				var run ds.Run
				deadline := time.Now().Add(60 * time.Second)
				for time.Now().Before(deadline) {
					run, _ = s.documentSets.Store.Run(sp.DocumentSetRunID)
					if run.State == "committed" || run.State == "failed_terminal" {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if run.State != "committed" || len(run.Candidate.Members) != 6 {
					t.Fatal("wrapped planner output did not generate the set", run.State, run.Error)
				}
				if run.Plan.PlanningCall == nil {
					t.Fatal("missing persisted planner call")
				}
				if int64(run.Plan.PlanningCall.InputBytes) != planningInputBytes.Load() {
					t.Fatalf("planner payload budget mismatch: persisted=%d actual=%d", run.Plan.PlanningCall.InputBytes, planningInputBytes.Load())
				}
				for _, member := range run.Candidate.Members {
					if _, body, err := markdown.ReadThought(s.workspace.RootPath, member.DocumentID); err != nil || body.AINotes == "" {
						t.Fatal("generated thought is unreadable", member.Key, err)
					}
				}
				diagnostics, err := filepath.Glob(filepath.Join(s.workspace.RootPath, ".content-registry/private/document-sets/*/diagnostics/planning/*.json"))
				if err != nil || len(diagnostics) != 0 {
					t.Fatal("successful plan retained a raw response", diagnostics, err)
				}
			}
			if formatted.Load() != 1 || plain.Load() != 0 {
				t.Fatal("unexpected automatic planner replay", formatted.Load(), plain.Load())
			}
		})
	}
}
