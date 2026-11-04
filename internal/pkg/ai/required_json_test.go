package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"thoughtflow/assets/documentformats"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentprofile"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
)

func TestRequiredJSONBypassesFallbackCacheWithoutChangingConversationPolicy(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	var formatted, plain atomic.Int32
	var supported atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "required-json-test")
		if format, exists := request["text"]; exists {
			formatted.Add(1)
			if string(format) != `{"format":{"type":"json_object"}}` {
				t.Error("incorrect response_format", string(format))
			}
			if !supported.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(formatUnsupportedResponse))
				return
			}
		} else {
			plain.Add(1)
		}
		_ = json.NewEncoder(w).Encode(llmfixture.Response(`{"assistant_reply":"answer"}`))
	}))
	defer server.Close()
	p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "required-json", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	// Ordinary conversation primes the shared unsupported cache.
	if _, err := p.BuildCaptureContext(context.Background(), CaptureContextRequest{SessionID: "capture-session", Content: "context"}); err != nil {
		t.Fatal(err)
	}
	strict := WithRequiredJSON(WithGenerationScope(context.Background(), "archive-operation"))
	for i := 0; i < 2; i++ {
		_, fallback, err := p.responseCompletionJSON(strict, "JSON only", "archive", 0)
		var failure ProviderError
		if fallback || !errors.As(err, &failure) || failure.Code != "thoughtflow.ai.conversion_unsupported" || failure.Feature != "text.format" || failure.RequestID != "required-json-test" || CanAutomaticallyRetry(err) {
			t.Fatal("required JSON silently downgraded or lost diagnostics", fallback, err)
		}
	}
	if formatted.Load() != 3 || plain.Load() != 1 {
		t.Fatal("required requests fell back or automatically retried", formatted.Load(), plain.Load())
	}
	// Even concurrent calls on the same provider must keep their own policy.
	supported.Store(true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		for _, required := range []bool{false, true} {
			wg.Add(1)
			go func(required bool) {
				defer wg.Done()
				ctx := context.Background()
				if required {
					ctx = WithRequiredJSON(ctx)
				}
				_, fallback, err := p.responseCompletionJSON(ctx, "JSON only", "request", 0)
				if err != nil || fallback == required {
					t.Error("request policy crossed contexts", required, fallback, err)
				}
			}(required)
		}
	}
	wg.Wait()
	if formatted.Load() != 11 || plain.Load() != 9 {
		t.Fatal("strict success changed conversation cache", formatted.Load(), plain.Load())
	}
	ctx, cancel := context.WithCancel(strict)
	cancel()
	if _, _, err := p.responseCompletionJSON(ctx, "JSON only", "cancelled", 0); !errors.Is(err, context.Canceled) {
		t.Fatal("required JSON lost cancellation", err)
	}
	if formatted.Load() != 11 || plain.Load() != 9 {
		t.Fatal("cancelled request reached provider")
	}
	var fallbackLogged, requiredFailureLogged bool
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "JSON response format unsupported; remembering automatic fallback" {
			fallbackLogged = true
			if record["scope_id"] != "capture-session" || record["json_required"] != false {
				t.Fatal("fallback log did not identify the ordinary conversation", record)
			}
		}
		if record["msg"] == "LLM generation request failed" && record["scope_id"] == "archive-operation" {
			requiredFailureLogged = true
			if record["json_required"] != true || record["json_format"] != true {
				t.Fatal("archive capability failure lost its required JSON policy", record)
			}
		}
	}
	if !fallbackLogged || !requiredFailureLogged {
		t.Fatal("missing conversation/strict archive policy diagnostics")
	}
}

func TestRequiredJSONArchiveOperationsRetainValidation(t *testing.T) {
	profile, err := documentprofile.Parse([]byte(documentformats.DesignDocV2), documentprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		call func(context.Context, *OpenAICompatibleProvider) error
	}{
		{"plan", func(ctx context.Context, p *OpenAICompatibleProvider) error {
			_, err := p.PlanDocumentSet(ctx, ds.CaptureSnapshot{}, ds.Profile{}, nil)
			return err
		}},
		{"generate", func(ctx context.Context, p *OpenAICompatibleProvider) error {
			_, err := p.GenerateDocument(ctx, DocumentGenerationRequest{Profile: profile})
			return err
		}},
		{"joint-review", func(ctx context.Context, p *OpenAICompatibleProvider) error {
			_, _, err := p.ReviewDocumentSet(ctx, nil, nil, ds.SharedDesign{}, ds.CaptureSnapshot{})
			return err
		}},
		{"review", func(ctx context.Context, p *OpenAICompatibleProvider) error {
			_, _, err := p.ReviewDocument(ctx, documentstore.RevisionData{})
			return err
		}},
		{"repair", func(ctx context.Context, p *OpenAICompatibleProvider) error {
			_, err := p.RepairDocument(ctx, documentstore.RevisionData{Profile: profile.Snapshot}, nil, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, content := range []string{`{"facts":`, `{"facts":true,"sections":42}`} {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var request map[string]json.RawMessage
					_ = json.NewDecoder(r.Body).Decode(&request)
					if string(request["text"]) != `{"format":{"type":"json_object"}}` {
						t.Error("archive request omitted json_object")
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(llmfixture.Response(content))
				}))
				p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "required-json-validation", Timeout: time.Second}, appconfig.EmbeddingConfig{})
				err := tc.call(WithRequiredJSON(context.Background()), p)
				server.Close()
				var failure ProviderError
				if !errors.As(err, &failure) || failure.Code != "thoughtflow.ai.invalid_json_response" || CanAutomaticallyRetry(err) || calls.Load() != 1 {
					t.Fatal("invalid archive output accepted, downgraded or retried", calls.Load(), err)
				}
			}
		})
	}
}
