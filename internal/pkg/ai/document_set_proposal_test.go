package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentprofile"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/testutil/llmfixture"
)

func TestPlannerExtractsAssistantProposalWithoutTreatingArchiveAsApproval(t *testing.T) {
	for _, status := range []string{"proposed", "confirmed"} {
		t.Run(status, func(t *testing.T) {
			capture := ds.CaptureSnapshot{Messages: []ds.Message{{Role: "user", Text: "Build a profile-driven agent."}, {Role: "ai", Text: "Persist requirement, task and execution state with recovery checkpoints."}, {Role: "user", Text: "Archive the design above."}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Input []struct{ Content string }
					Text  json.RawMessage
				}
				if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
					t.Error(e)
					return
				}
				if !strings.Contains(request.Input[0].Content, "including substantive designs already written") || !strings.Contains(request.Input[0].Content, "Do not ask users what files this profile generates") {
					t.Error("extraction still delegates archive mechanics to the user")
				}
				var input map[string]json.RawMessage
				json.Unmarshal([]byte(request.Input[1].Content), &input)
				var source struct {
					Users     []int `json:"user_message_indices"`
					Proposals []int `json:"proposal_message_indices"`
				}
				json.Unmarshal(input["source_contract"], &source)
				if len(source.Users) != 2 || len(source.Proposals) != 1 || source.Proposals[0] != 1 || !strings.Contains(string(input["archive_contract"]), "Markdown") {
					t.Error("incomplete source/archive contract")
				}
				result := ds.SharedDesign{Facts: []ds.Fact{{ID: "MOD-STATE", Kind: "module", Name: "State recovery", Content: capture.Messages[1].Text, Status: status, Source: ds.Source{Message: 1, Quote: capture.Messages[1].Text}, Owner: "module:MOD-STATE", Provenance: []ds.Provenance{{ID: "P-STATE", Kind: "model_proposal", PublicRef: "capture/message/1"}}}}}
				raw, _ := json.Marshal(result)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(llmfixture.Response(string(raw)))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "proposal-test", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			out, e := p.PlanDocumentSet(WithRequiredJSON(context.Background()), capture, ds.Builtins()[2], nil)
			if status == "confirmed" {
				if e == nil {
					t.Fatal("assistant proposal became user-approved")
				}
				return
			}
			if e != nil || out.Facts[0].Source.Message != 1 || out.Facts[0].Status != "proposed" {
				t.Fatal("proposal was discarded or relabeled", out, e)
			}
		})
	}
}

func TestUnapprovedDesignGenerationStillRequiresNativeJSON(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "native-json", true: "unsupported"}[unsupported], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request struct {
					Input []struct{ Content string }
					Text  json.RawMessage
				}
				json.NewDecoder(r.Body).Decode(&request)
				if string(request.Text) != `{"format":{"type":"json_object"}}` {
					t.Error("draft generation inherited text fallback")
				}
				if !strings.Contains(request.Input[0].Content, "private, unapproved design archive") || !strings.Contains(request.Input[0].Content, "ALL supplied user and assistant") {
					t.Error("missing draft extraction policy")
				}
				w.Header().Set("Content-Type", "application/json")
				if unsupported {
					w.WriteHeader(400)
					w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format","message":"unsupported"}}`))
					return
				}
				json.NewEncoder(w).Encode(llmfixture.Response(`{"title":"Design draft","sections":{"scope":{"content":"Retain existing proposed modules and user goals."},"behavior":{"content":"Decompose requirements and preserve execution state."},"design":{"content":"Record the extracted interface and checkpoint design as proposed."},"failures":{"content":"Recovery policy remains unconfirmed."},"verification":{"content":"Verification is proposed, not executed."}},"references":[],"assumptions":[]}`))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "draft-policy", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			profile, e := documentprofile.Parse([]byte(ds.Builtins()[2].Roles[0].ProfileSnapshot), documentprofile.Limits{})
			if e != nil {
				t.Fatal(e)
			}
			_, e = p.GenerateDocument(WithRequiredJSON(context.Background()), DocumentGenerationRequest{UnapprovedDraft: true, Purpose: ds.EngineeringPurpose, Profile: profile, Context: DocumentSourceContext{Body: "Frozen user and assistant proposals"}})
			if (e != nil) != unsupported || calls != 1 {
				t.Fatal("native JSON policy or replay changed", calls, e)
			}
		})
	}
}
