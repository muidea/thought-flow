package biz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

func TestCaptureParameterTypeFailurePreservesContextAndPrivateEvidence(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "object rejected", true: "string accepted"}[valid], func(t *testing.T) {
			const requestID = "capture-parameter-request"
			content := `<escape>{"assistant_reply":"New answer","document_parameters":{"purpose":{"value":"PRIVATE-PARAMETER-CANARY","confirmed":true}}}</escape>`
			if valid {
				content = `<escape>{"assistant_reply":"New answer","document_parameters":{"purpose":"model suggestion"}}</escape>`
			}
			var formatted, plain atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&request)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", requestID)
				if _, ok := request["text"]; ok {
					formatted.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
					return
				}
				plain.Add(1)
				_ = json.NewEncoder(w).Encode(llmfixture.Response(content))
			}))
			defer server.Close()
			root := t.TempDir()
			store := scratchpad.New(filepath.Join(root, "sessions"))
			before, err := store.Save(scratchpad.Scratchpad{
				SessionID: "s", InputGeneration: 14, Content: "Original confirmed material",
				Messages:       []scratchpad.Message{{Role: "user", Text: "Original confirmed material"}},
				SessionContext: scratchpad.SessionContext{CandidateProfileID: models.DocumentProfileBuiltinNote, CandidateProfileVersion: 1, CandidateDocumentFamily: models.DocumentFamilyNote, AssistantReply: "Previous answer", DocumentParameters: map[string]string{"purpose": "user confirmed"}, ParameterValues: map[string]scratchpad.ParameterValue{"purpose": {Value: "user confirmed", Confirmed: true, Source: "user"}}},
				Operation:      &scratchpad.ArchiveOperation{ID: "old-operation", State: "failed", InputGeneration: 12, Error: "OLD_PLANNER_ERROR"},
			})
			if err != nil {
				t.Fatal(err)
			}
			provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "parameter-types", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			svc := NewScratchpadService(store, WithCaptureContextProvider(provider), WithCaptureJSONDiagnostics(root), WithCaptureContextRetryDelays(0, 0))
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previousLogger)
			svc.handleCaptureContextEnrichRequest(captureContextEnrichRequest{SessionID: "s", ContentAtSchedule: before.Content, InputGeneration: before.InputGeneration, ExistingContext: before.SessionContext})
			after, err := store.Get("s")
			if err != nil {
				t.Fatal(err)
			}
			if after.Content != before.Content || after.SessionContext.DocumentParameters["purpose"] != "user confirmed" || !after.SessionContext.ParameterValues["purpose"].Confirmed {
				t.Fatal("user input or confirmed parameter changed")
			}
			paths, err := filepath.Glob(filepath.Join(root, ".content-registry/private/capture/*/diagnostics/*.json"))
			if err != nil {
				t.Fatal(err)
			}
			if valid {
				if len(paths) != 0 || after.SessionContext.AssistantReply != "New answer" {
					t.Fatal("successful response retained private completion or lost answer")
				}
			} else {
				if !reflect.DeepEqual(after.SessionContext, before.SessionContext) || len(paths) != 1 {
					t.Fatal("invalid response updated context or lost evidence")
				}
				message := after.Messages[len(after.Messages)-1].Text
				if !strings.Contains(message, "document_parameters") || !strings.Contains(message, requestID) || strings.Contains(message, "PRIVATE-PARAMETER-CANARY") {
					t.Fatal("field/request evidence missing from safe chat error")
				}
				data, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				var record struct {
					InputGeneration uint64                    `json:"input_generation"`
					Stage           string                    `json:"stage"`
					Response        ai.JSONResponseDiagnostic `json:"response"`
				}
				if json.Unmarshal(data, &record) != nil || record.InputGeneration != before.InputGeneration || record.Stage != "capture_context" || record.Response.Content != content || record.Response.RequestID != requestID || record.Response.ResponseHash != models.ContentHash(content) {
					t.Fatal("private response evidence was changed")
				}
				info, err := os.Stat(paths[0])
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("private evidence permissions incorrect")
				}
			}
			stored, err := os.ReadFile(filepath.Join(root, "sessions/s.json"))
			if err != nil || strings.Contains(string(stored)+logs.String(), "PRIVATE-PARAMETER-CANARY") {
				t.Fatal("model output leaked to session or ordinary logs")
			}
			if formatted.Load() != 1 || plain.Load() != 1 {
				t.Fatal("parameter validation retried the model", formatted.Load(), plain.Load())
			}
		})
	}
}

func TestCaptureDiagnosticSymlinkCannotExposeFailedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "capture-private-failure")
		_ = json.NewEncoder(w).Encode(llmfixture.Response(`{"assistant_reply":"answer","document_parameters":{"purpose":{"value":"PRIVATE-CAPTURE-CANARY"}}}}`))
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
	_, failure := provider.BuildCaptureContext(context.Background(), ai.CaptureContextRequest{Content: "original material"})
	if failure == nil {
		t.Fatal("parameter object accepted")
	}
	root, outside := t.TempDir(), t.TempDir()
	private := filepath.Join(root, ".content-registry/private")
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(private, "capture")); err != nil {
		t.Fatal(err)
	}
	svc := NewScratchpadService(nil, WithCaptureJSONDiagnostics(root))
	failure = svc.persistCaptureJSONFailure("../../outside", 4, failure)
	var providerError ai.ProviderError
	if !errors.As(failure, &providerError) || providerError.Field != "document_parameters" || providerError.RequestID != "capture-private-failure" || !strings.Contains(failure.Error(), "persist private capture JSON response diagnostic") || strings.Contains(failure.Error(), "PRIVATE-CAPTURE-CANARY") {
		t.Fatal("diagnostic persistence failure lost the safe provider error", failure)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("private capture output escaped workspace", err)
	}
}
