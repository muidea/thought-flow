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

	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/models"
)

func TestJSONResponseDiagnosticShapes(t *testing.T) {
	for _, tc := range []struct{ content, shape string }{
		{" \ufeff ", "empty"},
		{"<!DOCTYPE html><html>secret</html>", "html_document"},
		{"<body>secret</body>", "html_document"},
		{"<think>secret", "unclosed_reasoning_wrapper"},
		{"<analysis>secret</analysis>", "reasoning_only"},
		{"<think>secret</think>{}", "reasoning_wrapper"},
		{"```json\nsecret", "markdown_fence"},
		{"<secret-tag>body</secret-tag>", "unrecognized_markup"},
		{`{"facts":[]}`, "json_object"},
		{`{"facts":`, "incomplete_json_object"},
		{`{"facts":invalid}`, "invalid_json_object"},
		{`<escape>{"facts":[]}`, "escape_prefix"},
		{`<escape>{"facts":[]}</escape>`, "escape_prefix"},
		{`<escape>{"facts":[{"acceptance":{"recovery_cases":["case"}}]}`, "invalid_json_object"},
		{`<escape>{"facts":[</escape>`, "incomplete_json_object"},
		{`<escape>{"facts":[{"content":"中文"}],"`, "incomplete_json_object"},
		{"[]", "non_object_json"},
		{"null", "non_object_json"},
		{"not JSON", "non_json_text"},
	} {
		if got := jsonResponseShape(tc.content); got != tc.shape {
			t.Errorf("shape=%q, want %q", got, tc.shape)
		}
	}
}

func TestJSONFailureDiagnosticPreservesRawResponsePrivately(t *testing.T) {
	const content = "<html><body>PRIVATE-JSON-RESPONSE-CANARY 中文</body></html>"
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prior)
	for _, fallback := range []bool{false, true} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			var req map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "effective-completion-id")
			if _, ok := req["text"]; ok && fallback {
				w.Header().Set("X-Request-ID", "format-probe-id")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(formatUnsupportedResponse))
				return
			}
			_ = json.NewEncoder(w).Encode(llmfixture.Response(content))
		}))
		provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "diagnostic-test", Timeout: time.Second}, appconfig.EmbeddingConfig{})
		_, planErr := provider.PlanDocumentSet(context.Background(), ds.CaptureSnapshot{}, ds.Profile{}, nil)
		_, _, reviewErr := provider.ReviewDocumentSet(context.Background(), nil, nil, ds.SharedDesign{}, ds.CaptureSnapshot{})
		server.Close()
		for _, err := range []error{planErr, reviewErr} {
			var failure ProviderError
			if !errors.As(err, &failure) || failure.RequestID != "effective-completion-id" || failure.StatusCode != 200 || failure.ResponseShape != "html_document" || failure.ResponseBytes != len(content) || failure.ResponseHash != models.ContentHash(content) || failure.JSONOffset != 1 || CanAutomaticallyRetry(err) {
				t.Fatalf("missing effective response diagnostics: %#v %v", failure, err)
			}
			diagnostic, ok := JSONResponseErrorDiagnostic(err)
			if !ok || diagnostic.Version != 1 || diagnostic.Content != content || diagnostic.ResponseHash != models.ContentHash(content) || diagnostic.RequestID != failure.RequestID {
				t.Fatal("raw failure evidence missing or transformed", diagnostic)
			}
			encoded, marshalErr := json.Marshal(err)
			if marshalErr != nil || strings.Contains(string(encoded)+err.Error()+logs.String(), "PRIVATE-JSON-RESPONSE-CANARY") {
				t.Fatal("raw completion leaked through error serialization or ordinary logs", marshalErr)
			}
		}
		want := 2
		if fallback {
			want++
		}
		if int(calls.Load()) != want {
			t.Fatal("diagnostics replayed generation", calls.Load())
		}
	}
}

func TestConcurrentJSONDiagnosticsStayWithTheirRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var input struct{ Capture ds.CaptureSnapshot }
		_ = json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &input)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", input.Capture.SessionID)
		_ = json.NewEncoder(w).Encode(llmfixture.Response("<unrecognized>" + input.Capture.SessionID + "</unrecognized>"))
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "concurrent-diagnostics", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	var wg sync.WaitGroup
	for _, id := range []string{"request-A", "request-B", "request-C", "request-D"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := provider.PlanDocumentSet(context.Background(), ds.CaptureSnapshot{SessionID: id}, ds.Profile{}, nil)
			d, ok := JSONResponseErrorDiagnostic(err)
			if !ok || d.RequestID != id || d.Content != "<unrecognized>"+id+"</unrecognized>" {
				t.Errorf("request diagnostics crossed calls: %s %+v", id, d)
			}
		}(id)
	}
	wg.Wait()
}

