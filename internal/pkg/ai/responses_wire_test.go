package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/testutil/llmfixture"
)

func wireEvent(kind string, fields map[string]any) string {
	fields["type"] = kind
	raw, _ := json.Marshal(fields)
	return "event: " + kind + "\ndata: " + string(raw) + "\n\n"
}

func TestResponsesAcceptActualFramingWithoutReplaying(t *testing.T) {
	const answer = `{"assistant_reply":"当前回答","candidate_summary":"完整保留"}`
	const prefix = "{\"assistant_reply\":"
	jsonBody, _ := json.Marshal(llmfixture.Response(answer))
	completed := llmfixture.SSE(answer, map[string]any{"usage": map[string]int{"input_tokens": 17, "output_tokens": 23}})
	created := wireEvent("response.created", map[string]any{"response": map[string]string{"id": "resp-fixture", "object": "response", "status": "in_progress"}})
	delta := func(value string) string {
		return wireEvent("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "delta": value})
	}
	textDone := wireEvent("response.output_text.done", map[string]any{"output_index": 0, "content_index": 0, "text": answer})
	for _, tc := range []struct{ name, contentType, body string }{
		{"json-with-sse-header", "text/event-stream; charset=utf-8", string(jsonBody)},
		{"json-with-no-header", "", string(jsonBody)},
		{"complete-sse", "text/event-stream", completed},
		{"sse-with-json-header", "application/json", completed},
		{"full-sse", "text/event-stream", ": keepalive\n\n" + created + delta(prefix) + delta(strings.TrimPrefix(answer, prefix)) + textDone + completed + "data: [DONE]\n\n"},
		{"crlf-sse", "text/event-stream", strings.ReplaceAll(completed, "\n", "\r\n")},
		{"bom-sse", "text/event-stream", "\xef\xbb\xbf" + completed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/responses" || r.Header.Get("Accept") != "application/json" {
					t.Error("wrong request contract")
				}
				var req map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&req)
				if string(req["stream"]) != "false" || string(req["text"]) != `{"format":{"type":"json_object"}}` {
					t.Error("request lost explicit nonstream or JSON format")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Request-ID", "wire-request-id")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			ctx, trace := withJSONResponseTrace(context.Background())
			content, fallback, err := p.responseCompletionJSON(WithRequiredJSON(ctx), "Return JSON", "新输入", 0)
			if err != nil || fallback {
				t.Fatal("unexpected generation failure/fallback", err, fallback)
			}
			out, err := parseCaptureContextResponse(content)
			if err != nil || out.AssistantReply != "当前回答" || out.CandidateSummary != "完整保留" || calls.Load() != 1 || trace.diagnostic.RequestID != "wire-request-id" {
				t.Fatal("lost validated output/identity or replayed", out, err, calls.Load(), trace.diagnostic)
			}
			if strings.Contains(tc.name, "sse") && tc.name != "json-with-sse-header" && (trace.diagnostic.PromptTokens != 17 || trace.diagnostic.CompletionTokens != 23) {
				t.Fatal("lost SSE usage", trace.diagnostic)
			}
		})
	}
}

