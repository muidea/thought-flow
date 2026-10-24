package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
)

const formatUnsupportedResponse = `{"error":{"code":"conversion_unsupported","feature":"response_format"}}`

func TestJSONFallbackRememberedAcrossProvidersAndStillValidates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		status  int
		code    string
	}{
		{"valid", `{"assistant_reply":"回答","candidate_summary":"摘要"}`, 200, ""},
		{"malformed", "not JSON", 200, "thoughtflow.ai.invalid_json_fallback"},
		{"missing-answer", `{"candidate_summary":"摘要"}`, 200, "thoughtflow.ai.invalid_json_fallback"},
		{"transient", "", 503, "thoughtflow.ai.json_fallback_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var formatted, plain atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After-Ms", "1")
				if _, exists := request["response_format"]; exists {
					formatted.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(formatUnsupportedResponse))
					return
				}
				plain.Add(1)
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.content}}}})
			}))
			defer server.Close()
			cfg := appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}
			for i := 0; i < 2; i++ {
				// Separate feature modules construct separate provider instances.
				provider := NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})
				result, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{Content: "材料"})
				if tc.code == "" {
					if err != nil || result.AssistantReply != "回答" {
						t.Fatalf("call %d result=%#v err=%v", i, result, err)
					}
				} else {
					var failure ProviderError
					if !errors.As(err, &failure) || failure.Code != tc.code || CanAutomaticallyRetry(err) {
						t.Fatalf("call %d failure=%#v err=%v", i, failure, err)
					}
				}
			}
			wantPlain := int32(2)
			if formatted.Load() != 1 || plain.Load() != wantPlain {
				t.Fatalf("formatted=%d plain=%d; want one probe and bounded compatible attempts", formatted.Load(), plain.Load())
			}
		})
	}
}

func TestAutomaticJSONFallbackCapabilityIdentity(t *testing.T) {
	var formatted, plain atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, exists := request["response_format"]; exists {
			formatted.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(formatUnsupportedResponse))
			return
		}
		plain.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()
	for _, tc := range []struct {
		name      string
		base      string
		model     string
		key       string
		formatted int32
		plain     int32
	}{
		{"initial", server.URL, "a", "key-a", 1, 1},
		{"normalized-endpoint", server.URL + "/v1/", "a", " key-a ", 1, 2},
		{"different-model", server.URL, "b", "key-a", 2, 3},
		{"different-credential", server.URL, "a", "key-b", 3, 4},
		{"different-endpoint", server.URL + "/other", "a", "key-a", 4, 5},
		{"original-identity", server.URL, "a", "key-a", 4, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
				BaseURL: tc.base, ChatModel: tc.model, APIKey: tc.key,
				Timeout: time.Second,
			}, appconfig.EmbeddingConfig{})
			_, usedFallback, err := provider.chatCompletionJSON(context.Background(), "system", "user", 0.2)
			if err != nil || !usedFallback {
				t.Fatalf("fallback=%v err=%v", usedFallback, err)
			}
			if formatted.Load() != tc.formatted || plain.Load() != tc.plain {
				t.Fatalf("formatted=%d plain=%d; want %d/%d", formatted.Load(), plain.Load(), tc.formatted, tc.plain)
			}
		})
	}
}

func TestJSONCapabilityDoesNotCacheOtherErrorsOrRemoveSupportedFormat(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"conversion_unsupported"}}`,
		`{"error":{"code":"conversion_unsupported","feature":"tools"}}`,
		`{"error":{"code":"conversion_unsupported","unsupported_features":["response_format","tools"]}}`,
		`{"error":{"code":"invalid_request_error","feature":"response_format"}}`,
		`{"error":{"message":"unauthorized"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if _, exists := request["response_format"]; !exists {
					t.Error("unrelated failure or success removed response_format")
				}
				w.Header().Set("Content-Type", "application/json")
				if attempts.Add(1) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(body))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			for i := 0; i < 3; i++ {
				_, fallback, err := provider.chatCompletionJSON(context.Background(), "system", "user", 0.2)
				if fallback || (i == 0 && err == nil) || (i > 0 && err != nil) {
					t.Fatalf("call %d fallback=%v err=%v", i, fallback, err)
				}
			}
			if attempts.Load() != 3 {
				t.Fatalf("attempts=%d", attempts.Load())
			}
		})
	}
}

