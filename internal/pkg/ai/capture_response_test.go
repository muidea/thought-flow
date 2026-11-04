package ai

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

	"thoughtflow/internal/pkg/appconfig"
)

func TestCaptureResponseSelectsValidatedObjectWithoutMerging(t *testing.T) {
	for _, tc := range []struct {
		name, content, reply, summary string
	}{
		{"normal", `{"assistant_reply":"answer","candidate_summary":"summary"}`, "answer", "summary"},
		{"auxiliary first", `{"diagnostic":{"phase":"complete"}}` + "\n" + `{"assistant_reply":"answer","candidate_summary":"summary"}`, "answer", "summary"},
		{"summary first", `{"candidate_summary":"discard me","candidate_body":"discard body"}` + "\n" + `{"assistant_reply":"answer"}`, "answer", ""},
		{"wrong type first", `{"candidate_summary":"discard me","assistant_reply":[]}` + "\n" + `{"assistant_reply":"answer"}`, "answer", ""},
		{"malformed first", `{"topic":"broken"x}` + "\n" + `{"assistant_reply":"answer"}`, "answer", ""},
		{"braces in answer", "```json\n" + `{"assistant_reply":"an example: {x}","candidate_summary":"summary"}` + "\n```", "an example: {x}", "summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseCaptureContextResponse(tc.content)
			if err != nil || parsed.AssistantReply != tc.reply || parsed.CandidateSummary != tc.summary || parsed.CandidateBody != "" {
				t.Fatalf("wrong candidate or merged fields: parsed=%+v err=%v", parsed, err)
			}
		})
	}
}

func TestCaptureResponseRejectsMissingOrNestedAnswer(t *testing.T) {
	for _, content := range []string{
		`{"candidate_summary":"archive summary","candidate_body":"full document"}`,
		`{"assistant_reply":" \n "}`,
		`{"assistant_reply":null}`,
		`{"AssistantReply":"wrong wire name"}`,
		`{"metadata":{"assistant_reply":"nested example"}}`,
		`{"candidate_body":"a quoted example: {\"assistant_reply\":\"not this turn\"}"}`,
		`null`,
	} {
		_, err := parseCaptureContextResponse(content)
		var missing requiredJSONFieldError
		if !errors.As(err, &missing) || missing.field != "assistant_reply" {
			t.Fatalf("accepted missing/nested answer or lost diagnosis: %s: %v", content, err)
		}
	}
	_, err := parseCaptureContextResponse(`{"assistant_reply":["wrong type"]}`)
	var fieldType *json.UnmarshalTypeError
	if !errors.As(err, &fieldType) || fieldType.Field != "assistant_reply" {
		t.Fatalf("lost type validation: %v", err)
	}
}

func TestCaptureWireContractSurvivesCustomPromptAndJSONFallback(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "default"
		if custom {
			name = "custom"
		}
		t.Run(name, func(t *testing.T) {
			var formatted, plain atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages       []struct{ Role, Content string } `json:"input"`
					ResponseFormat json.RawMessage                  `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				if len(request.Messages) != 2 {
					t.Errorf("unexpected message count: %d", len(request.Messages))
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				system, user := request.Messages[0].Content, request.Messages[1].Content
				if !strings.HasSuffix(system, captureResponseContract) || !strings.HasSuffix(user, "candidate_summary and candidate_body are separate archive fields.") {
					t.Error("missing required response contract")
				}
				_, schemaJSON, found := strings.Cut(system, "Capture response_schema JSON (wire types, not source content):\n")
				schemaJSON, _, _ = strings.Cut(schemaJSON, "\n\n"+captureResponseContract)
				var schema struct {
					Defs map[string]struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"$defs"`
				}
				if !found || json.Unmarshal([]byte(schemaJSON), &schema) != nil {
					t.Error("missing exact capture wire schema")
				}
				properties := schema.Defs["captureContextJSON"].Properties
				var parameters struct {
					Type       string
					Additional map[string]string `json:"additionalProperties"`
				}
				if json.Unmarshal(properties["document_parameters"], &parameters) != nil || parameters.Type != "object" || parameters.Additional["type"] != "string" {
					t.Error("parameter map must require string values")
				}
				if !strings.Contains(system, "confirmation metadata is owned by the backend") || !strings.Contains(system, "Never put objects, arrays, numbers, booleans or null inside this map") {
					t.Error("custom prompts must not erase parameter type/authority constraints")
				}
				_, history, ok := strings.Cut(user, "Existing context JSON (historical state, not the response schema):\n")
				if !ok {
					t.Error("historical state must be labeled as input")
				}
				history, _, _ = strings.Cut(history, "\n\nExisting thought profile JSON:")
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(history), &fields); err != nil {
					t.Error(err)
				}
				for _, key := range []string{"assistant_reply", "candidate_body", "candidate_summary", "document_parameters", "archive_intent"} {
					if _, ok := fields[key]; !ok {
						t.Errorf("historical context does not use response key %s", key)
					}
				}
				if _, wrong := fields["AssistantReply"]; wrong {
					t.Error("Go field names leaked into wire schema")
				}
				if len(request.ResponseFormat) != 0 {
					formatted.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(formatUnsupportedResponse))
					return
				}
				plain.Add(1)
				_ = json.NewEncoder(w).Encode(llmfixture.Response(`{"metadata":{"completed":true}}` + "\n" + `{"assistant_reply":"本轮仅包含预订和订单，不包含收付款。","candidate_summary":"限定范围"}`))
			}))
			defer server.Close()
			cfg := appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}
			if custom {
				path := filepath.Join(t.TempDir(), "capture.md")
				if err := os.WriteFile(path, []byte("Use the configured business tone. Maintain booking and order requirements."), 0600); err != nil {
					t.Fatal(err)
				}
				cfg.Prompts.CaptureContextSystemPath = path
			}
			for i := 0; i < 2; i++ {
				provider := NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})
				out, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{
					Content: "不用处理收付款", Existing: CaptureContextResult{AssistantReply: "previous answer", CandidateSummary: "old summary"},
				})
				if err != nil || out.AssistantReply != "本轮仅包含预订和订单，不包含收付款。" || out.CandidateSummary != "限定范围" {
					t.Fatalf("valid response lost: %+v %v", out, err)
				}
			}
			if formatted.Load() != 1 || plain.Load() != 2 {
				t.Fatalf("extra generation/repeated negotiation: formatted=%d plain=%d", formatted.Load(), plain.Load())
			}
		})
	}
}
