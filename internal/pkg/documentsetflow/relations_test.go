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
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/testutil/designfixture"
	"thoughtflow/internal/testutil/llmfixture"
)

func TestEngineeringPlannerRelationContractCommitsValidAndIsolatesInvalid(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[valid], func(t *testing.T) {
			_, shared := designfixture.Capture()
			shared.Relations[0].ID = "REL-003"
			if !valid {
				shared.Relations[0].From, shared.Relations[0].To = shared.Relations[0].To, shared.Relations[0].From
			}
			raw, _ := json.Marshal(shared)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-ID", "relation-plan-request")
				// The updated gateway may supply JSON with its upstream SSE header.
				_ = json.NewEncoder(w).Encode(llmfixture.Response(string(raw)))
			}))
			defer server.Close()
			p := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			g := &engineeringGenerator{}
			s, previous, capture := engineeringSetup(t, g)
			s.opts.Planner = p
			profile := ds.Builtins()[2]
			zero := uint64(0)
			plan, err := s.Plan(context.Background(), capture.SessionID, PlanRequest{OperationID: "typed-plan", SetID: previous.SetID, ExpectedGeneration: &zero, CaptureGeneration: &capture.Generation, ProfileID: profile.ID, ProfileVersion: profile.Version, ProfileHash: profile.Hash})
			if calls.Load() != 1 {
				t.Fatal("planner replayed", calls.Load())
			}
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				start(t, s, plan, "typed-run")
				run := wait(t, s, "typed-run")
				if run.State != "committed" || run.Candidate == nil || len(run.Candidate.Members) != 6 {
					t.Fatal("valid contract failed", run.State, run.Error)
				}
				return
			}
			var failure ai.ProviderError
			if !errors.Is(err, ds.ErrInvalid) || !errors.As(err, &failure) || failure.Code != "thoughtflow.document_set.invalid_relation" || failure.Reason != "endpoint_kind_mismatch" || failure.RequestID != "relation-plan-request" {
				t.Fatal("lost typed relation reason", err)
			}
			if _, e := s.Store.Plan(previous.SetID, "typed-plan"); !os.IsNotExist(e) {
				t.Fatal("invalid plan saved", e)
			}
			head, e := s.Store.HeadOrEmpty(previous.SetID)
			if e != nil || head.Generation != 0 || head.Working != "" || len(g.requests) != 0 {
				t.Fatal("invalid relation generated or published members", e)
			}
			files, e := filepath.Glob(filepath.Join(s.Store.Root, documentsetstore.Private(previous.SetID), "diagnostics", "planning", "typed-plan-*.json"))
			if e != nil || len(files) != 1 {
				t.Fatal("missing private diagnostic", files, e)
			}
			data, e := os.ReadFile(files[0])
			if e != nil || !strings.Contains(string(data), "REL-003") || !strings.Contains(string(data), "relation-plan-request") {
				t.Fatal("lost private relation output", e)
			}
		})
	}
}
