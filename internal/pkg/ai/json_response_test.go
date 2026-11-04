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
	"thoughtflow/internal/testutil/llmfixture"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	ds "thoughtflow/internal/pkg/documentset"
)

func TestJSONResponseWrappers(t *testing.T) {
	const object = "{\"facts\":[{\"id\":\"REQ-1\",\"content\":\"Keep <tag>, ``` and 中文 unchanged\"}]}"
	for _, content := range []string{
		object,
		"<escape>" + object,
		"<escape>\n" + object + "\n</escape>",
		"<analysis>Done</analysis>\n<escape>```json\n" + object + "\n```</escape>",
		"<analysis>Done</analysis>\n<escape>" + object,
		" \ufeff\n" + object,
		"```json\n" + object + "\n```",
		"```\r\n" + object + "\r\n```",
		`<think>{"facts":[{"id":"WRONG"}]} is a discarded example.</think>` + object,
		"<analysis>Check the requested schema.</analysis>\n```JSON\n" + object + "\n```",
	} {
		if got := unwrapJSONResponse(content); got != object {
			t.Fatalf("object bytes changed: got %q, want %q", got, object)
		}
	}
	for _, content := range []string{
		`<html><body>` + object + `</body></html>`,
		`<escape>` + object + `</escape>Explanation`,
		`<escape>` + object + `</escape></escape>`,
		`<escape>{"facts":[</escape>`,
		`<escape>` + object + `{} </escape>`,
		object + `</escape>`,
		`<escape><html>` + object + `</html></escape>`,
		`<escape><escape>` + object,
		`<escape>{"facts":[{"id":"REQ-1"}],"`,
		`<escape>` + object + `{}`,
		`<escape>[]`,
		`<think>` + object,
		`<think>` + object + `</think>`, // No final answer outside reasoning.
		`<think>unfinished <think>nested</think>` + object + `</think>`,
		"```json\n" + object,
		"```html\n" + object + "\n```",
		"```json\n" + object + "\n```\nExplanation",
		"```json\n" + object + "\n```\n```json\n{}\n```",
		"<think>Done</think>\n" + object + "\n{}",
		"<think>Done</think>\n" + `{"facts":`,
		"Here is an example: " + object,
		"<think>Done</think>null",
		"<think>Done</think>[]",
	} {
		if got := unwrapJSONResponse(content); got != content {
			t.Fatalf("unexpectedly extracted an answer from %q: %q", content, got)
		}
	}
}

func TestDocumentSetJSONWrappersPreserveStrictValidationAndFallbackCache(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		for _, tc := range []struct {
			name, plan, review string
			invalid            bool
		}{
			{"wrapped", "<think>{\"facts\":[{\"id\":\"WRONG\"}]}</think>\n```json\n{\"facts\":[{\"id\":\"REQ-1\"}]}\n```", "<analysis>Review complete.</analysis>\n```json\n{\"evidence\":[],\"issues\":[]}\n```", false},
			{"escape", `<escape>{"facts":[{"id":"REQ-1"}]}`, `<escape>{"evidence":[],"issues":[]}`, false},
			{"escape-pair", `<escape>{"facts":[{"id":"REQ-1"}]}</escape>`, `<escape>{"evidence":[],"issues":[]}</escape>`, false},
			{"escape-pair-incomplete", `<escape>{"facts":[{"id":"REQ-1"}],"</escape>`, `<escape>{"evidence":[</escape>`, true},
			{"escape-pair-schema-drift", `<escape>{"facts":[{"id":"REQ-1"}],"questions":[]}</escape>`, `<escape>{"evidence":[],"questions":[]}</escape>`, true},
			{"escape-incomplete", `<escape>{"facts":[{"id":"REQ-1"}],"`, `<escape>{"evidence":[`, true},
			{"escape-schema-drift", `<escape>{"facts":[{"id":"REQ-1"}],"questions":[]}`, `<escape>{"evidence":[],"questions":[]}`, true},
			{"unknown-field", "<think>Done</think>\n```json\n{\"facts\":[{\"id\":\"REQ-1\"}],\"unexpected\":true}\n```", `{"evidence":[],"unexpected":true}`, true},
			{"no-required-content", "<think>Done</think>\n```json\n{\"facts\":[]}\n```", "```json\n{\"issues\":[]}\n```", true},
			{"html", `<html><body>{"facts":[{"id":"REQ-1"}]}</body></html>`, `<html><body>{"evidence":[]}</body></html>`, true},
			{"only-reasoning", `<think>{"facts":[{"id":"REQ-1"}]}</think>`, `<think>{"evidence":[]}</think>`, true},
			{"multiple-objects", "```json\n{\"facts\":[{\"id\":\"REQ-1\"}]}\n{}\n```", `{"evidence":[]}{"evidence":[]}`, true},
		} {
			mode := "formatted/"
			if fallback {
				mode = "fallback/"
			}
			t.Run(mode+tc.name, func(t *testing.T) {
				var formatted, plain atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						ResponseFormat json.RawMessage            `json:"text"`
						Messages       []struct{ Content string } `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if len(req.ResponseFormat) > 0 {
						formatted.Add(1)
						if fallback {
							w.WriteHeader(http.StatusBadRequest)
							_, _ = w.Write([]byte(formatUnsupportedResponse))
							return
						}
					} else {
						plain.Add(1)
					}
					content := tc.plan
					if strings.Contains(req.Messages[0].Content, "Review the final document bytes") {
						content = tc.review
					}
					_ = json.NewEncoder(w).Encode(llmfixture.Response(content))
				}))
				defer server.Close()
				provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "wrapped-json", Timeout: time.Second}, appconfig.EmbeddingConfig{})
				for i := 0; i < 2; i++ {
					plan, err := provider.PlanDocumentSet(context.Background(), ds.CaptureSnapshot{}, ds.Profile{}, nil)
					assertSetResponseError(t, err, tc.invalid, fallback, "plan document set")
					if err == nil && (len(plan.Facts) != 1 || plan.Facts[0].ID != "REQ-1") {
						t.Fatal("reasoning object used as plan", plan)
					}
					review, _, err := provider.ReviewDocumentSet(context.Background(), nil, nil, ds.SharedDesign{}, ds.CaptureSnapshot{})
					assertSetResponseError(t, err, tc.invalid, fallback, "review document set")
					if err == nil && review.Evidence == nil {
						t.Fatal("missing review evidence")
					}
				}
				wantFormatted, wantPlain := int32(4), int32(0)
				if fallback {
					wantFormatted, wantPlain = 1, 4
				}
				if formatted.Load() != wantFormatted || plain.Load() != wantPlain {
					t.Fatalf("unexpected replay: formatted=%d plain=%d", formatted.Load(), plain.Load())
				}
			})
		}
	}
}

func assertSetResponseError(t *testing.T, err error, invalid, fallback bool, stage string) {
	t.Helper()
	if !invalid {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	want := "thoughtflow.ai.invalid_json_response"
	if fallback {
		want = "thoughtflow.ai.invalid_json_fallback"
	}
	var failure ProviderError
	if !errors.As(err, &failure) || failure.Code != want || CanAutomaticallyRetry(err) || !strings.Contains(err.Error(), stage) {
		t.Fatalf("invalid output not rejected with stage and terminal provider error: %v", err)
	}
}
