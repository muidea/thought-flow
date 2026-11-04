package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
)

func TestPlannerRequestContainsExactResponseContractAndCompleteInput(t *testing.T) {
	capture := ds.CaptureSnapshot{Messages: []ds.Message{{Role: "user", Text: "Keep all requirements and exact 中文 evidence."}}}
	profile := ds.Builtins()[2]
	previous := &ds.SharedDesign{Facts: []ds.Fact{{ID: "REQ-existing", Content: "previous confirmed content"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Content string } `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			t.Error("invalid request", err)
			return
		}
		var input struct {
			Capture  ds.CaptureSnapshot `json:"capture"`
			Profile  ds.Profile         `json:"profile"`
			Previous ds.SharedDesign    `json:"previous"`
			Schema   map[string]any     `json:"response_schema"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			t.Error(err)
			return
		}
		if mustJSON(input.Capture) != mustJSON(capture) || mustJSON(input.Profile) != mustJSON(profile) || mustJSON(input.Previous) != mustJSON(previous) {
			t.Error("schema guidance must not remove frozen scope, profile or prior facts")
		}
		defs := input.Schema["$defs"].(map[string]any)
		root := defs["SharedDesign"].(map[string]any)
		props := root["properties"].(map[string]any)
		if input.Schema["$ref"] != "#/$defs/SharedDesign" || root["additionalProperties"] != false || props["open_questions"] == nil || props["facts"] == nil {
			t.Error("wrong response root schema", input.Schema)
		}
		for _, invalid := range []string{"questions", "profile", "schema_version", "set_id", "name"} {
			if _, exists := props[invalid]; exists {
				t.Error("input metadata exposed as output contract", invalid)
			}
		}
		for typ, keys := range map[string][]string{
			"Fact":               {"source", "acceptance", "artifact", "fields", "provenance"},
			"Question":           {"kind", "message", "resolution_source", "obligation_refs"},
			"Condition":          {"value", "status", "source", "reason"},
			"AcceptanceContract": {"negative_cases", "boundary_cases", "recovery_cases"},
			"FactChange":         {"from", "to", "source", "replacement_coverage"},
		} {
			fields := defs[typ].(map[string]any)["properties"].(map[string]any)
			for _, key := range keys {
				if fields[key] == nil {
					t.Error("missing nested contract", typ, key)
				}
			}
		}
		artifact := defs["ArtifactContract"].(map[string]any)["properties"].(map[string]any)
		placeholder := artifact["not_applicable"].(map[string]any)
		if placeholder["type"] != "object" || placeholder["additionalProperties"] != false || placeholder["maxProperties"] != float64(0) {
			t.Error("artifact applicability placeholder must not allow substantive declarations")
		}
		if !strings.Contains(request.Messages[0].Content, "do not copy it into artifact") {
			t.Error("acceptance and artifact applicability guidance is ambiguous")
		}
		for _, instruction := range []string{"compact JSON", "open_questions, never questions", "Preserve ALL distinct requirements", "members are generated later", "read ALL frozen user messages", "never ask the user to reconfirm", "planning completeness defect", "Unknown has_interfaces/has_persistence belongs in conditions", "document-only delivery"} {
			if !strings.Contains(request.Messages[0].Content, instruction) {
				t.Error("missing output guidance", instruction)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llmfixture.Response(`<escape>{"facts":[{"id":"REQ-1"}],"open_questions":[]}`))
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "schema-test", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	if _, err := provider.PlanDocumentSet(context.Background(), capture, profile, previous); err != nil {
		t.Fatal(err)
	}
}
