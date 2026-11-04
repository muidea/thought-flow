package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/testutil/llmfixture"
)

func TestPlannerEvidenceUsesAbsoluteIndicesAndKeepsFailuresPrivate(t *testing.T) {
	c := ds.CaptureSnapshot{Messages: []ds.Message{{Role: "user", Text: "Old user scope."}, {Role: "ai", Text: "PRIVATE ASSISTANT PROPOSAL"}, {Role: "user", Text: "PRIVATE USER EVIDENCE: preserve exact bytes. 原文要求。"}}}
	for _, tc := range []struct {
		name   string
		source ds.Source
		reason string
	}{
		{"ordinal", ds.Source{Message: 1, Quote: "原文要求。"}, ""},
		{"correct", ds.Source{Message: 2, Quote: "原文要求。"}, ""},
		{"missing-quote", ds.Source{Message: 0}, "quote_empty"},
		{"assistant", ds.Source{Message: 1, Quote: "PRIVATE ASSISTANT PROPOSAL"}, "not_user_message"},
		{"paraphrase", ds.Source{Message: 2, Quote: "PRIVATE MODEL PARAPHRASE"}, "quote_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			shared := ds.SharedDesign{Facts: []ds.Fact{{ID: "REQ-001", Kind: "requirement", Name: "Scope", Content: "Preserve the original scope", Status: "confirmed", Source: tc.source}}}
			content, _ := json.Marshal(shared)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req struct {
					Input []struct{ Content string } `json:"input"`
					Text  json.RawMessage            `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Input) != 2 {
					t.Error("missing request", err)
					return
				}
				var input struct {
					Capture  ds.CaptureSnapshot
					Contract struct {
						Indices []int `json:"user_message_indices"`
					} `json:"source_contract"`
				}
				if err := json.Unmarshal([]byte(req.Input[1].Content), &input); err != nil || !reflect.DeepEqual(input.Capture, c) || !reflect.DeepEqual(input.Contract.Indices, []int{0, 2}) {
					t.Error("wrong index space or modified capture", err, input)
				}
				if !strings.Contains(req.Input[0].Content, "never count only user turns") || string(req.Text) != `{"format":{"type":"json_object"}}` {
					t.Error("missing evidence/JSON contract")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "evidence-request")
				_ = json.NewEncoder(w).Encode(llmfixture.Response(string(content)))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			out, err := p.PlanDocumentSet(WithRequiredJSON(context.Background()), c, ds.Builtins()[0], nil)
			if calls.Load() != 1 || CanAutomaticallyRetry(err) {
				t.Fatal("replayed planner", calls.Load(), err)
			}
			if tc.reason == "" {
				if err != nil || len(out.Facts) != 1 || out.Facts[0].Source.Message != 2 || out.Facts[0].Source.Quote != tc.source.Quote || out.Facts[0].Content != shared.Facts[0].Content {
					t.Fatal("failed exact evidence recovery", out, err)
				}
				return
			}
			var failure ProviderError
			var evidence ds.FactEvidenceError
			if !errors.Is(err, ds.ErrInvalid) || !errors.As(err, &evidence) || !errors.As(err, &failure) || failure.Reason != tc.reason || failure.Code != "thoughtflow.document_set.invalid_evidence" || failure.RequestID != "evidence-request" || failure.StatusCode != 200 || len(out.Facts) != 0 {
				t.Fatal("missing contract error and request identity", out, err)
			}
			diagnostic, ok := JSONResponseErrorDiagnostic(err)
			if !ok || diagnostic.Content != string(content) {
				t.Fatal("missing unmodified private response", diagnostic)
			}
			encoded, _ := json.Marshal(err)
			if strings.Contains(err.Error()+string(encoded), "PRIVATE") {
				t.Fatal("evidence leaked into public errors", err)
			}
		})
	}
}
