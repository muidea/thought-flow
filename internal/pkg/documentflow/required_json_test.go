package documentflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
)

func TestDocumentReviewAndRepairRequireJSONWithoutFallback(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&request)
		if string(request["text"]) != `{"format":{"type":"json_object"}}` {
			t.Error("document review/repair omitted json_object")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "document-required-json", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	_, s, thought, _ := setup(t, "design")
	snapshot, report := check(t, s, thought.ID)
	s.Options.Repairer = provider
	_, err := s.Repair(context.Background(), thought.ID, snapshot.Head.WorkingRevisionID, report.ReportID, snapshot.Head.Generation, []string{report.Issues[0].ID}, []string{"proposal"})
	var failure ai.ProviderError
	if !errors.As(err, &failure) || failure.Code != "thoughtflow.ai.conversion_unsupported" || calls.Load() != 1 {
		t.Fatal("repair downgraded or retried", calls.Load(), err)
	}
	s.Options.Reviewer = provider
	r, err := s.Validate(context.Background(), thought.ID, snapshot.Head.WorkingRevisionID, snapshot.Head.Generation)
	if err != nil || r.Status != "error" || calls.Load() != 2 {
		t.Fatal("unsupported reviewer passed, downgraded or retried", calls.Load(), r.Status, err)
	}
	after, err := s.Get(thought.ID)
	if err != nil || after.Head.WorkingRevisionID != snapshot.Head.WorkingRevisionID || after.Head.FinalizedRevisionID != "" {
		t.Fatal("failed review/repair changed published content", err)
	}
}
