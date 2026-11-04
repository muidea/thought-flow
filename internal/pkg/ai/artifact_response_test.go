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
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/testutil/designfixture"
)

func TestPlannerArtifactApplicabilityThroughJSONFallback(t *testing.T) {
	capture, shared := designfixture.Capture()
	raw, _ := json.Marshal(shared)
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"empty-placeholder", `"not_applicable":{}`, true},
		{"nonempty-reason", `"not_applicable":{"build":"PRIVATE-REASON"}`, false},
		{"unknown-empty-field", `"unexpected":{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input map[string]json.RawMessage
			_ = json.Unmarshal(raw, &input)
			var facts []map[string]json.RawMessage
			_ = json.Unmarshal(input["facts"], &facts)
			for _, fact := range facts {
				if artifact := fact["artifact"]; len(artifact) > 0 {
					fact["artifact"] = json.RawMessage(string(artifact[:len(artifact)-1]) + "," + tc.extra + "}")
				}
			}
			input["facts"], _ = json.Marshal(facts)
			body, _ := json.Marshal(input)
			content := "<escape>" + string(body) + "</escape>"
			var formatted, plain atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if _, ok := request["text"]; ok {
					formatted.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(formatUnsupportedResponse))
					return
				}
				plain.Add(1)
				w.Header().Set("X-Request-ID", "artifact-response")
				_ = json.NewEncoder(w).Encode(llmfixture.Response(content))
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "artifact-test", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			for attempt := 0; attempt < 2; attempt++ {
				out, err := provider.PlanDocumentSet(context.Background(), capture, ds.Builtins()[2], nil)
				if tc.valid {
					if err != nil || !reflect.DeepEqual(out, shared) {
						t.Fatal("empty placeholder changed or blocked the shared contract", err)
					}
					plan, err := ds.BuildPlan("engineering", ds.Builtins()[2], capture, out, 0, 1)
					if err != nil || len(ds.EngineeringIssues(plan)) != 0 {
						t.Fatal("valid contract did not pass deterministic gates", err)
					}
				} else {
					var failure ProviderError
					if !errors.As(err, &failure) || CanAutomaticallyRetry(err) || strings.Contains(err.Error(), "PRIVATE-") {
						t.Fatal("invalid artifact accepted, retried or exposed", err)
					}
					diagnostic, ok := JSONResponseErrorDiagnostic(err)
					if !ok || diagnostic.Content != content || diagnostic.RequestID != "artifact-response" {
						t.Fatal("rejected declaration lost private response evidence")
					}
				}
			}
			if formatted.Load() != 1 || plain.Load() != 2 {
				t.Fatal("fallback capability cache or no-retry policy regressed", formatted.Load(), plain.Load())
			}
		})
	}
}
