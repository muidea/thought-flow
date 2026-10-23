package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
)

func TestRefinementMetadataConstraintsAndCompleteSource(t *testing.T) {
	valid := refineJSON{Title: "DuckDB 索引", Summary: "计划采用 DuckDB，尚未验证并发写入；排除远程同步。", Tags: []string{"DuckDB", "索引"}}
	cases := []struct {
		name      string
		edit      func(*refineJSON)
		wantError bool
	}{
		{"valid", func(*refineJSON) {}, false},
		{"missing summary", func(r *refineJSON) { r.Summary = " " }, true},
		{"oversized summary", func(r *refineJSON) { r.Summary = strings.Repeat("中", models.MetadataSummaryMaxRunes+1) }, true},
		{"oversized title", func(r *refineJSON) { r.Title = strings.Repeat("中", models.MetadataTitleMaxRunes+1) }, true},
		{"missing title", func(r *refineJSON) { r.Title = "" }, true},
		{"oversized tag", func(r *refineJSON) { r.Tags = []string{strings.Repeat("中", models.MetadataTagMaxRunes+1)} }, true},
		{"too many tags", func(r *refineJSON) { r.Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} }, true},
		{"url tag", func(r *refineJSON) { r.Tags = []string{"https://example.test"} }, true},
		{"no supported tags", func(r *refineJSON) { r.Tags = nil }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output := valid
			tc.edit(&output)
			source := "SOURCE_START " + strings.Repeat("长笔记正文", 5000) + " SOURCE_END"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if len(body.Messages) != 2 || !strings.Contains(body.Messages[1].Content, source) {
					t.Error("refinement did not receive complete source")
				}
				if len(body.Messages) > 0 && (!strings.Contains(body.Messages[0].Content, "600") || !strings.Contains(body.Messages[0].Content, "Preserve negation")) {
					t.Error("missing metadata constraints")
				}
				data, _ := json.Marshal(output)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(data)}}}})
			}))
			defer server.Close()
			provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, APIKey: "test", ChatModel: "test"}, appconfig.EmbeddingConfig{})
			got, err := provider.Refine(context.Background(), RefineRequest{Thought: models.Thought{ID: "note"}, Content: models.ThoughtContent{AINotes: source}})
			if tc.wantError {
				var failure ProviderError
				if !errors.As(err, &failure) || failure.Code != "thoughtflow.ai.invalid_refinement_metadata" || !failure.Retryable {
					t.Fatalf("expected retryable metadata error, got %v", err)
				}
			} else if err != nil || got.Summary != output.Summary {
				t.Fatalf("summary altered or refinement failed: %+v %v", got, err)
			}
		})
	}
}

func TestLocalMetadataStaysValidAndAvoidsSubstringTags(t *testing.T) {
	got, err := NewLocalRefineProvider().Refine(context.Background(), RefineRequest{Content: models.ThoughtContent{AINotes: strings.Repeat("中文内容", 400)}})
	if err != nil || !utf8.ValidString(got.Summary) || ValidateRefinementMetadata(got) != nil {
		t.Fatalf("invalid local metadata: %+v %v", got, err)
	}
	if got := inferTags("ongoing plain retail"); len(got) != 0 {
		t.Fatalf("unrelated substring tags: %v", got)
	}
}
