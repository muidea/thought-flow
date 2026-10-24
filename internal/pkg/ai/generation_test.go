package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentstore"
)

func TestGenerationUnknownOutcomeNeverReplays(t *testing.T) {
	for _, formatted := range []bool{false, true} {
		for _, status := range []int{400, 401, 403, 429, 502, 503, -1} {
			t.Run(fmtTestName(formatted, status), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if status == -1 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Should-Retry", "true")
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"code":"upstream","message":"HTTP 403 (upstream)","retryable":true}}`))
				}))
				defer server.Close()
				p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
				var err error
				if formatted {
					_, _, err = p.chatCompletionJSON(context.Background(), "system", "user", 0.2)
				} else {
					_, err = p.chatCompletion(context.Background(), "system", "user", 0.2)
				}
				if err == nil || calls.Load() != 1 || CanAutomaticallyRetry(err) {
					t.Fatalf("calls=%d err=%v", calls.Load(), err)
				}
			})
		}
	}
}

func fmtTestName(formatted bool, status int) string {
	return fmt.Sprintf("formatted=%v/status=%d", formatted, status)
}

func TestFallbackFailureNeverReplaysAndPreservesDiagnostics(t *testing.T) {
	for _, status := range []int{429, 502, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var formatted, plain atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&request)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "gateway-attempt-123")
				if _, ok := request["response_format"]; ok {
					formatted.Add(1)
					w.WriteHeader(400)
					_, _ = w.Write([]byte(formatUnsupportedResponse))
					return
				}
				plain.Add(1)
				w.Header().Set("X-Should-Retry", "true")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"upstream_unavailable","failure_class":"upstream_access_denied","retryable":false,"message":"bootstrap: HTTP 403 (upstream)"}}`))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			_, err := p.BuildCaptureContext(context.Background(), CaptureContextRequest{SessionID: "session-test", Content: "ISA-95"})
			var failure ProviderError
			if !errors.As(err, &failure) || failure.StatusCode != status || failure.GatewayCode != "upstream_unavailable" || failure.FailureClass != "upstream_access_denied" || failure.RequestID != "gateway-attempt-123" || failure.GatewayRetryable == nil || *failure.GatewayRetryable || failure.Retryable || failure.AutoRetry {
				t.Fatalf("failure=%#v err=%v", failure, err)
			}
			if formatted.Load() != 1 || plain.Load() != 1 {
				t.Fatalf("formatted=%d plain=%d", formatted.Load(), plain.Load())
			}
		})
	}
}

func TestGenerationSharesNegotiationDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			time.Sleep(45 * time.Millisecond)
			w.WriteHeader(400)
			_, _ = w.Write([]byte(formatUnsupportedResponse))
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()
	p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: 100 * time.Millisecond}, appconfig.EmbeddingConfig{})
	start := time.Now()
	_, _, err := p.chatCompletionJSON(context.Background(), "system", "user", 0.2)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 180*time.Millisecond || calls.Load() != 2 {
		t.Fatalf("elapsed=%v calls=%d err=%v", time.Since(start), calls.Load(), err)
	}
}

func TestGenerationRejectsTruncatedAndInvalidCompletions(t *testing.T) {
	for _, body := range []string{`{"choices":`, `{"choices":"bad"}`, `{"choices":[]}`, `{"choices":[{"finish_reason":"length","message":{"content":"{\"type\":\"text\",\"confidence\":1}"}}]}`} {
		t.Run(body, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
			result, err := p.Classify(context.Background(), ClassifyRequest{User: "input"})
			if err == nil || result.Raw != "" || calls.Load() != 1 || CanAutomaticallyRetry(err) {
				t.Fatalf("result=%#v calls=%d err=%v", result, calls.Load(), err)
			}
		})
	}
}

func TestGenerationLogsGatewayCorrelationWithoutRequestSecrets(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") == "" {
			t.Error("missing client correlation")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "gateway-success-123")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"assistant_reply\":\"ok\"}"}}]}`))
	}))
	defer server.Close()
	p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, APIKey: "SECRET_API_KEY", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	_, err := p.BuildCaptureContext(context.Background(), CaptureContextRequest{SessionID: "session-safe", Content: "PRIVATE_PROMPT_CONTENT"})
	if err != nil {
		t.Fatal(err)
	}
	log := logs.String()
	for _, want := range []string{"gateway-success-123", "session-safe"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing correlation: %s", log)
		}
	}
	if strings.Contains(log, "SECRET_API_KEY") || strings.Contains(log, "PRIVATE_PROMPT_CONTENT") {
		t.Fatal("request secrets logged")
	}
}

func TestReviewReusesCaptureFormatDecision(t *testing.T) {
	var formatted, plain atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		if _, ok := request["response_format"]; ok {
			formatted.Add(1)
			w.WriteHeader(400)
			_, _ = w.Write([]byte(formatUnsupportedResponse))
			return
		}
		plain.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"assistant_reply\":\"ok\",\"issues\":[]}"}}]}`))
	}))
	defer server.Close()
	config := appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}
	p := NewOpenAICompatibleProvider(config, appconfig.EmbeddingConfig{})
	if _, err := p.BuildCaptureContext(context.Background(), CaptureContextRequest{Content: "source"}); err != nil {
		t.Fatal(err)
	}
	reviewer := NewOpenAICompatibleProvider(config, appconfig.EmbeddingConfig{})
	if _, _, err := reviewer.ReviewDocument(context.Background(), documentstore.RevisionData{}); err != nil {
		t.Fatal(err)
	}
	if formatted.Load() != 1 || plain.Load() != 2 {
		t.Fatalf("format negotiation repeated: %d/%d", formatted.Load(), plain.Load())
	}
}

func TestGenerationKeepsHTTPStatusForMalformedGatewayErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Request-ID", "broken-gateway-1")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>Bad gateway</html>"))
	}))
	defer server.Close()
	p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
	_, err := p.chatCompletion(context.Background(), "system", "user", 0.2)
	var failure ProviderError
	if !errors.As(err, &failure) || failure.StatusCode != 502 || failure.Code != "thoughtflow.ai.transient_status" || failure.RequestID != "broken-gateway-1" || calls.Load() != 1 || CanAutomaticallyRetry(err) {
		t.Fatalf("failure=%#v err=%v calls=%d", failure, err, calls.Load())
	}
}
