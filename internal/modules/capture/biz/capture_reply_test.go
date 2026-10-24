package biz

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/scratchpad"
)

// Exercise the real provider JSON parser, async context enrichment and disk
// persistence. Model content is scripted; this is not a live-model quality eval.
func TestCaptureResearchAnswersSurviveFollowupAndReload(t *testing.T) {
	const first = "先看核心结论：ISA-95 用于企业业务与制造运营的集成。Level 4 关注业务计划，Level 3 关注制造运营；例如 ERP 下达生产订单，MES 反馈实际产量和物料消耗。"
	const second = "再补充一个具体区别：生产能力描述资源能够完成什么，生产计划描述何时做什么。共享设备和物料标识可减少 ERP 与 MES 之间的映射歧义。具体标准条款尚需核对原文。"
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		mu.Lock()
		requests = append(requests, string(raw))
		turn := len(requests)
		mu.Unlock()
		reply := first
		body := "## 集成边界\n\nLevel 4 的业务计划与 Level 3 的制造运营交换订单和执行结果。"
		if turn > 1 {
			reply = second
			body += "\n\n## 能力与计划\n\n能力说明可用资源，计划安排执行时间；统一资源标识减少映射歧义。"
		}
		result := map[string]any{"assistant_reply": reply, "topic": "ISA95 模型调研", "candidate_title": "ISA95 模型", "candidate_summary": "ISA95 的集成边界、资源能力和生产计划。", "candidate_body": body, "archive_intent": "none", "archive_strategy": "new", "missing_profile_inputs": []string{"可选：行业背景"}, "source_links": []string{}}
		payload, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(payload)}}}})
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	root := t.TempDir()
	store := scratchpad.New(root)
	svc := NewScratchpadService(store, WithCaptureContextProvider(provider), WithEventHub(newRecordingEventHub(true)))
	if _, err := svc.AppendMessage("research", "user", "为我调研isa95模型"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		sp, err := store.Get("research")
		return err == nil && len(sp.Messages) == 2
	})
	one, err := store.Get("research")
	if err != nil {
		t.Fatal(err)
	}
	if one.Messages[1].Text != first || one.SessionContext.AssistantReply != first {
		t.Fatal("chat answer was replaced by archive metadata", one)
	}
	if _, err := svc.AppendMessage("research", "user", "还有什么内容吗"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		sp, err := store.Get("research")
		return err == nil && len(sp.Messages) == 4
	})
	two, err := store.Get("research")
	if err != nil {
		t.Fatal(err)
	}
	if two.Messages[1].Text != first || two.Messages[3].Text != second {
		t.Fatal("followup lost its own answer", two.Messages)
	}
	if two.SessionContext.CandidateSummary == second || !strings.Contains(two.SessionContext.CandidateBody, "集成边界") || !strings.Contains(two.SessionContext.CandidateBody, "能力与计划") {
		t.Fatal("archive content and reply are not separate", two.SessionContext)
	}
	if strings.Contains(archiveBody(two), "再补充一个具体区别") {
		t.Fatal("chat narration leaked into archive body")
	}
	reloaded, err := scratchpad.New(root).Get("research")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SessionContext.AssistantReply != second || len(reloaded.Messages) != 4 || reloaded.Messages[3].Text != second {
		t.Fatal("reload lost the answer", reloaded)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || !strings.Contains(requests[1], first) || !strings.Contains(requests[1], "还有什么内容吗") {
		t.Fatal("followup lacked conversation context", requests)
	}
}

func TestCaptureReplyDoesNotReplayPreviousTurnOrArchiveBody(t *testing.T) {
	current := scratchpad.Scratchpad{SessionContext: scratchpad.SessionContext{AssistantReply: "old answer", CandidateSummary: "archive summary", CandidateBody: "cumulative material"}}
	next := captureContextToScratchpad(ai.CaptureContextResult{AssistantReply: "new answer"}, current)
	if reply := sessionContextReplyText(next); reply != "new answer" {
		t.Fatal(reply)
	}
	missing := captureContextToScratchpad(ai.CaptureContextResult{CandidateSummary: "updated summary"}, current)
	if missing.AssistantReply != "" {
		t.Fatal("reused previous turn's answer", missing)
	}
}

func TestCaptureReportsUnsupportedCapabilitiesWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": "conversion_unsupported", "message": "conversion unavailable", "feature": "response_format", "unsupported_features": []string{"response_format", "tools"},
		}})
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	store := scratchpad.New(t.TempDir())
	svc := NewScratchpadService(store, WithCaptureContextProvider(provider), WithEventHub(newRecordingEventHub(true)), WithCaptureContextRetryDelays(0, 0))
	if _, err := svc.AppendMessage("unsupported", "user", "解释 ISA-95"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		sp, err := store.Get("unsupported")
		return err == nil && len(sp.Messages) == 2
	})
	sp, err := store.Get("unsupported")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"response_format", "tools", "JSON", "Provider", "不会自动重试"} {
		if !strings.Contains(sp.Messages[1].Text, fragment) {
			t.Fatalf("missing %q in user-facing diagnosis: %q", fragment, sp.Messages[1].Text)
		}
	}
	if attempts.Load() != 1 {
		t.Fatalf("non-retryable error caused %d requests", attempts.Load())
	}
}

func TestCaptureInvalidJSONFallbackDoesNotRepeatOrUpdateArchiveContext(t *testing.T) {
	for _, content := range []string{"不是 JSON", `{"candidate_summary":"无有效回答的归档摘要"}`} {
		t.Run(content, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if attempts.Add(1) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
			}))
			defer server.Close()
			provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second, AllowJSONFallback: true}, appconfig.EmbeddingConfig{})
			store := scratchpad.New(t.TempDir())
			svc := NewScratchpadService(store, WithCaptureContextProvider(provider), WithEventHub(newRecordingEventHub(true)), WithCaptureContextRetryDelays(0, 0))
			if _, err := svc.AppendMessage("fallback", "user", "解释 ISA-95"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				sp, err := store.Get("fallback")
				return err == nil && len(sp.Messages) == 2
			})
			sp, err := store.Get("fallback")
			if err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 2 || sp.SessionContext.AssistantReply != "" || sp.SessionContext.CandidateSummary != "" || !strings.Contains(sp.Messages[1].Text, "未能获得通过校验的 JSON 结果") {
				t.Fatalf("invalid fallback leaked or retried: attempts=%d scratchpad=%#v", attempts.Load(), sp)
			}
		})
	}
}
