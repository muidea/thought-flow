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
