package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/testutil/llmfixture"
)

func TestResponsesWireContractAndOrderedAssistantText(t *testing.T) {
	for _, mode := range []string{"plain", "required-json", "classifier-cap"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/responses" {
					t.Error("generation used a different endpoint", r.Method, r.URL.Path)
				}
				var req map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				for _, key := range []string{"messages", "response_format", "max_tokens", "previous_response_id", "background"} {
					if _, exists := req[key]; exists {
						t.Error("unexpected request field", key)
					}
				}
				if string(req["stream"]) != "false" || r.Header.Get("Accept") != "application/json" {
					t.Error("generation must explicitly request nonstream JSON")
				}
				if string(req["store"]) != "false" || string(req["model"]) != `"wire-model"` || string(req["temperature"]) != "0.2" {
					t.Error("Responses request changed model, temperature or retention", string(req["store"]), string(req["model"]), string(req["temperature"]))
				}
				var input []struct{ Role, Content string }
				if err := json.Unmarshal(req["input"], &input); err != nil || len(input) != 2 || input[0].Role != "system" || input[0].Content != "SYSTEM 中文" || input[1].Role != "user" || input[1].Content != "USER material" {
					t.Error("input roles or source bytes changed", input, err)
				}
				if mode == "required-json" {
					if string(req["text"]) != `{"format":{"type":"json_object"}}` {
						t.Error("missing Responses JSON format", string(req["text"]))
					}
				} else if _, exists := req["text"]; exists {
					t.Error("plain request unexpectedly requested JSON formatting")
				}
				if mode == "classifier-cap" {
					if string(req["max_output_tokens"]) != "96" {
						t.Error("classification output cap not mapped")
					}
				} else if _, exists := req["max_output_tokens"]; exists {
					t.Error("unrequested output cap added")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "responses-wire-id")
				_, _ = w.Write([]byte(`{"object":"response","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"PRIVATE REASONING"}]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"answer\":"},{"type":"output_text","text":"\"kept\"}"}]}],"usage":{"input_tokens":13,"output_tokens":8,"total_tokens":21}}`))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "wire-model", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			ctx, trace := withJSONResponseTrace(context.Background())
			var text string
			var err error
			switch mode {
			case "plain":
				text, err = p.responseCompletion(ctx, "SYSTEM 中文", "USER material", 0.2)
			case "required-json":
				text, _, err = p.responseCompletionJSON(WithRequiredJSON(ctx), "SYSTEM 中文", "USER material", 0.2)
			case "classifier-cap":
				var result ClassifyResult
				result, err = p.Classify(ctx, ClassifyRequest{System: "SYSTEM 中文", User: "USER material", Temperature: 0.2, MaxTokens: 96})
				text = result.Raw
			}
			if err != nil || text != `{"answer":"kept"}` || calls.Load() != 1 {
				t.Fatal("Responses text was lost, exposed reasoning or replayed", text, calls.Load(), err)
			}
			if trace.diagnostic.RequestID != "responses-wire-id" || trace.diagnostic.ResponseStatus != "completed" || trace.diagnostic.PromptTokens != 13 || trace.diagnostic.CompletionTokens != 8 || trace.diagnostic.FinishReason != "" {
				t.Fatal("Responses completion metadata not preserved", trace.diagnostic)
			}
		})
	}
}

func TestResponsesRejectUnfinishedRefusedAndUnexpectedOutputWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
		code string
	}{
		{"truncated", llmfixture.Response("partial", map[string]any{"status": "incomplete", "incomplete_details": map[string]string{"reason": "max_output_tokens"}}), "output_truncated"},
		{"filtered", llmfixture.Response("partial", map[string]any{"status": "incomplete", "incomplete_details": map[string]string{"reason": "content_filter"}}), "output_incomplete"},
		{"failed", llmfixture.Response("partial", map[string]any{"status": "failed", "error": map[string]string{"code": "server_error", "message": "PRIVATE failure"}}), "response_failed"},
		{"completed-with-error", llmfixture.Response("partial", map[string]any{"error": map[string]string{"code": "server_error", "message": "PRIVATE failure"}}), "response_failed"},
		{"missing-status", llmfixture.Response("partial", map[string]any{"status": ""}), "output_incomplete"},
		{"queued", llmfixture.Response("partial", map[string]any{"status": "queued"}), "output_incomplete"},
		{"in-progress", llmfixture.Response("partial", map[string]any{"status": "in_progress"}), "output_incomplete"},
		{"cancelled", llmfixture.Response("partial", map[string]any{"status": "cancelled"}), "output_incomplete"},
		{"empty", llmfixture.Response("  "), "empty_completion"},
		{"wrong-protocol", map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "legacy"}}}}, "invalid_response"},
		{"output-text-only", map[string]any{"object": "response", "status": "completed", "output_text": "shortcut"}, "empty_completion"},
		{"tool-call", llmfixture.Response("", map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "unrequested"}}}), "invalid_response"},
		{"incomplete-message", llmfixture.Response("", map[string]any{"output": []any{map[string]any{"type": "message", "role": "assistant", "status": "in_progress", "content": []any{map[string]string{"type": "output_text", "text": "partial"}}}}}), "invalid_response"},
		{"refusal-after-text", llmfixture.Response("", map[string]any{"output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "partial"}, map[string]string{"type": "refusal", "refusal": "PRIVATE refusal"}}}}}), "refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Should-Retry", "true")
				w.Header().Set("X-Request-ID", "responses-terminal-id")
				_ = json.NewEncoder(w).Encode(tc.body)
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			text, fallback, err := p.responseCompletionJSON(WithRequiredJSON(context.Background()), "JSON only", "request", 0)
			var failure ProviderError
			if text != "" || fallback || !errors.As(err, &failure) || failure.Code != "thoughtflow.ai."+tc.code || failure.StatusCode != 200 || failure.RequestID != "responses-terminal-id" || CanAutomaticallyRetry(err) || failure.Retryable {
				t.Fatal("unsafe Responses output accepted or made retryable", text, fallback, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(paths, []string{"/v1/responses"}) {
				t.Fatal("generation replayed or switched protocols", paths)
			}
		})
	}
}
