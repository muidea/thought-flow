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
	"thoughtflow/internal/testutil/designfixture"
	"thoughtflow/internal/testutil/llmfixture"
)

func TestEngineeringPlannerUsesRegistryAndRejectsTypedRelationsWithoutReplay(t *testing.T) {
	for _, tc := range []struct{ name, kind, from, to, reason string }{
		{"valid", "realizes", "MOD-ORDER", "REQ-ORDER", ""},
		{"reversed", "realizes", "REQ-ORDER", "MOD-ORDER", "endpoint_kind_mismatch"},
		{"field-mapping-is-not-uses-contract", "uses_contract", "API-ORDER", "ENTITY-ORDER", "endpoint_kind_mismatch"},
		{"unknown-kind", "PRIVATE INVALID KIND", "MOD-ORDER", "REQ-ORDER", "unknown_kind"},
		{"missing-target", "realizes", "MOD-ORDER", "MISSING", "missing_to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture, shared := designfixture.Capture()
			shared.Relations[0] = ds.Relation{ID: "REL-003", Kind: tc.kind, From: tc.from, To: tc.to}
			content, _ := json.Marshal(shared)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req struct {
					Input []struct{ Content string } `json:"input"`
					Text  json.RawMessage            `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Input) != 2 {
					t.Error(err)
					return
				}
				var input struct {
					Rules  []ds.RelationRule `json:"relation_contract"`
					Schema map[string]any    `json:"response_schema"`
				}
				if err := json.Unmarshal([]byte(req.Input[1].Content), &input); err != nil || !reflect.DeepEqual(input.Rules, ds.EngineeringRelationRules()) {
					t.Error("registry was not sent", err, input.Rules)
				}
				defs := input.Schema["$defs"].(map[string]any)
				props := defs["Relation"].(map[string]any)["properties"].(map[string]any)
				var enum []string
				for _, kind := range props["kind"].(map[string]any)["enum"].([]any) {
					enum = append(enum, kind.(string))
				}
				want := []string{"realizes", "constrains", "uses_contract", "verifies", "depends_on", "supersedes"}
				if !reflect.DeepEqual(enum, want) || !strings.Contains(req.Input[0].Content, "never member keys") || !strings.Contains(req.Input[0].Content, "record that mapping in interface fields") || string(req.Text) != `{"format":{"type":"json_object"}}` {
					t.Error("wrong typed relation/JSON guidance", enum)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-ID", "typed-relation-request")
				_, _ = w.Write([]byte(llmfixture.SSE(string(content))))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			out, err := p.PlanDocumentSet(WithRequiredJSON(context.Background()), capture, ds.Builtins()[2], nil)
			if calls.Load() != 1 || CanAutomaticallyRetry(err) {
				t.Fatal("replayed planner", calls.Load(), err)
			}
			if tc.reason == "" {
				if err != nil || !reflect.DeepEqual(out.Relations, shared.Relations) {
					t.Fatal("changed valid relations", out, err)
				}
				if _, err := ds.BuildPlan("engineering-relations", ds.Builtins()[2], capture, out, 0, 1); err != nil {
					t.Fatal("planner accepted a contract the gate rejected", err)
				}
				return
			}
			var failure ProviderError
			var relation ds.RelationValidationError
			if !errors.Is(err, ds.ErrInvalid) || !errors.As(err, &relation) || !errors.As(err, &failure) || failure.Code != "thoughtflow.document_set.invalid_relation" || failure.RequestID != "typed-relation-request" || failure.StatusCode != 200 || failure.Reason != tc.reason || relation.RelationID != "REL-003" || len(out.Facts) != 0 {
				t.Fatal("lost typed contract diagnostic or used invalid output", out, err)
			}
			diagnostic, ok := JSONResponseErrorDiagnostic(err)
			if !ok || diagnostic.Content != string(content) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("lost private result or leaked it", err)
			}
		})
	}
}

func TestNonEngineeringRelationSchemasKeepTheirExistingSemantics(t *testing.T) {
	input := DocumentSetPlanningInput(ds.CaptureSnapshot{}, ds.Builtins()[1], nil)
	if _, ok := input["relation_contract"]; ok {
		t.Fatal("engineering rules leaked into research")
	}
	defs := input["response_schema"].(map[string]any)["$defs"].(map[string]any)
	props := defs["Relation"].(map[string]any)["properties"].(map[string]any)
	if _, ok := props["kind"].(map[string]any)["enum"]; ok {
		t.Fatal("research relations constrained")
	}
}
