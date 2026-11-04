package biz

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

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

func TestPrepareArchiveRequiresJSONWithoutRetryOrLocalFallback(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			var request map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&request)
			if string(request["text"]) != `{"format":{"type":"json_object"}}` {
				t.Error("archive generator omitted json_object")
			}
			w.Header().Set("Content-Type", "application/json")
			if unsupported {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"sections\":42}"}]}]}`))
		}))
		provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "archive-required-json", Timeout: time.Second}, appconfig.EmbeddingConfig{})
		registry, err := documentprofile.NewRegistry("", models.DocumentProfileBuiltinNote, documentprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		svc := NewScratchpadService(newMemoryScratchpad(), WithDocumentProfiles(registry, provider, 10, 2))
		sp := scratchpad.Scratchpad{SessionID: "strict-archive", ArchiveStrategy: scratchpad.ArchiveStrategyNew, SessionContext: scratchpad.SessionContext{
			CandidateTitle: "Design", CandidateBody: strings.Repeat("Preserve source material. ", 80),
			CandidateProfileID: models.DocumentProfileBuiltinDesignDoc, CandidateProfileVersion: 1,
			DocumentParameters: map[string]string{"problem": "duplicate requests", "goals": "prevent duplicate writes", "non_goals": "do not change contracts"},
		}}
		preview, err := svc.PrepareArchive(context.Background(), sp, nil)
		server.Close()
		want := "thoughtflow.ai.invalid_json_response"
		if unsupported {
			want = "thoughtflow.ai.conversion_unsupported"
		}
		var failure ai.ProviderError
		if !errors.As(err, &failure) || failure.Code != want || preview.Body != "" || calls.Load() != 1 {
			t.Fatal("archive accepted, downgraded or retried provider failure", calls.Load(), preview.Validation, err)
		}
	}
}
