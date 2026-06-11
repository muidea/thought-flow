package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
)

func TestLocalClassify_IdentifiesURL(t *testing.T) {
	p := NewLocalRefineClassify()
	result, err := p.Classify(context.Background(), ClassifyRequest{User: "https://example.com/article"})
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	parsed := decodeClassify(t, result.Raw)
	if parsed.Type != "url" {
		t.Fatalf("expected type=url, got %q (raw=%s)", parsed.Type, result.Raw)
	}
	if parsed.ExtractedURL != "https://example.com/article" {
		t.Fatalf("expected extracted_url to match input, got %q", parsed.ExtractedURL)
	}
	if parsed.Confidence != 1.0 {
		t.Fatalf("expected confidence=1.0, got %v", parsed.Confidence)
	}
}

func TestLocalClassify_IdentifiesURLWithTrailingPunctuation(t *testing.T) {
	p := NewLocalRefineClassify()
	result, err := p.Classify(context.Background(), ClassifyRequest{User: "Check this: https://example.com/post!"})
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	parsed := decodeClassify(t, result.Raw)
	if parsed.Type != "mixed" {
		t.Fatalf("expected type=mixed for URL inside prose, got %q", parsed.Type)
	}
	if parsed.ExtractedURL != "https://example.com/post" {
		t.Fatalf("trailing punctuation should be stripped, got %q", parsed.ExtractedURL)
	}
}

func TestLocalClassify_IdentifiesText(t *testing.T) {
	p := NewLocalRefineClassify()
	result, err := p.Classify(context.Background(), ClassifyRequest{User: "Just a thought about embeddings"})
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	parsed := decodeClassify(t, result.Raw)
	if parsed.Type != "text" {
		t.Fatalf("expected type=text, got %q", parsed.Type)
	}
}

func TestLocalClassify_EmptyInput(t *testing.T) {
	p := NewLocalRefineClassify()
	result, err := p.Classify(context.Background(), ClassifyRequest{User: "   "})
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	parsed := decodeClassify(t, result.Raw)
	if parsed.Type != "text" {
		t.Fatalf("expected type=text for whitespace input, got %q", parsed.Type)
	}
}

func TestOpenAICompatibleClassify_ParsesJSONResponse(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"gpt-4o-mini"`) {
			t.Errorf("expected model in payload, got %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "x",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "{\"type\":\"url\",\"extracted_url\":\"https://example.test/post\",\"confidence\":0.92}"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer server.Close()

	p := NewOpenAICompatibleProvider(testConfig(server.URL), testEmbeddingConfig())
	result, err := p.Classify(context.Background(), ClassifyRequest{
		System:      DefaultClassifySystem,
		User:        "paste this https://example.test/post",
		Temperature: 0,
	})
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	if result.Model != "gpt-4o-mini" {
		t.Errorf("expected model name to be set, got %q", result.Model)
	}
	parsed := decodeClassify(t, result.Raw)
	if parsed.Type != "url" || parsed.ExtractedURL != "https://example.test/post" {
		t.Errorf("unexpected classification: %+v", parsed)
	}
	if attempts != 1 {
		t.Errorf("expected exactly one HTTP attempt, got %d", attempts)
	}
}

func TestOpenAICompatibleClassify_RetriesTransient(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "{\"type\":\"text\",\"confidence\":0.7}"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer server.Close()

	p := NewOpenAICompatibleProvider(testConfig(server.URL), testEmbeddingConfig())
	result, err := p.Classify(context.Background(), ClassifyRequest{
		System: DefaultClassifySystem,
		User:   "ambiguous input",
	})
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("expected retry on 502, got %d attempts", attempts)
	}
	parsed := decodeClassify(t, result.Raw)
	if parsed.Type != "text" {
		t.Errorf("expected type=text after retry, got %q", parsed.Type)
	}
}

func testConfig(baseURL string) appconfig.LLMConfig {
	return appconfig.LLMConfig{
		BaseURL:   baseURL,
		APIKey:    "test-key",
		ChatModel: "gpt-4o-mini",
		Timeout:   2 * time.Second,
	}
}

func testEmbeddingConfig() appconfig.EmbeddingConfig {
	return appconfig.EmbeddingConfig{
		APIKey: "",
	}
}

type classifyJSON struct {
	Type         string  `json:"type"`
	ExtractedURL string  `json:"extracted_url"`
	Confidence   float64 `json:"confidence"`
}

func decodeClassify(t *testing.T, raw string) classifyJSON {
	t.Helper()
	parsed := classifyJSON{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("raw is not valid classify JSON: %v (raw=%q)", err, raw)
	}
	return parsed
}
