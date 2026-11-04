package documentsetflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/models"
)

func TestJSONDiagnosticWriteFailurePreservesProviderErrorAndPrivateBoundary(t *testing.T) {
	const content = "<unsupported>PRIVATE-FAILED-RESPONSE</unsupported>"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "failed-response-request")
		_ = json.NewEncoder(w).Encode(llmfixture.Response(content))
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
	_, failure := provider.PlanDocumentSet(context.Background(), ds.CaptureSnapshot{}, ds.Profile{}, nil)
	if failure == nil {
		t.Fatal("invalid markup accepted")
	}
	root, outside := t.TempDir(), t.TempDir()
	s := New(root, Options{})
	t.Cleanup(s.Close)
	private := filepath.Join(root, documentsetstore.Private("set-a"))
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(private, "diagnostics")); err != nil {
		t.Fatal(err)
	}
	err, path := s.persistJSONFailure("set-a", "operation-a", "planning", failure)
	var providerErr ai.ProviderError
	if path != "" || !errors.As(err, &providerErr) || providerErr.RequestID != "failed-response-request" || !strings.Contains(err.Error(), "persist private JSON response diagnostic") || strings.Contains(err.Error(), "PRIVATE-FAILED-RESPONSE") || ai.CanAutomaticallyRetry(err) {
		t.Fatalf("diagnostic write failure lost the generation error or exposed output: %s %v", path, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 || calls.Load() != 1 {
		t.Fatal("diagnostic escaped the private workspace or replayed the request", entries, calls.Load(), err)
	}
}

func TestArchiveGenerationProtocolFailureIsPrivateAndExplicitRetryCanSaveDraft(t *testing.T) {
	const invalid = `{"title":"Design","sections":{"assumptions":{"content":"PRIVATE-GENERATION-CANARY"},"references":{"content":"not section keys"}}}`
	var calls atomic.Int32
	var fixed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Input []struct{ Content string }
			Text  json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 2 || string(request.Text) != `{"format":{"type":"json_object"}}` {
			t.Error("invalid archive generation request", err)
			w.WriteHeader(400)
			return
		}
		content := invalid
		if fixed.Load() {
			var profile struct {
				Keys []string `json:"allowed_section_keys"`
			}
			input := strings.SplitN(strings.TrimPrefix(request.Input[1].Content, "Profile JSON:\n"), "\n\nParameters JSON:", 2)[0]
			if err := json.Unmarshal([]byte(input), &profile); err != nil || len(profile.Keys) == 0 {
				t.Error("missing exact profile section keys", err)
			}
			draft := models.DocumentDraft{Title: "Extracted design proposal", Sections: map[string]models.DocumentSection{}}
			for _, key := range profile.Keys {
				draft.Sections[key] = models.DocumentSection{Content: "Proposed " + key + ": retain authenticated orders, atomic persistence and checkpoint recovery. Verification remains unexecuted and approval unresolved."}
			}
			raw, _ := json.Marshal(draft)
			content = string(raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-ID", "generation-protocol-request")
		json.NewEncoder(w).Encode(llmfixture.Response(content))
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
	s, plan := proposedArchivePlan(t, &engineeringGenerator{})
	s.opts.Generator = provider
	_, err := s.Start(RunRequest{ArchiveDraft: true, OperationID: "generation-protocol-failure", SetID: plan.SetID, PlanID: plan.ID, PlanHash: plan.Hash, ExpectedGeneration: &plan.BaseGeneration, CaptureGeneration: &plan.Capture.Generation})
	if err != nil {
		t.Fatal(err)
	}
	run := wait(t, s, "generation-protocol-failure")
	public, _ := json.Marshal(run)
	if run.State != "failed_terminal" || run.Candidate != nil || calls.Load() != 1 || strings.Contains(string(public), "PRIVATE-GENERATION-CANARY") || !strings.Contains(run.Error, "invalid_document_sections") || !strings.Contains(run.Error, "generation-protocol-request") {
		t.Fatal("invalid output accepted, leaked or automatically retried", run.State, run.Error, calls.Load())
	}
	files, err := filepath.Glob(filepath.Join(s.Store.Root, documentsetstore.Private(plan.SetID), "diagnostics", "generation", run.ID+"-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("no private generation evidence", files, err)
	}
	var diagnostic struct {
		Response ai.JSONResponseDiagnostic `json:"response"`
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || json.Unmarshal(raw, &diagnostic) != nil || diagnostic.Response.Content != invalid || diagnostic.Response.RequestID != "generation-protocol-request" {
		t.Fatal("raw generation evidence lost", err)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("generation diagnostic is not private", err)
	}
	fixed.Store(true)
	_, err = s.Retry(run.ID, RetryRequest{OperationID: "generation-protocol-fixed", ExpectedGeneration: &plan.BaseGeneration, AcknowledgeUncertain: true})
	if err != nil {
		t.Fatal(err)
	}
	retried := wait(t, s, "generation-protocol-fixed")
	if retried.State != "draft_saved" || retried.Draft == nil || len(retried.Draft.Members) != 7 || retried.Candidate != nil || calls.Load() != 7 {
		t.Fatal("explicit retry did not preserve draft isolation", retried.State, retried.Error, calls.Load())
	}
	head, err := s.Store.HeadOrEmpty(plan.SetID)
	if err != nil || head.Generation != 0 || head.Working != "" || head.Finalized != "" {
		t.Fatal("unapproved draft was published", head, err)
	}
	old, err := s.Store.Run(run.ID)
	if err != nil || old.State != "failed_terminal" || old.Error != run.Error {
		t.Fatal("retry replaced old failure evidence", err)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatal("retry removed failure evidence", err)
	}
}

func TestPlannerEvidenceFailurePersistsPrivatelyAndPublishesNothing(t *testing.T) {
	_, shared := fixture()
	shared.Facts[0].Source.Quote = "PRIVATE INVALID PLANNER EVIDENCE"
	raw, _ := json.Marshal(shared)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-ID", "planner-evidence-request")
		_ = json.NewEncoder(w).Encode(llmfixture.Response(string(raw)))
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
	s, snapshot, previous := setup(t, provider, provider)
	s.opts.Planner = provider
	profile := ds.Builtins()[0]
	zero := uint64(0)
	_, err := s.Plan(context.Background(), snapshot.SessionID, PlanRequest{SetID: previous.SetID, OperationID: "plan-invalid-evidence", ExpectedGeneration: &zero, CaptureGeneration: &snapshot.Generation, ProfileID: profile.ID, ProfileVersion: profile.Version, ProfileHash: profile.Hash})
	var failure ai.ProviderError
	if !errors.Is(err, ds.ErrInvalid) || !errors.As(err, &failure) || failure.RequestID != "planner-evidence-request" || failure.Reason != "quote_not_found" || calls.Load() != 1 || strings.Contains(err.Error(), "PRIVATE INVALID") {
		t.Fatal("lost evidence gate, leaked or replayed", calls.Load(), err)
	}
	if _, err := s.Store.Plan(previous.SetID, "plan-invalid-evidence"); !os.IsNotExist(err) {
		t.Fatal("failed evidence saved a plan", err)
	}
	head, err := s.Store.HeadOrEmpty(previous.SetID)
	if err != nil || head.Generation != 0 || head.Working != "" {
		t.Fatal("invalid evidence published", head, err)
	}
	files, err := filepath.Glob(filepath.Join(s.Store.Root, documentsetstore.Private(previous.SetID), "diagnostics", "planning", "plan-invalid-evidence-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("no private diagnostic", files, err)
	}
	content, err := os.ReadFile(files[0])
	if err != nil || !strings.Contains(string(content), "PRIVATE INVALID PLANNER EVIDENCE") || !strings.Contains(string(content), "planner-evidence-request") {
		t.Fatal("raw failure was lost", err)
	}
}