func TestIncompleteJSONWithCompletedResponseRemainsTerminalAndPreservesEvidence(t *testing.T) {
	// Synthetic reproduction of the observed shape. Never store real user
	// completions in a test fixture or ordinary log.
	const content = `<escape>{"facts":[{"id":"REQ-1","content":"PRIVATE-INCOMPLETE 中文"}],"`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llmfixture.Response(content, map[string]any{"usage": map[string]int{"input_tokens": 1350, "output_tokens": 4096, "total_tokens": 5446}}))
	}))
	defer server.Close()
	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "incomplete-test", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	_, err := provider.PlanDocumentSet(context.Background(), ds.CaptureSnapshot{}, ds.Profile{}, nil)
	var failure ProviderError
	if !errors.As(err, &failure) || failure.Reason != "incomplete_json" || failure.ResponseShape != "incomplete_json_object" || failure.JSONOffset != int64(len(content)+1) || CanAutomaticallyRetry(err) || failure.Retryable {
		t.Fatalf("incomplete completion incorrectly accepted or classified: %v", err)
	}
	diagnostic, ok := JSONResponseErrorDiagnostic(err)
	if !ok || diagnostic.Content != content || diagnostic.ResponseHash != models.ContentHash(content) || diagnostic.ResponseStatus != "completed" || diagnostic.FinishReason != "" || diagnostic.CompletionTokens != 4096 || diagnostic.PromptTokens != 1350 || calls.Load() != 1 {
		t.Fatal("raw evidence/metadata lost or request replayed", calls.Load())
	}
	if strings.Contains(err.Error(), "PRIVATE-INCOMPLETE") {
		t.Fatal("private content leaked in error")
	}
}

func TestUnknownPlannerFieldCannotExposeSourceTextInErrors(t *testing.T) {
	const content = `<escape>{"facts":[{"id":"REQ-1"}],"PRIVATE-FIELD-NAME-CANARY":[]}`
	_, trace := withJSONResponseTrace(context.Background())
	trace.capture("schema-error", 200, content)
	var out ds.SharedDesign
	parseErr := ds.StrictJSON([]byte(unwrapJSONResponse(content)), &out)
	if parseErr == nil {
		t.Fatal("schema drift was accepted")
	}
	failure := trace.validationError(parseErr, true)
	var providerErr ProviderError
	if !errors.As(failure, &providerErr) || providerErr.Reason != "unknown_field" || strings.Contains(failure.Error(), "PRIVATE-FIELD-NAME-CANARY") {
		t.Fatal("model-controlled field name escaped the private diagnostic", failure)
	}
	diagnostic, ok := JSONResponseErrorDiagnostic(failure)
	if !ok || diagnostic.Content != content {
		t.Fatal("schema mismatch lost raw evidence")
	}
}

func TestMalformedWrappedJSONReportsOriginalSyntaxPositionWithoutRepair(t *testing.T) {
	// Synthetic missing array terminator plus an unfinished tail. A later EOF
	// must not hide the earlier syntax failure, and neither is repaired.
	const malformed = `{"facts":[{"acceptance":{"recovery_cases":["PRIVATE-SYNTAX 中文"}},{"id":"next"}],"open_questions":["`
	for _, content := range []string{
		malformed,
		"<escape>" + malformed,
		"<escape>\n" + malformed + "\n</escape>",
		"<analysis>Discard this identical example: " + malformed + "</analysis>\n<escape>" + malformed,
	} {
		if unwrapJSONResponse(content) != content {
			t.Fatal("invalid source was repaired or extracted as a successful response")
		}
		_, trace := withJSONResponseTrace(context.Background())
		trace.capture("syntax-position", 200, content)
		var out ds.SharedDesign
		parseErr := ds.StrictJSON([]byte(unwrapJSONResponse(content)), &out)
		if parseErr == nil {
			t.Fatal("invalid source accepted")
		}
		failure := trace.validationError(parseErr, true)
		var providerErr ProviderError
		wantOffset := int64(strings.LastIndex(content, malformed) + strings.Index(malformed, "}") + 1)
		if !errors.As(failure, &providerErr) || providerErr.JSONOffset != wantOffset || providerErr.Reason != "invalid_json" || providerErr.ResponseShape != "invalid_json_object" || CanAutomaticallyRetry(failure) {
			t.Fatalf("wrong syntax location or policy: %v, expected byte %d", failure, wantOffset)
		}
		if strings.Contains(failure.Error(), "PRIVATE-SYNTAX") || strings.Contains(failure.Error(), "invalid character '<'") {
			t.Fatal("private source leaked or wrapper concealed actual syntax error")
		}
		diagnostic, ok := JSONResponseErrorDiagnostic(failure)
		if !ok || diagnostic.Content != content || diagnostic.ResponseHash != models.ContentHash(content) {
			t.Fatal("diagnostics changed the original response")
		}
	}
}
