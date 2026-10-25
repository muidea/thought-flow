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

func TestCaptureCompatibleResponseAfterAuxiliaryJSONPersistsCurrentAnswer(t *testing.T) {
	var formatted, plain atomic.Int32
	const reply = "本轮范围限定为多门店预订和订单，不包含收付款。"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, ok := request["response_format"]; ok {
			formatted.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
			return
		}
		answer := "范围包含多门店预订和订单。"
		if plain.Add(1) > 1 {
			answer = reply
		}
		content := `{"candidate_summary":"discard this fragment","archive_intent":"llm"}` + "\n" +
			`{"assistant_reply":"` + answer + `","candidate_summary":"范围已收敛","candidate_body":"多门店平台只处理预订和订单，不处理收付款。","archive_intent":"none"}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}, appconfig.EmbeddingConfig{})
	root := t.TempDir()
	store := scratchpad.New(root)
	svc := NewScratchpadService(store, WithCaptureContextProvider(provider), WithEventHub(newRecordingEventHub(true)))
	for i, input := range []string{"面向多家门店，只提供预订和订单", "不用处理收付款"} {
		if _, err := svc.AppendMessage("booking", "user", input); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			sp, err := store.Get("booking")
			return err == nil && len(sp.Messages) == (i+1)*2
		})
	}
	sp, err := scratchpad.New(root).Get("booking")
	if err != nil {
		t.Fatal(err)
	}
	if sp.Messages[2].Text != "不用处理收付款" || sp.Messages[3].Text != reply || sp.SessionContext.AssistantReply != reply {
		t.Fatalf("current input/answer did not survive reload: %+v", sp.Messages)
	}
	if sp.ArchiveIntent != scratchpad.ArchiveIntentNone || sp.SessionContext.CandidateSummary != "范围已收敛" || sp.Operation != nil {
		t.Fatalf("discarded JSON fragment changed context or requested archive: %+v", sp)
	}
	if formatted.Load() != 1 || plain.Load() != 2 {
		t.Fatalf("repeated negotiation or generation: formatted=%d plain=%d", formatted.Load(), plain.Load())
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

func TestCaptureFallbackFailuresKeepDiagnosticsAndBoundRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		status        int
		emptyChoices  bool
		want          string
	}{
		{name: "malformed", content: "不是 JSON", status: 200, want: "无法解析为 JSON"},
		{name: "missing-answer", content: `{"candidate_summary":"无有效回答的归档摘要"}`, status: 200, want: "缺少必填回答字段 assistant_reply"},
		{name: "wrong-type", content: `{"assistant_reply":["invalid"]}`, status: 200, want: "JSON 字段类型不符合"},
		{name: "unavailable", status: 503, want: "模型服务暂时不可用（HTTP 503）"},
		{name: "gateway-upstream-forbidden", status: 502, want: "模型服务暂时不可用（HTTP 502）"},
		{name: "limited", status: 429, want: "模型服务限流（HTTP 429）"},
		{name: "empty", status: 200, emptyChoices: true, want: "模型服务未返回候选回答"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After-Ms", "1")
				if attempts.Add(1) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
					return
				}
				w.WriteHeader(tc.status)
				if tc.status != 200 {
					if tc.status == 502 {
						_, _ = w.Write([]byte(`{"error":{"code":"upstream","message":"chatgpt upstream: code:6, message:bootstrap: HTTP 403 (upstream)"}}`))
						return
					}
					_, _ = w.Write([]byte(`{"error":{"message":"UPSTREAM_PRIVATE_DETAIL"}}`))
					return
				}
				if tc.emptyChoices {
					_, _ = w.Write([]byte(`{"choices":[]}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.content}}}})
			}))
			defer server.Close()
			provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, ChatModel: "scripted", Timeout: time.Second}, appconfig.EmbeddingConfig{})
			root := t.TempDir()
			store := scratchpad.New(root)
			svc := NewScratchpadService(store, WithCaptureContextProvider(provider), WithEventHub(newRecordingEventHub(true)), WithCaptureContextRetryDelays(0, 0))
			if _, err := svc.AppendMessage("fallback", "user", "解释 ISA-95"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { sp, err := store.Get("fallback"); return err == nil && len(sp.Messages) == 2 })
			sp, err := scratchpad.New(root).Get("fallback")
			if err != nil {
				t.Fatal(err)
			}
			wantAttempts := int32(2)
			if attempts.Load() != wantAttempts || sp.SessionContext.AssistantReply != "" || sp.SessionContext.CandidateSummary != "" {
				t.Fatalf("invalid result leaked or retried: attempts=%d scratchpad=%#v", attempts.Load(), sp)
			}
			if sp.Messages[0].Text != "解释 ISA-95" || !strings.Contains(sp.Messages[1].Text, tc.want) || strings.Contains(sp.Messages[1].Text, "UPSTREAM_PRIVATE_DETAIL") {
				t.Fatalf("bad persisted diagnosis: %#v", sp.Messages)
			}
			if tc.status == 200 && !tc.emptyChoices && !strings.Contains(sp.Messages[1].Text, "不会自动重试") {
				t.Fatal("invalid content must remain terminal")
			}
			if tc.status >= 400 && strings.Contains(sp.Messages[1].Text, "JSON 输出能力") {
				t.Fatal("transport failure blamed on JSON capability")
			}
		})
	}
}