func TestJSONFallbackConcurrentProvidersNegotiateOnce(t *testing.T) {
	var formatted, plain atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, exists := request["response_format"]; exists {
			formatted.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(formatUnsupportedResponse))
			return
		}
		plain.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const calls = 16
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < calls; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: 5 * time.Second}, appconfig.EmbeddingConfig{})
			<-start
			_, fallback, err := provider.chatCompletionJSON(ctx, "system", "user", 0.2)
			if err != nil || !fallback {
				t.Errorf("fallback=%v err=%v", fallback, err)
			}
		}()
	}
	close(start)
	workers.Wait()
	if formatted.Load() != 1 || plain.Load() != calls {
		t.Fatalf("formatted=%d plain=%d; want 1/%d", formatted.Load(), plain.Load(), calls)
	}
}

func TestJSONCapabilityCanceledProbeAndWaiterRelease(t *testing.T) {
	capability := &jsonFormatCapability{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, complete, err := capability.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waiterCtx, cancelWaiter := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, _, err := capability.acquire(waiterCtx)
		result <- err
	}()
	cancelWaiter()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter err=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("waiting caller did not cancel")
	}
	complete(context.Canceled)
	// A canceled probe establishes no capability; another caller must probe.
	skip, complete, err := capability.acquire(ctx)
	if err != nil || skip || complete == nil {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
	complete(nil)
	// Known support must not serialize later completions behind a probe gate.
	_, first, err := capability.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := capability.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first(ProviderError{Code: "thoughtflow.ai.conversion_unsupported", Feature: "response_format"})
	second(nil)
	// An in-flight success cannot undo a newer unsupported capability decision.
	skip, _, err = capability.acquire(ctx)
	if err != nil || !skip {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
}

func TestJSONCapabilityCacheEvictsOnlyIdleStates(t *testing.T) {
	cache := newJSONCapabilityCache(2)
	a := jsonFormatCapabilityKey{model: "a"}
	b := jsonFormatCapabilityKey{model: "b"}
	c := jsonFormatCapabilityKey{model: "c"}
	first, releaseA, err := cache.lease(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseB, err := cache.lease(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	releaseB()
	_, releaseC, err := cache.lease(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	again, releaseAgain, err := cache.lease(context.Background(), a)
	if err != nil || again != first {
		t.Fatal("active identity lost canonical state")
	}
	releaseAgain()
	releaseA()
	releaseC()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 2 || cache.entries[b] != nil {
		t.Fatal("idle LRU not evicted")
	}
}

func TestJSONCapabilityCacheCapacityWaitCancelsAndRemainsBounded(t *testing.T) {
	cache := newJSONCapabilityCache(1)
	key := jsonFormatCapabilityKey{model: "active"}
	_, release, err := cache.lease(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err = cache.lease(ctx, jsonFormatCapabilityKey{model: "waiting"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capacity wait err=%v", err)
	}
	release()
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, done, err := cache.lease(ctx, jsonFormatCapabilityKey{model: fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
				return
			}
			cache.mu.Lock()
			size := len(cache.entries)
			cache.mu.Unlock()
			if size > 1 {
				t.Errorf("cache exceeded capacity: %d", size)
			}
			done()
		}(i)
	}
	workers.Wait()
}

func TestJSONCapabilityProbeWaitConsumesGenerationBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	p := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: 30 * time.Millisecond}, appconfig.EmbeddingConfig{})
	state, release, err := jsonFormatCapabilities.lease(context.Background(), p.jsonFormat)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, complete, err := state.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer complete(context.Canceled)
	_, _, err = p.chatCompletionJSON(context.Background(), "system", "user", 0.2)
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatalf("waiting call renewed budget: calls=%d err=%v", calls.Load(), err)
	}
}