func TestResponsesRejectInvalidSSEWithoutReplayOrRawContentLeak(t *testing.T) {
	const text = `{"assistant_reply":"PRIVATE ANSWER"}`
	completed := llmfixture.SSE(text)
	delta := wireEvent("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "delta": text})
	for _, tc := range []struct{ name, body string }{
		{"done-only", "data: [DONE]\n\n"},
		{"missing-completed", delta},
		{"truncated-final", strings.TrimSuffix(completed, "\n")},
		{"malformed-final", "event: response.completed\ndata: {PRIVATE\n\n"},
		{"mismatch-event-type", strings.Replace(completed, "event: response.completed", "event: response.created", 1)},
		{"duplicate-completed", completed + completed},
		{"error-after-completed", completed + wireEvent("error", map[string]any{"message": "PRIVATE ERROR"})},
		{"failed", wireEvent("response.failed", map[string]any{"response": llmfixture.Response(text)})},
		{"incomplete", wireEvent("response.incomplete", map[string]any{"response": llmfixture.Response(text)})},
		{"wrong-response-id", wireEvent("response.created", map[string]any{"response": map[string]string{"id": "another", "object": "response"}}) + completed},
		{"wrong-final-text", wireEvent("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "delta": "DIFFERENT"}) + completed},
		{"wrong-done-text", delta + wireEvent("response.output_text.done", map[string]any{"output_index": 0, "content_index": 0, "text": "DIFFERENT"}) + completed},
		{"wrong-item-id", wireEvent("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "item_id": "wrong", "delta": text}) + completed},
		{"out-of-range-part", wireEvent("response.output_text.delta", map[string]any{"output_index": 4, "content_index": 0, "delta": text}) + completed},
		{"delta-after-done", delta + wireEvent("response.output_text.done", map[string]any{"output_index": 0, "content_index": 0, "text": text}) + delta + completed},
		{"event-after-done", completed + "data: [DONE]\n\n" + delta},
		{"incomplete-snapshot", llmfixture.SSE(text, map[string]any{"status": "incomplete"})},
		{"refusal", llmfixture.SSE("", map[string]any{"output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "refusal", "refusal": "PRIVATE REFUSAL"}}}}})},
		{"html", "<html>PRIVATE ERROR</html>"},
		{"oversize-after-completed", completed + ":" + strings.Repeat("x", maxResponsesBytes)},
		{"completed-but-invalid-business-json", llmfixture.SSE(`{"assistant_reply":[]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-ID", "wire-rejected-id")
				w.Header().Set("X-Should-Retry", "true")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			out, err := p.BuildCaptureContext(WithRequiredJSON(context.Background()), CaptureContextRequest{Content: "KEEP INPUT"})
			var failure ProviderError
			if !errors.As(err, &failure) || calls.Load() != 1 || CanAutomaticallyRetry(err) || out.AssistantReply != "" || failure.RequestID != "wire-rejected-id" || failure.StatusCode != 200 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("unsafe result, missing diagnostics or replay", out, calls.Load(), err)
			}
		})
	}
}

type trackedResponseBody struct {
	io.Reader
	closed bool
}

func (b *trackedResponseBody) Close() error { b.closed = true; return nil }

func TestResponsesWireClosesBodyAndEnforcesBudgetWithoutTransport(t *testing.T) {
	for _, body := range []string{llmfixture.SSE("answer"), "<html>error</html>", strings.Repeat(" ", maxResponsesBytes+1)} {
		tracked := &trackedResponseBody{Reader: strings.NewReader(body)}
		_, _, err := readResponsesHTTP(context.Background(), &http.Response{Body: tracked})
		if !tracked.closed || (len(body) > maxResponsesBytes && err == nil) {
			t.Fatal("body not closed or unbounded", err)
		}
	}
	for _, size := range []int{16, 17} {
		tracked := &trackedResponseBody{Reader: strings.NewReader(strings.Repeat("x", size))}
		bounded := &boundedResponseBody{ReadCloser: tracked, remaining: 16}
		_, err := io.ReadAll(bounded)
		_ = bounded.Close()
		if !tracked.closed || (size == 16 && err != nil) || (size > 16 && err == nil) {
			t.Fatal("budget failed at boundary", size, err)
		}
	}
}

func TestResponsesSSECancellationPreservesContextAndNeverReplays(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[timeout], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": waiting\n\n")
				w.(http.Flusher).Flush()
				if !timeout {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			budget := time.Second
			if timeout {
				budget = 50 * time.Millisecond
			}
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: budget}, appconfig.EmbeddingConfig{})
			out, err := p.responseCompletion(ctx, "system", "user", 0)
			want := context.Canceled
			if timeout {
				want = context.DeadlineExceeded
			}
			if out != "" || !errors.Is(err, want) || calls.Load() != 1 || CanAutomaticallyRetry(err) {
				t.Fatal("cancellation lost or generation replayed", out, calls.Load(), err)
			}
		})
	}
}