func TestCaptureCachedFallbackRequiresExplicitRetryAfterGateway(t *testing.T) {
	var formatted, plain atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After-Ms", "1")
		if _, found := request["response_format"]; found {
			formatted.Add(1)
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"conversion_unsupported","feature":"response_format"}}`))
			return
		}
		turn := plain.Add(1)
		if turn == 2 {
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"error":{"code":"upstream","message":"bootstrap: HTTP 403 (upstream)"}}`))
			return
		}
		reply := "ISA-95 定义企业与制造运营边界。"
		if turn > 2 {
			reply = "ERP、MES/MOM 和控制系统承担不同职责。"
		}
		content, _ := json.Marshal(map[string]string{"assistant_reply": reply, "candidate_summary": reply})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer server.Close()
	provider := ai.NewOpenAICompatibleProvider(appconfig.LLMConfig{BaseURL: server.URL, Timeout: time.Second}, appconfig.EmbeddingConfig{})
	root := t.TempDir()
	store := scratchpad.New(root)
	svc := NewScratchpadService(store, WithCaptureContextProvider(provider), WithEventHub(newRecordingEventHub(true)), WithCaptureContextRetryDelays(0, 0))
	if _, err := svc.AppendMessage("s", "user", "调研 ISA-95"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { sp, _ := store.Get("s"); return len(sp.Messages) == 2 })
	if _, err := svc.AppendMessage("s", "user", "涉及哪些信息系统"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { sp, _ := store.Get("s"); return len(sp.Messages) == 4 })
	sp, err := scratchpad.New(root).Get("s")
	if err != nil {
		t.Fatal(err)
	}
	if formatted.Load() != 1 || plain.Load() != 2 || sp.Messages[1].Text != "ISA-95 定义企业与制造运营边界。" || !strings.Contains(sp.Messages[3].Text, "HTTP 502") {
		t.Fatalf("unexpected replay or lost input: %+v", sp)
	}
	if _, err := svc.AppendMessage("s", "user", "请重试涉及哪些信息系统"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { sp, _ := store.Get("s"); return len(sp.Messages) == 6 })
	sp, _ = scratchpad.New(root).Get("s")
	if formatted.Load() != 1 || plain.Load() != 3 || sp.Messages[5].Text != "ERP、MES/MOM 和控制系统承担不同职责。" {
		t.Fatalf("explicit retry failed: %+v", sp)
	}

}
