package documentsetflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentprofile"
	ds "thoughtflow/internal/pkg/documentset"
)

func TestSetPlanningGenerationAndReviewRequireJSONAfterConversationFallback(t *testing.T) {
	for _, wire := range []string{"json", "json-with-sse-header", "sse"} {
		t.Run(wire, func(t *testing.T) { testSetRequiredJSONWithWire(t, wire) })
	}
}

func testSetRequiredJSONWithWire(t *testing.T, wire string) {
	var archive atomic.Bool
	var plans, members, reviews, plain atomic.Int32
	_, shared := fixture()
	// The planner counts user turns and reports 1, but the second user is at
	// absolute index 2. Exact source bytes are already present in the capture.
	for i := range shared.Facts {
		shared.Facts[i].Source.Message = 1
	}
	for key, condition := range shared.Conditions {
		condition.Source.Message = 1
		shared.Conditions[key] = condition
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ResponseFormat json.RawMessage            `json:"text"`
			Messages       []struct{ Content string } `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) != 2 {
			t.Error("invalid LLM request", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !archive.Load() {
			if len(req.ResponseFormat) != 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
				return
			}
			plain.Add(1)
			_, _ = w.Write([]byte(`{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"assistant_reply\":\"answer\"}"}]}]}`))
			return
		}
		if string(req.ResponseFormat) != `{"format":{"type":"json_object"}}` {
			t.Error("archive inherited the conversation fallback decision")
		}
		var result any
		system, input := req.Messages[0].Content, req.Messages[1].Content
		switch {
		case strings.HasPrefix(system, "Plan a coordinated"):
			plans.Add(1)
			result = shared
		case strings.HasPrefix(system, "Generate structured"):
			members.Add(1)
			var profile struct {
				Keys []string `json:"allowed_section_keys"`
			}
			var source ai.DocumentSourceContext
			profileJSON := strings.SplitN(strings.TrimPrefix(input, "Profile JSON:\n"), "\n\nParameters JSON:", 2)[0]
			sourceParts := strings.SplitN(input, "\n\nSource context JSON:\n", 2)
			if len(sourceParts) != 2 || json.Unmarshal([]byte(profileJSON), &profile) != nil || json.Unmarshal([]byte(strings.SplitN(sourceParts[1], "\n\nPrevious draft JSON:", 2)[0]), &source) != nil {
				t.Error("invalid generation payload")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			request := ai.DocumentGenerationRequest{Context: source}
			for _, key := range profile.Keys {
				request.Profile.Sections = append(request.Profile.Sections, documentprofile.SectionSpec{Key: key})
			}
			result, _ = (&generator{}).GenerateDocument(r.Context(), request)
		case strings.HasPrefix(system, "Review the final"):
			reviews.Add(1)
			var review struct {
				Checklist []ds.ReviewItem `json:"checklist"`
				Members   []ds.Artifact   `json:"members"`
			}
			if err := json.Unmarshal([]byte(input), &review); err != nil {
				t.Error(err)
			}
			result, _, _ = (reviewer{}).ReviewDocumentSet(r.Context(), review.Checklist, review.Members, ds.SharedDesign{}, ds.CaptureSnapshot{})
		default:
			t.Error("unexpected archive call")
		}
		content, _ := json.Marshal(result)
		if wire != "json" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if wire == "sse" {
			_, _ = w.Write([]byte(llmfixture.SSE(string(content))))
		} else {
			_ = json.NewEncoder(w).Encode(llmfixture.Response(string(content)))
		}
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "set-required-json", Timeout: time.Minute}, appconfig.EmbeddingConfig{})
	if _, err := provider.BuildCaptureContext(context.Background(), ai.CaptureContextRequest{Content: "conversation"}); err != nil {
		t.Fatal(err)
	}
	archive.Store(true)
	s, snapshot, _ := setup(t, provider, provider)
	snapshot.Messages = append([]ds.Message{{Role: "user", Text: "Earlier unrelated discussion."}, {Role: "ai", Text: "Assistant context is not user confirmation."}}, snapshot.Messages...)
	s.opts.Planner = provider
	p := ds.Builtins()[0]
	zero := uint64(0)
	plan, err := s.Plan(context.Background(), snapshot.SessionID, PlanRequest{OperationID: "plan-json", Name: "JSON archive", ExpectedGeneration: &zero, CaptureGeneration: &snapshot.Generation, ProfileID: p.ID, ProfileVersion: p.Version, ProfileHash: p.Hash})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := s.Store.Plan(plan.SetID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range persisted.Shared.Facts {
		if fact.Source.Message != 2 || fact.Source.Quote != "" {
			t.Fatal("plan has invalid source address", fact.Source.Message)
		}
	}
	start(t, s, plan, "run-json")
	run := wait(t, s, "run-json")
	if run.State != "committed" || run.Candidate == nil || len(run.Candidate.Members) != 6 {
		t.Fatal("strict archive failed", run.State, run.Error)
	}
	if plans.Load() != 1 || members.Load() != 6 || reviews.Load() == 0 || plain.Load() != 1 {
		t.Fatal("missing archive coverage or plain-text replay", plans.Load(), members.Load(), reviews.Load(), plain.Load())
	}
}
