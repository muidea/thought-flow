package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
)

func TestJSONRequestsDoNotDowngradeOrRetryParameterErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   map[string]any
		code   string
	}{
		{"conversion-code", 400, map[string]any{"code": "conversion_unsupported", "feature": "response_format", "unsupported_features": []string{"response_format", "tools"}}, "thoughtflow.ai.conversion_unsupported"},
		{"conversion-type", 400, map[string]any{"type": "conversion_unsupported", "feature": "response_format", "unsupported_features": []string{"response_format", "tools"}}, "thoughtflow.ai.conversion_unsupported"},
		{"misclassified-conversion", 503, map[string]any{"code": "conversion_unsupported", "feature": "response_format", "unsupported_features": []string{"response_format", "tools"}}, "thoughtflow.ai.conversion_unsupported"},
		{"other-parameter-error", 400, map[string]any{"code": "invalid_request_error"}, "thoughtflow.ai.http_status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				var request struct {
					ResponseFormat struct {
						Type string `json:"type"`
					} `json:"response_format"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.ResponseFormat.Type != "json_object" {
					t.Error("JSON request silently lost response_format")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.code == "thoughtflow.ai.conversion_unsupported" {
					// The semantic failure must override even an upstream retry hint.
					w.Header().Set("X-Should-Retry", "true")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": tc.body})
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			_, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{Content: "解释 ISA-95"})
			var failure ProviderError
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Retryable || failure.StatusCode != tc.status {
				t.Fatalf("unexpected failure: %#v, %v", failure, err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("parameter failure caused %d requests", attempts.Load())
			}
			if tc.code == "thoughtflow.ai.conversion_unsupported" &&
				(failure.Feature != "response_format" || len(failure.UnsupportedFeatures) != 2 || !strings.Contains(err.Error(), "unsupported_features=response_format, tools")) {
				t.Fatalf("capability diagnostics were lost: %#v", failure)
			}
		})
	}
}

func TestPlainTextCompletionDoesNotSendResponseFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if _, exists := request["response_format"]; exists {
			t.Error("plain text completion requested a JSON response format")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "plain text"}}}})
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	if reply, err := provider.chatCompletion(context.Background(), "system", "user", 0.2); err != nil || reply != "plain text" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
}

func TestExplicitJSONFallbackRequiresAnUnambiguousCapabilityError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features map[string]any
		fallback bool
	}{
		{"format", map[string]any{"code": "conversion_unsupported", "feature": "response_format"}, true},
		{"format-type", map[string]any{"type": "conversion_unsupported", "unsupported_features": []string{"response_format.json_object"}}, true},
		{"ambiguous", map[string]any{"code": "conversion_unsupported"}, false},
		{"tools", map[string]any{"code": "conversion_unsupported", "feature": "tools"}, false},
		{"mixed", map[string]any{"code": "conversion_unsupported", "feature": "response_format", "unsupported_features": []string{"tools"}}, false},
		{"generic-400", map[string]any{"code": "invalid_request_error", "feature": "response_format"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turn := attempts.Add(1)
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				_, hasFormat := request["response_format"]
				if hasFormat != (turn == 1) {
					t.Errorf("attempt %d response_format present=%v", turn, hasFormat)
				}
				w.Header().Set("Content-Type", "application/json")
				if turn == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": tc.features})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"assistant_reply":"实质回答","candidate_summary":"归档摘要","candidate_body":"工作材料"}`}}}})
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second, AllowJSONFallback: true}, appconfig.EmbeddingConfig{})
			result, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{Content: "解释 ISA-95"})
			if tc.fallback {
				if err != nil || attempts.Load() != 2 || result.AssistantReply != "实质回答" || result.CandidateSummary != "归档摘要" {
					t.Fatalf("fallback failed: attempts=%d result=%#v error=%v", attempts.Load(), result, err)
				}
			} else if err == nil || attempts.Load() != 1 {
				t.Fatalf("unexpected fallback: attempts=%d error=%v", attempts.Load(), err)
			}
		})
	}
}

func TestJSONFallbackRejectsInvalidResultsAndDoesNotRetryTransientFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		status  int
		code    string
	}{
		{"malformed", "这是普通文字，不是 JSON", 200, "thoughtflow.ai.invalid_json_fallback"},
		{"missing-answer", `{"candidate_summary":"只有摘要"}`, 200, "thoughtflow.ai.invalid_json_fallback"},
		{"transient", "", 503, "thoughtflow.ai.json_fallback_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if attempts.Add(1) == 1 {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.content}}}})
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second, AllowJSONFallback: true}, appconfig.EmbeddingConfig{})
			_, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{Content: "解释 ISA-95"})
			var failure ProviderError
			if attempts.Load() != 2 || !errors.As(err, &failure) || failure.Code != tc.code || failure.Retryable {
				t.Fatalf("attempts=%d error=%#v", attempts.Load(), err)
			}
		})
	}
}

func TestJSONFallbackPreservesFurtherUnsupportedFeatureDiagnostics(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feature := "response_format"
		if attempts.Add(1) > 1 {
			feature = "temperature"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "conversion_unsupported", "feature": feature, "unsupported_features": []string{feature}}})
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second, AllowJSONFallback: true}, appconfig.EmbeddingConfig{})
	_, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{Content: "解释 ISA-95"})
	var failure ProviderError
	if attempts.Load() != 2 || !errors.As(err, &failure) || failure.Code != "thoughtflow.ai.conversion_unsupported" || failure.Feature != "temperature" || len(failure.UnsupportedFeatures) != 1 || failure.Retryable {
		t.Fatalf("lost fallback error diagnostics: attempts=%d error=%#v", attempts.Load(), err)
	}
}
