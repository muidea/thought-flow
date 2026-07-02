package biz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mcevent "github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

// memoryScratchpad is the in-memory test double for the scratchpad
// store. It mirrors the production store's contract (Get returns
// zero-value on missing, Delete is idempotent, Save upserts) but
// skips the file system so tests run in microseconds.
type memoryScratchpad struct {
	mu    sync.Mutex
	items map[string]scratchpad.Scratchpad
}

func newMemoryScratchpad() *memoryScratchpad {
	return &memoryScratchpad{items: map[string]scratchpad.Scratchpad{}}
}

func (m *memoryScratchpad) Get(id string) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sp, ok := m.items[id]; ok {
		return sp, nil
	}
	return scratchpad.Scratchpad{SessionID: id}, nil
}

func (m *memoryScratchpad) Save(sp scratchpad.Scratchpad) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[sp.SessionID] = sp
	return sp, nil
}

func (m *memoryScratchpad) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *memoryScratchpad) MarkCommitted(id, thoughtID string) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp, ok := m.items[id]
	if !ok {
		return scratchpad.Scratchpad{}, errors.New("scratchpad not found")
	}
	now := time.Now().UTC()
	sp.CommittedThoughtID = thoughtID
	sp.CommittedAt = &now
	m.items[id] = sp
	return sp, nil
}

func (m *memoryScratchpad) Reset(id string) (scratchpad.Scratchpad, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp, ok := m.items[id]
	if !ok {
		return scratchpad.Scratchpad{}, errors.New("scratchpad not found")
	}
	sp.Content = ""
	sp.Messages = nil
	sp.Draft = scratchpad.Draft{}
	m.items[id] = sp
	return sp, nil
}

func TestScratchpadServiceAppendMessageAccumulatesContent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	if _, err := svc.AppendMessage("s1", "user", "first thought"); err != nil {
		t.Fatalf("first AppendMessage: %v", err)
	}
	if _, err := svc.AppendMessage("s1", "ai", "ok"); err != nil {
		t.Fatalf("second AppendMessage: %v", err)
	}
	if _, err := svc.AppendMessage("s1", "user", "more"); err != nil {
		t.Fatalf("third AppendMessage: %v", err)
	}
	sp, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sp.Content != "first thought\n\nmore" {
		t.Fatalf("content = %q, want %q", sp.Content, "first thought\n\nmore")
	}
	if len(sp.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(sp.Messages))
	}
	if sp.Messages[1].Role != "ai" {
		t.Fatalf("messages[1].Role = %q, want ai", sp.Messages[1].Role)
	}
}

func TestScratchpadServiceAppendMessageClearsSessionContextUntilProviderReturns(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "old title",
			CandidateSummary: "old summary",
			CandidateBody:    "old body",
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	svc := NewScratchpadService(store)

	if _, err := svc.AppendMessage("s1", "user", "我需要整理一个主题方向"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sp.Content != "我需要整理一个主题方向" {
		t.Fatalf("Content = %q", sp.Content)
	}
	if hasSessionContext(sp.SessionContext) {
		t.Fatalf("SessionContext should be empty while waiting for provider, got %+v", sp.SessionContext)
	}
}

func TestScratchpadServiceAppendMessageClearsExistingLLMContext(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "第一轮：我需要整理一个主题方向。",
		SessionContext: scratchpad.SessionContext{
			CandidateBody:    "LLM 第一轮整理：主题方向。",
			CandidateSummary: "LLM 第一轮摘要",
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	svc := NewScratchpadService(store)

	if _, err := svc.AppendMessage("s1", "user", "第二轮：补充背景、目标和预期产出。"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hasSessionContext(sp.SessionContext) {
		t.Fatalf("SessionContext should be cleared while waiting for provider, got %+v", sp.SessionContext)
	}
}

func TestScratchpadServiceAppendMessageEnrichesContextWithProvider(t *testing.T) {
	store := newMemoryScratchpad()
	provider := &stubCaptureContextProvider{
		result: ai.CaptureContextResult{
			Topic:            "LLM topic",
			Goal:             "LLM goal",
			CandidateTitle:   "LLM title",
			CandidateTags:    []string{"llm", "capture"},
			CandidateSummary: "LLM summary",
			CandidateBody:    "LLM body",
			ArchiveIntent:    "none",
			ArchiveStrategy:  "new",
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
	)

	if _, err := svc.AppendMessage("s1", "user", "raw capture message"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	waitFor(t, func() bool { return provider.callCount() == 1 })
	if provider.lastReq.SessionID != "s1" || provider.lastReq.Content != "raw capture message" {
		t.Fatalf("provider request = %+v", provider.lastReq)
	}
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return sp.SessionContext.CandidateSummary == "LLM summary"
	})
	if sp.SessionContext.CandidateTitle != "LLM title" {
		t.Fatalf("CandidateTitle = %q", sp.SessionContext.CandidateTitle)
	}
	if sp.SessionContext.CandidateSummary != "LLM summary" {
		t.Fatalf("CandidateSummary = %q", sp.SessionContext.CandidateSummary)
	}
	if !sameStringSet(sp.SessionContext.CandidateTags, []string{"capture", "llm"}) {
		t.Fatalf("CandidateTags = %+v", sp.SessionContext.CandidateTags)
	}
	if len(sp.Messages) != 2 || sp.Messages[0].Role != "user" || sp.Messages[1].Role != "ai" {
		t.Fatalf("Messages = %+v", sp.Messages)
	}
	if sp.Messages[1].Text != "LLM summary" {
		t.Fatalf("persisted ai reply = %q", sp.Messages[1].Text)
	}
}

func TestScratchpadServiceAppendMessageEnrichesCommittedHistorySession(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID:          "history",
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
		SessionContext: scratchpad.SessionContext{
			CandidateSummary: "archived summary",
			CandidateBody:    "archived body",
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	provider := &stubCaptureContextProvider{
		result: ai.CaptureContextResult{
			CandidateSummary: "continued summary",
			CandidateBody:    "continued body",
			ArchiveIntent:    "none",
			ArchiveStrategy:  "update_thought",
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
	)

	if _, err := svc.AppendMessage("history", "user", "继续补充历史会话"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	waitFor(t, func() bool { return provider.callCount() == 1 })
	if provider.lastReq.Existing.CandidateSummary != "archived summary" {
		t.Fatalf("Existing.CandidateSummary = %q", provider.lastReq.Existing.CandidateSummary)
	}
	sp := waitForScratchpad(t, store, "history", func(sp scratchpad.Scratchpad) bool {
		return strings.Contains(sp.SessionContext.CandidateSummary, "continued summary")
	})
	if sp.CommittedThoughtID != "thought-1" {
		t.Fatalf("CommittedThoughtID = %q, want thought-1", sp.CommittedThoughtID)
	}
	if len(sp.Messages) != 2 || sp.Messages[0].Role != "user" || sp.Messages[1].Role != "ai" {
		t.Fatalf("Messages = %+v", sp.Messages)
	}
}

func TestScratchpadServiceAppendMessageSeedsCommittedHistoryFromArchivePreview(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID:          "history",
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "待用户提供原始内容后确认",
			CandidateSummary: "当前会话上下文为空，需要用户提供原始内容。",
			CandidateBody:    "错误整理：没有上述内容。",
		},
		ArchivePreview: &scratchpad.ArchivePreview{
			Title:    "归档标题",
			Body:     "这是已经归档的完整内容，需要作为继续对话的上述内容。",
			Tags:     []string{"归档", "历史"},
			Strategy: scratchpad.ArchiveStrategyNew,
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	provider := &stubCaptureContextProvider{
		result: ai.CaptureContextResult{
			CandidateSummary: "基于归档内容继续补充",
			CandidateBody:    "补充后的内容",
			ArchiveIntent:    "none",
			ArchiveStrategy:  "new",
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
	)

	if _, err := svc.AppendMessage("history", "user", "基于上述内容继续完善"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	waitFor(t, func() bool { return provider.callCount() == 1 })
	if provider.lastReq.Existing.CandidateTitle != "归档标题" {
		t.Fatalf("Existing.CandidateTitle = %q", provider.lastReq.Existing.CandidateTitle)
	}
	if !strings.Contains(provider.lastReq.Existing.CandidateBody, "已经归档的完整内容") {
		t.Fatalf("Existing.CandidateBody = %q", provider.lastReq.Existing.CandidateBody)
	}
	if !strings.Contains(provider.lastReq.Existing.CandidateBody, "错误整理") {
		t.Fatalf("Existing.CandidateBody should preserve current context too, got %q", provider.lastReq.Existing.CandidateBody)
	}
	if !sameStringSet(provider.lastReq.Existing.CandidateTags, []string{"归档", "历史"}) {
		t.Fatalf("Existing.CandidateTags = %+v", provider.lastReq.Existing.CandidateTags)
	}
	sp := waitForScratchpad(t, store, "history", func(sp scratchpad.Scratchpad) bool {
		return strings.Contains(sp.SessionContext.CandidateBody, "补充后的内容")
	})
	if sp.CommittedThoughtID != "thought-1" {
		t.Fatalf("CommittedThoughtID = %q, want thought-1", sp.CommittedThoughtID)
	}
	if sp.ArchivePreview != nil {
		t.Fatalf("ArchivePreview should be cleared after a new user turn, got %+v", sp.ArchivePreview)
	}
	if sp.ArchiveIntent != scratchpad.ArchiveIntentNone {
		t.Fatalf("ArchiveIntent = %q, want none", sp.ArchiveIntent)
	}
}

func TestScratchpadServiceAppendMessagePreservesUncoveredUserTurn(t *testing.T) {
	store := newMemoryScratchpad()
	provider := &stubCaptureContextProvider{
		result: ai.CaptureContextResult{
			CandidateSummary: "LLM summary without latest details",
			CandidateBody:    "LLM body without latest details",
			ArchiveIntent:    "none",
			ArchiveStrategy:  "new",
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
	)

	latest := "使用 Golang 开发语言，文件方式进行数据存储"
	if _, err := svc.AppendMessage("s1", "user", latest); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return strings.Contains(sp.SessionContext.CandidateSummary, "待整合信息")
	})
	if !containsStrings(sp.SessionContext.ConfirmedFacts, "用户补充："+latest) {
		t.Fatalf("ConfirmedFacts should preserve latest user turn, got %+v", sp.SessionContext.ConfirmedFacts)
	}
	if !strings.Contains(sp.SessionContext.CandidateSummary, latest) || !strings.Contains(sp.SessionContext.CandidateBody, latest) {
		t.Fatalf("latest turn not preserved in summary/body:\nsummary=%s\nbody=%s", sp.SessionContext.CandidateSummary, sp.SessionContext.CandidateBody)
	}
	if !strings.Contains(sp.Messages[len(sp.Messages)-1].Text, latest) {
		t.Fatalf("AI reply should surface preserved latest turn, got %+v", sp.Messages)
	}
}

func TestScratchpadServiceAppendMessageSurfacesUncoveredConflictTurn(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			ConfirmedFacts:   []string{"使用 Golang 开发"},
			CandidateSummary: "当前按 Golang 收敛",
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	provider := &stubCaptureContextProvider{
		result: ai.CaptureContextResult{
			CandidateSummary: "LLM summary without conflict",
			CandidateBody:    "LLM body without conflict",
			ArchiveIntent:    "none",
			ArchiveStrategy:  "new",
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
	)

	latest := "改成 Python，不再使用 Golang"
	if _, err := svc.AppendMessage("s1", "user", latest); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return strings.Contains(sp.SessionContext.CandidateSummary, "需确认的冲突/变更")
	})
	if len(sp.SessionContext.Conflicts) == 0 || !strings.Contains(strings.Join(sp.SessionContext.Conflicts, "\n"), latest) {
		t.Fatalf("Conflicts should preserve latest conflict turn, got %+v", sp.SessionContext.Conflicts)
	}
	if !strings.Contains(sp.SessionContext.CandidateSummary, latest) {
		t.Fatalf("conflict not surfaced in summary:\n%s", sp.SessionContext.CandidateSummary)
	}
}

func TestScratchpadServiceAppendMessageKeepsPendingUntilProviderCompletes(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "old title",
			CandidateSummary: "old summary",
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	provider := &stubCaptureContextProvider{result: ai.CaptureContextResult{
		CandidateTitle:   "new title",
		CandidateSummary: "new summary for follow-up note",
		ArchiveIntent:    "none",
		ArchiveStrategy:  "new",
	}}
	hub := newRecordingEventHub(false)
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(hub),
	)

	if _, err := svc.AppendMessage("s1", "user", "follow-up note"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if provider.callCount() != 0 {
		t.Fatalf("provider should wait for EventHub dispatch, calls = %d", provider.callCount())
	}
	if hub.Count(models.EventScratchpadContextEnrichRequested) != 1 || hub.events[0].LaneKey() != "capture.context.s1" {
		t.Fatalf("enrich request event not queued on session lane: %+v", hub.events)
	}
	if hub.Count(models.EventScratchpadContextUpdated) != 0 {
		t.Fatalf("context update event should not be published before provider completes, got %d", len(hub.events))
	}
	sp, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hasSessionContext(sp.SessionContext) {
		t.Fatalf("SessionContext should stay empty while pending, got %+v", sp.SessionContext)
	}

	hub.DispatchAll()
	sp = waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return sp.SessionContext.CandidateSummary == "new summary for follow-up note"
	})
	if sp.SessionContext.CandidateTitle != "new title" || sp.SessionContext.CandidateSummary != "new summary for follow-up note" {
		t.Fatalf("SessionContext = %+v", sp.SessionContext)
	}
	if len(sp.Messages) != 2 || sp.Messages[0].Role != "user" || sp.Messages[1].Role != "ai" || sp.Messages[1].Text != "new summary for follow-up note" {
		t.Fatalf("Messages = %+v", sp.Messages)
	}
	if hub.Count(models.EventScratchpadContextUpdated) != 1 {
		t.Fatalf("events = %+v", hub.events)
	}
}

func TestScratchpadServiceAppendMessageAllowsConsecutiveUserTurnsBeforeContextReply(t *testing.T) {
	store := newMemoryScratchpad()
	provider := &stubCaptureContextProvider{result: ai.CaptureContextResult{
		CandidateSummary: "synthesized reply after both turns",
		ArchiveIntent:    "none",
		ArchiveStrategy:  "new",
	}}
	hub := newRecordingEventHub(false)
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(hub),
	)

	if _, err := svc.AppendMessage("s1", "user", "first user turn"); err != nil {
		t.Fatalf("AppendMessage first: %v", err)
	}
	if _, err := svc.AppendMessage("s1", "user", "second user turn"); err != nil {
		t.Fatalf("AppendMessage second: %v", err)
	}
	beforeReply, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get before reply: %v", err)
	}
	if len(beforeReply.Messages) != 2 || beforeReply.Messages[0].Role != "user" || beforeReply.Messages[1].Role != "user" {
		t.Fatalf("Messages before reply = %+v", beforeReply.Messages)
	}

	hub.DispatchAll()
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return len(sp.Messages) == 3
	})
	if len(sp.Messages) != 3 {
		t.Fatalf("Messages = %+v", sp.Messages)
	}
	want := []scratchpad.Message{
		{Role: "user", Text: "first user turn"},
		{Role: "user", Text: "second user turn"},
		{Role: "ai", Text: "synthesized reply after both turns"},
	}
	for idx, expected := range want {
		if sp.Messages[idx].Role != expected.Role || sp.Messages[idx].Text != expected.Text {
			t.Fatalf("Messages[%d] = %+v, want role=%q text=%q", idx, sp.Messages[idx], expected.Role, expected.Text)
		}
	}
}

func TestScratchpadServiceUpdateSessionContextDoesNotPersistAIReply(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	if _, err := svc.AppendMessage("s1", "user", "first user turn"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := svc.UpdateSessionContext("s1", scratchpad.SessionContext{
		CandidateSummary: "manual context edit",
	}); err != nil {
		t.Fatalf("UpdateSessionContext: %v", err)
	}
	sp, err := store.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(sp.Messages) != 1 || sp.Messages[0].Role != "user" {
		t.Fatalf("Messages = %+v", sp.Messages)
	}
}

func TestScratchpadServiceUpdateSessionContextMirrorsArchiveIntent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	sp, err := svc.UpdateSessionContext("s1", scratchpad.SessionContext{
		CandidateSummary: "ready to archive",
		ArchiveIntent:    scratchpad.ArchiveIntentLLM,
		ArchiveStrategy:  scratchpad.ArchiveStrategyNew,
	})
	if err != nil {
		t.Fatalf("UpdateSessionContext: %v", err)
	}
	if sp.SessionContext.ArchiveIntent != scratchpad.ArchiveIntentLLM || sp.ArchiveIntent != scratchpad.ArchiveIntentLLM {
		t.Fatalf("ArchiveIntent session=%q top=%q", sp.SessionContext.ArchiveIntent, sp.ArchiveIntent)
	}
	if sp.SessionContext.ArchiveStrategy != scratchpad.ArchiveStrategyNew || sp.ArchiveStrategy != scratchpad.ArchiveStrategyNew {
		t.Fatalf("ArchiveStrategy session=%q top=%q", sp.SessionContext.ArchiveStrategy, sp.ArchiveStrategy)
	}
}

func TestScratchpadServiceAppendMessagePreservesExistingContextWhenProviderReturnsPartial(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			ConfirmedFacts:    []string{"old fact"},
			OpenQuestions:     []string{"old question?"},
			Conflicts:         []string{"old conflict"},
			CandidateTitle:    "old title",
			CandidateTags:     []string{"old-tag"},
			CandidateSummary:  "old summary",
			SourceLinks:       []string{"https://old.example"},
			RelatedThoughtIDs: []string{"thought-old"},
			SuggestedTopicIDs: []string{"topic-old"},
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	provider := &stubCaptureContextProvider{
		result: ai.CaptureContextResult{
			CandidateTitle:   "new title",
			CandidateTags:    []string{"new-tag"},
			CandidateSummary: "new summary for follow-up note",
			ConfirmedFacts:   []string{"new fact"},
			SourceLinks:      []string{"https://new.example"},
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
	)

	if _, err := svc.AppendMessage("s1", "user", "follow-up note"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return sp.SessionContext.CandidateSummary == "new summary for follow-up note"
	})
	if sp.SessionContext.CandidateTitle != "new title" {
		t.Fatalf("CandidateTitle = %q", sp.SessionContext.CandidateTitle)
	}
	if sp.SessionContext.CandidateSummary != "new summary for follow-up note" {
		t.Fatalf("CandidateSummary = %q", sp.SessionContext.CandidateSummary)
	}
	if !containsStrings(sp.SessionContext.ConfirmedFacts, "old fact", "new fact") {
		t.Fatalf("ConfirmedFacts = %+v", sp.SessionContext.ConfirmedFacts)
	}
	if !sameStringSet(sp.SessionContext.CandidateTags, []string{"old-tag", "new-tag"}) {
		t.Fatalf("CandidateTags = %+v", sp.SessionContext.CandidateTags)
	}
	if !containsStrings(sp.SessionContext.SourceLinks, "https://old.example", "https://new.example") {
		t.Fatalf("SourceLinks = %+v", sp.SessionContext.SourceLinks)
	}
	if len(sp.SessionContext.OpenQuestions) != 0 {
		t.Fatalf("OpenQuestions should be replaced by provider output, got %+v", sp.SessionContext.OpenQuestions)
	}
	if !containsStrings(sp.SessionContext.Conflicts, "old conflict") {
		t.Fatalf("Conflicts = %+v", sp.SessionContext.Conflicts)
	}
	if !containsStrings(sp.SessionContext.RelatedThoughtIDs, "thought-old") {
		t.Fatalf("RelatedThoughtIDs = %+v", sp.SessionContext.RelatedThoughtIDs)
	}
	if !containsStrings(sp.SessionContext.SuggestedTopicIDs, "topic-old") {
		t.Fatalf("SuggestedTopicIDs = %+v", sp.SessionContext.SuggestedTopicIDs)
	}
}

func TestScratchpadServiceAppendMessageReplacesOpenQuestionsFromProvider(t *testing.T) {
	store := newMemoryScratchpad()
	if _, err := store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			OpenQuestions: []string{"old unresolved question?"},
		},
	}); err != nil {
		t.Fatalf("seed scratchpad: %v", err)
	}
	provider := &stubCaptureContextProvider{
		result: ai.CaptureContextResult{
			OpenQuestions: []string{"new unresolved question?"},
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
	)

	if _, err := svc.AppendMessage("s1", "user", "follow-up note"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return len(sp.SessionContext.OpenQuestions) > 0
	})
	if !sameStringSet(sp.SessionContext.OpenQuestions, []string{"new unresolved question?"}) {
		t.Fatalf("OpenQuestions = %+v", sp.SessionContext.OpenQuestions)
	}
}

func TestScratchpadServiceAppendMessageSurfacesProviderFailure(t *testing.T) {
	store := newMemoryScratchpad()
	provider := &stubCaptureContextProvider{
		err: ai.ProviderError{Code: "thoughtflow.ai.http_status", StatusCode: 401, Message: "invalid key"},
	}
	hub := newRecordingEventHub(true)
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(hub),
	)

	if _, err := svc.AppendMessage("s1", "user", "needs llm synthesis"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return len(sp.Messages) == 2
	})
	if hasSessionContext(sp.SessionContext) {
		t.Fatalf("failure should not create archiveable context, got %+v", sp.SessionContext)
	}
	if sp.Messages[1].Role != "ai" || !strings.Contains(sp.Messages[1].Text, "鉴权失败") {
		t.Fatalf("failure message = %+v", sp.Messages[1])
	}
	if provider.callCount() != 1 {
		t.Fatalf("non-retryable provider calls = %d, want 1", provider.callCount())
	}
	if hub.Count(models.EventScratchpadContextUpdated) != 1 {
		t.Fatalf("events = %+v", hub.events)
	}
}

func TestScratchpadServiceAppendMessageRetriesTransientProviderFailure(t *testing.T) {
	store := newMemoryScratchpad()
	provider := &stubCaptureContextProvider{
		errs: []error{
			ai.ProviderError{Code: "thoughtflow.ai.transient_status", StatusCode: 529, Message: "cluster busy", Retryable: true},
			nil,
		},
		result: ai.CaptureContextResult{
			CandidateSummary: "retry succeeded",
			CandidateBody:    "retry body",
			ArchiveIntent:    "none",
			ArchiveStrategy:  "new",
		},
	}
	svc := NewScratchpadService(store,
		WithCaptureContextProvider(provider),
		WithEventHub(newRecordingEventHub(true)),
		WithCaptureContextRetryDelays(0),
	)

	if _, err := svc.AppendMessage("s1", "user", "needs retry"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp := waitForScratchpad(t, store, "s1", func(sp scratchpad.Scratchpad) bool {
		return sp.SessionContext.CandidateSummary == "retry succeeded"
	})
	if provider.callCount() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.callCount())
	}
	if len(sp.Messages) != 2 || sp.Messages[1].Text != "retry succeeded" {
		t.Fatalf("messages = %+v", sp.Messages)
	}
	if strings.Contains(sp.Messages[1].Text, "暂时不可用") {
		t.Fatalf("transient failure leaked to user: %+v", sp.Messages)
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := map[string]int{}
	for _, item := range left {
		seen[item]++
	}
	for _, item := range right {
		if seen[item] == 0 {
			return false
		}
		seen[item]--
	}
	return true
}

func containsStrings(haystack []string, needles ...string) bool {
	values := map[string]struct{}{}
	for _, item := range haystack {
		values[item] = struct{}{}
	}
	for _, needle := range needles {
		if _, ok := values[needle]; !ok {
			return false
		}
	}
	return true
}

func TestScratchpadServiceAppendMessageRejectsEmptyFields(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	if _, err := svc.AppendMessage("", "user", "hi"); err == nil {
		t.Fatalf("empty session id should error")
	}
	if _, err := svc.AppendMessage("s1", "user", "   "); err == nil {
		t.Fatalf("whitespace text should error")
	}
}

func TestScratchpadServiceCaptureContextTimeoutCanBeConfigured(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	if svc.contextTimeout != defaultCaptureContextEnrichTimeout {
		t.Fatalf("default context timeout = %v, want %v", svc.contextTimeout, defaultCaptureContextEnrichTimeout)
	}
	svc = NewScratchpadService(newMemoryScratchpad(), WithCaptureContextTimeout(9*time.Minute))
	if svc.contextTimeout != 9*time.Minute {
		t.Fatalf("configured context timeout = %v, want 9m", svc.contextTimeout)
	}
	svc = NewScratchpadService(newMemoryScratchpad(), WithCaptureContextTimeout(0))
	if svc.contextTimeout != defaultCaptureContextEnrichTimeout {
		t.Fatalf("zero timeout should keep default, got %v", svc.contextTimeout)
	}
	if len(svc.contextRetryDelays) != len(defaultCaptureContextRetryDelays) {
		t.Fatalf("default retry delays = %+v", svc.contextRetryDelays)
	}
	svc = NewScratchpadService(newMemoryScratchpad(), WithCaptureContextRetryDelays(0, time.Millisecond))
	if len(svc.contextRetryDelays) != 2 || svc.contextRetryDelays[0] != 0 || svc.contextRetryDelays[1] != time.Millisecond {
		t.Fatalf("configured retry delays = %+v", svc.contextRetryDelays)
	}
}

type recordingEventHub struct {
	events       []mcevent.Event
	observers    map[string][]mcevent.Observer
	autoDispatch bool
	dispatched   int
}

func newRecordingEventHub(autoDispatch bool) *recordingEventHub {
	return &recordingEventHub{
		observers:    map[string][]mcevent.Observer{},
		autoDispatch: autoDispatch,
	}
}

func (h *recordingEventHub) Subscribe(eventID string, observer mcevent.Observer) {
	if h.observers == nil {
		h.observers = map[string][]mcevent.Observer{}
	}
	h.observers[eventID] = append(h.observers[eventID], observer)
}

func (h *recordingEventHub) Unsubscribe(eventID string, observer mcevent.Observer) {
	observers := h.observers[eventID]
	next := observers[:0]
	for _, candidate := range observers {
		if candidate.ID() != observer.ID() {
			next = append(next, candidate)
		}
	}
	if len(next) == 0 {
		delete(h.observers, eventID)
		return
	}
	h.observers[eventID] = next
}

func (h *recordingEventHub) Post(ev mcevent.Event) {
	h.events = append(h.events, ev)
	if h.autoDispatch {
		h.DispatchAll()
	}
}

func (h *recordingEventHub) Send(ev mcevent.Event) mcevent.Result {
	h.events = append(h.events, ev)
	result := mcevent.NewResult(ev.ID(), ev.Source(), ev.Destination())
	h.dispatch(ev, result)
	return result
}

func (h *recordingEventHub) Terminate(context.Context) {}

func (h *recordingEventHub) DispatchAll() {
	for h.dispatched < len(h.events) {
		ev := h.events[h.dispatched]
		h.dispatched++
		h.dispatch(ev, nil)
	}
}

func (h *recordingEventHub) Count(eventID string) int {
	count := 0
	for _, ev := range h.events {
		if ev.ID() == eventID {
			count++
		}
	}
	return count
}

func (h *recordingEventHub) dispatch(ev mcevent.Event, result mcevent.Result) {
	for eventID, observers := range h.observers {
		if !ev.Match(eventID) {
			continue
		}
		for _, observer := range observers {
			if !recordingEventMatchesDestination(ev, observer) {
				continue
			}
			observer.Notify(ev, result)
		}
	}
}

func recordingEventMatchesDestination(ev mcevent.Event, observer mcevent.Observer) bool {
	destination := ev.Destination()
	observerID := observer.ID()
	return mcevent.MatchValue(destination, observerID) || mcevent.MatchValue(observerID, destination)
}

type stubCaptureContextProvider struct {
	mu      sync.Mutex
	calls   int
	lastReq ai.CaptureContextRequest
	result  ai.CaptureContextResult
	err     error
	errs    []error
}

func (s *stubCaptureContextProvider) BuildCaptureContext(_ context.Context, req ai.CaptureContextRequest) (ai.CaptureContextResult, error) {
	s.mu.Lock()
	s.calls++
	s.lastReq = req
	call := s.calls
	s.mu.Unlock()
	if call <= len(s.errs) && s.errs[call-1] != nil {
		return ai.CaptureContextResult{}, s.errs[call-1]
	}
	if s.err != nil {
		return ai.CaptureContextResult{}, s.err
	}
	return s.result, nil
}

func (s *stubCaptureContextProvider) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition was not met before timeout")
}

func waitForScratchpad(t *testing.T, store *memoryScratchpad, id string, condition func(scratchpad.Scratchpad) bool) scratchpad.Scratchpad {
	t.Helper()
	var latest scratchpad.Scratchpad
	waitFor(t, func() bool {
		sp, err := store.Get(id)
		if err != nil {
			return false
		}
		latest = sp
		return condition(sp)
	})
	return latest
}

func TestScratchpadServiceAppendDraftMergesAndProjects(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	_, err := svc.AppendDraft("s1", scratchpad.Draft{
		TitleSet:        "renamed",
		TagsAdded:       []string{"ai", "draft"},
		TopicIDs:        []string{"topic-1"},
		RefineRequested: true,
	})
	if err != nil {
		t.Fatalf("AppendDraft: %v", err)
	}
	sp, _ := store.Get("s1")
	if sp.Title != "renamed" {
		t.Fatalf("Title = %q, want renamed", sp.Title)
	}
	if len(sp.Tags) != 2 || sp.Tags[0] != "ai" {
		t.Fatalf("Tags = %v, want [ai draft]", sp.Tags)
	}
	if len(sp.TopicHints) != 1 || sp.TopicHints[0] != "topic-1" {
		t.Fatalf("TopicHints = %v", sp.TopicHints)
	}
	if !sp.Draft.RefineRequested {
		t.Fatalf("RefineRequested not set")
	}
	if sp.Draft.TitleSet != "renamed" {
		t.Fatalf("Draft.TitleSet = %q", sp.Draft.TitleSet)
	}
}

func TestScratchpadServiceAppendDraftTagsAddedAndRemovedDedupe(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)

	// First round: add ai + draft.
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{TagsAdded: []string{"ai", "draft"}}); err != nil {
		t.Fatalf("AppendDraft add: %v", err)
	}
	// Second round: add ai again (idempotent) + remove draft.
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{
		TagsAdded:   []string{"ai", "extra"},
		TagsRemoved: []string{"draft"},
	}); err != nil {
		t.Fatalf("AppendDraft remove: %v", err)
	}
	sp, _ := store.Get("s1")
	want := []string{"ai", "extra"}
	if len(sp.Tags) != 2 || sp.Tags[0] != "ai" || sp.Tags[1] != "extra" {
		t.Fatalf("Tags = %v, want %v", sp.Tags, want)
	}
	if len(sp.Draft.TagsAdded) != 3 {
		// ai was added twice but union dedupes the persisted TagAdded
		// list at append time too — check the top-level Tags only here.
		t.Fatalf("Draft.TagsAdded len = %d, want 3 (union keeps first seen)", len(sp.Draft.TagsAdded))
	}
}

func TestScratchpadServiceAppendDraftNotesAppendToContent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "hi"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{NotesAppended: []string{"my note"}}); err != nil {
		t.Fatalf("AppendDraft: %v", err)
	}
	sp, _ := store.Get("s1")
	if sp.Content != "hi\n\nmy note" {
		t.Fatalf("Content = %q", sp.Content)
	}
	if len(sp.Draft.NotesAppended) != 1 {
		t.Fatalf("NotesAppended len = %d", len(sp.Draft.NotesAppended))
	}
}

func TestScratchpadServiceBuildCaptureCommandFlattens(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "hello world"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{
		TitleSet:  "My Title",
		TagsAdded: []string{"a"},
	}); err != nil {
		t.Fatalf("AppendDraft: %v", err)
	}
	sp, _ := store.Get("s1")
	sp.SessionContext.CandidateBody = "hello world"
	sp, _ = store.Save(sp)
	cmd, err := svc.BuildCaptureCommand(sp)
	if err != nil {
		t.Fatalf("BuildCaptureCommand: %v", err)
	}
	if cmd.Content != "hello world" {
		t.Fatalf("Content = %q", cmd.Content)
	}
	if cmd.Title != "My Title" {
		t.Fatalf("Title = %q", cmd.Title)
	}
	if len(cmd.Tags) != 1 || cmd.Tags[0] != "a" {
		t.Fatalf("Tags = %v", cmd.Tags)
	}
	if cmd.Source != models.ThoughtSourceScratchpadCommit {
		t.Fatalf("Source = %q", cmd.Source)
	}
}

func TestScratchpadServiceBuildCaptureCommandUsesEnrichedContext(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "raw request",
		Title:     "raw title",
		Tags:      []string{"raw"},
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "LLM title",
			CandidateTags:    []string{"llm", "archive"},
			CandidateBody:    "raw request",
			CandidateSummary: "## 当前收敛结论\n\n- 已整理成完整归档正文\n- 包含下一步行动",
		},
	}
	cmd, err := svc.BuildCaptureCommand(sp)
	if err != nil {
		t.Fatalf("BuildCaptureCommand: %v", err)
	}
	if cmd.Content != "## 当前收敛结论\n\n- 已整理成完整归档正文\n- 包含下一步行动" {
		t.Fatalf("Content = %q", cmd.Content)
	}
	if cmd.Title != "LLM title" {
		t.Fatalf("Title = %q", cmd.Title)
	}
	if len(cmd.Tags) != 2 || cmd.Tags[0] != "llm" || cmd.Tags[1] != "archive" {
		t.Fatalf("Tags = %v", cmd.Tags)
	}
}

func TestScratchpadServiceBuildCaptureCommandInferURLType(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "see https://example.com for details"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp, _ := store.Get("s1")
	sp.SessionContext.CandidateBody = "final URL synthesis"
	sp, _ = store.Save(sp)
	cmd, err := svc.BuildCaptureCommand(sp)
	if err != nil {
		t.Fatalf("BuildCaptureCommand: %v", err)
	}
	if cmd.Type != models.ThoughtTypeURL {
		t.Fatalf("Type = %q, want url", cmd.Type)
	}
}

func TestScratchpadServiceBuildCaptureCommandDefaultsToTextType(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	if _, err := svc.AppendMessage("s1", "user", "just a plain text thought, no url"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp, _ := store.Get("s1")
	sp.SessionContext.CandidateBody = "final plain text synthesis"
	sp, _ = store.Save(sp)
	cmd, err := svc.BuildCaptureCommand(sp)
	if err != nil {
		t.Fatalf("BuildCaptureCommand: %v", err)
	}
	if cmd.Type != models.ThoughtTypeText {
		t.Fatalf("Type = %q, want text", cmd.Type)
	}
}

func TestScratchpadServiceBuildCaptureCommandRejectsEmptyContent(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	_, err := svc.BuildCaptureCommand(scratchpad.Scratchpad{SessionID: "s1"})
	if err == nil {
		t.Fatalf("empty content should error")
	}
}

func TestScratchpadServiceBuildCaptureCommandRejectsRawOnlyContent(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	_, err := svc.BuildCaptureCommand(scratchpad.Scratchpad{SessionID: "s1", Content: "raw user input"})
	if err == nil {
		t.Fatalf("raw-only content should not be archivable without final llm context")
	}
}

func TestScratchpadServiceBuildCaptureCommandRejectsAlreadyCommitted(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = svc.AppendMessage("s1", "user", "hello")
	sp, _ := store.Get("s1")
	sp.CommittedThoughtID = "thought-1"
	_, err := svc.BuildCaptureCommand(sp)
	if !errors.Is(err, ErrAlreadyCommitted) {
		t.Fatalf("err = %v, want ErrAlreadyCommitted", err)
	}
}

func TestScratchpadServiceResetAfterCommitClearsVolatileKeepsLink(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = svc.AppendMessage("s1", "user", "draft")
	_, _ = store.MarkCommitted("s1", "thought-1")
	reset, err := svc.ResetAfterCommit("s1")
	if err != nil {
		t.Fatalf("ResetAfterCommit: %v", err)
	}
	if reset.Content != "" || len(reset.Messages) != 0 {
		t.Fatalf("volatile fields not cleared: %+v", reset)
	}
	if reset.CommittedThoughtID != "thought-1" {
		t.Fatalf("committed link lost: %+v", reset)
	}
}

func TestScratchpadServiceResetAfterCommitOnUncommittedIsPlainReset(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = svc.AppendMessage("s1", "user", "draft")
	reset, err := svc.ResetAfterCommit("s1")
	if err != nil {
		t.Fatalf("ResetAfterCommit: %v", err)
	}
	if reset.Content != "" {
		t.Fatalf("Content = %q", reset.Content)
	}
	if reset.CommittedThoughtID != "" {
		t.Fatalf("should still be uncommitted, got %+v", reset)
	}
}

func TestScratchpadServiceRejectsNilStore(t *testing.T) {
	svc := NewScratchpadService(nil)
	if _, err := svc.AppendMessage("s1", "user", "x"); !errors.Is(err, ErrScratchpadUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.AppendDraft("s1", scratchpad.Draft{}); !errors.Is(err, ErrScratchpadUnavailable) {
		t.Fatalf("AppendDraft err = %v", err)
	}
	if _, err := svc.ResetAfterCommit("s1"); !errors.Is(err, ErrScratchpadUnavailable) {
		t.Fatalf("ResetAfterCommit err = %v", err)
	}
}

func TestScratchpadServiceUpdateSessionContextReplacesBlock(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	// Pre-stage: existing scratchpad with old context.
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			Topic: "old",
		},
	})
	_, err := svc.UpdateSessionContext("s1", scratchpad.SessionContext{
		Topic:             "new topic",
		Goal:              "summarise",
		ConfirmedFacts:    []string{"fact-1", "  ", "fact-2"},
		OpenQuestions:     []string{"q1"},
		Conflicts:         []string{},
		CandidateTitle:    "  ", // whitespace-only → empty
		CandidateTags:     []string{"ai", "", "draft"},
		CandidateSummary:  "summary",
		CandidateBody:     "body",
		SourceLinks:       []string{"https://x", " "},
		RelatedThoughtIDs: []string{"t-1"},
		SuggestedTopicIDs: []string{"topic-1"},
	})
	if err != nil {
		t.Fatalf("UpdateSessionContext: %v", err)
	}
	sp, _ := store.Get("s1")
	if sp.SessionContext.Topic != "new topic" {
		t.Fatalf("Topic = %q", sp.SessionContext.Topic)
	}
	if sp.SessionContext.Goal != "summarise" {
		t.Fatalf("Goal = %q", sp.SessionContext.Goal)
	}
	if len(sp.SessionContext.ConfirmedFacts) != 2 || sp.SessionContext.ConfirmedFacts[0] != "fact-1" {
		t.Fatalf("ConfirmedFacts = %+v (whitespace should be dropped)", sp.SessionContext.ConfirmedFacts)
	}
	if sp.SessionContext.CandidateTitle != "" {
		t.Fatalf("CandidateTitle should be empty after trim, got %q", sp.SessionContext.CandidateTitle)
	}
	if len(sp.SessionContext.CandidateTags) != 2 {
		t.Fatalf("CandidateTags = %+v", sp.SessionContext.CandidateTags)
	}
	if sp.SessionContext.CandidateBody != "body" {
		t.Fatalf("CandidateBody not preserved (CandidateBody is NOT trimmed): %q", sp.SessionContext.CandidateBody)
	}
}

func TestScratchpadServiceUpdateSessionContextCreatesAbsentSession(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp, err := svc.UpdateSessionContext("absent", scratchpad.SessionContext{Topic: "auto-created"})
	if err != nil {
		t.Fatalf("UpdateSessionContext: %v", err)
	}
	if sp.SessionID != "absent" {
		t.Fatalf("SessionID = %q", sp.SessionID)
	}
	if sp.SessionContext.Topic != "auto-created" {
		t.Fatalf("Topic = %q", sp.SessionContext.Topic)
	}
}

func TestScratchpadServiceUpdateSessionContextRejectsEmptySessionID(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	if _, err := svc.UpdateSessionContext("", scratchpad.SessionContext{}); err == nil {
		t.Fatalf("empty session id should error")
	}
}

func TestScratchpadServiceSetArchiveIntentNormalisesUnknown(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = store.Save(scratchpad.Scratchpad{SessionID: "s1"})
	// menu → kept
	sp, err := svc.SetArchiveIntent("s1", scratchpad.ArchiveIntentMenu)
	if err != nil {
		t.Fatalf("SetArchiveIntent menu: %v", err)
	}
	if sp.ArchiveIntent != scratchpad.ArchiveIntentMenu {
		t.Fatalf("ArchiveIntent = %q", sp.ArchiveIntent)
	}
	// llm → kept
	sp, _ = svc.SetArchiveIntent("s1", scratchpad.ArchiveIntentLLM)
	if sp.ArchiveIntent != scratchpad.ArchiveIntentLLM {
		t.Fatalf("ArchiveIntent = %q", sp.ArchiveIntent)
	}
	// unknown → none
	sp, _ = svc.SetArchiveIntent("s1", scratchpad.ArchiveIntent("bogus"))
	if sp.ArchiveIntent != scratchpad.ArchiveIntentNone {
		t.Fatalf("bogus intent should normalise to none, got %q", sp.ArchiveIntent)
	}
}

func TestScratchpadServiceSetArchiveIntentRejectsEmptySessionID(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	if _, err := svc.SetArchiveIntent("", scratchpad.ArchiveIntentMenu); err == nil {
		t.Fatalf("empty session id should error")
	}
}

func TestScratchpadServiceSetArchiveStrategyPersistsSourceThoughtID(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = store.Save(scratchpad.Scratchpad{SessionID: "s1"})
	sp, err := svc.SetArchiveStrategy("s1", scratchpad.ArchiveStrategySupplement, "thought-parent")
	if err != nil {
		t.Fatalf("SetArchiveStrategy: %v", err)
	}
	if sp.ArchiveStrategy != scratchpad.ArchiveStrategySupplement {
		t.Fatalf("ArchiveStrategy = %q", sp.ArchiveStrategy)
	}
	if sp.SourceThoughtID != "thought-parent" {
		t.Fatalf("SourceThoughtID = %q (should be stamped)", sp.SourceThoughtID)
	}
}

func TestScratchpadServiceSetArchiveStrategyDefaultsToNewOnUnknown(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	_, _ = store.Save(scratchpad.Scratchpad{SessionID: "s1"})
	sp, _ := svc.SetArchiveStrategy("s1", scratchpad.ArchiveStrategy("what"), "")
	if sp.ArchiveStrategy != scratchpad.ArchiveStrategyNew {
		t.Fatalf("unknown strategy should default to new, got %q", sp.ArchiveStrategy)
	}
	if sp.SourceThoughtID != "" {
		t.Fatalf("SourceThoughtID should remain empty, got %q", sp.SourceThoughtID)
	}
}

func TestScratchpadServiceSetArchiveStrategyRejectsEmptySessionID(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad())
	if _, err := svc.SetArchiveStrategy("", scratchpad.ArchiveStrategyNew, ""); err == nil {
		t.Fatalf("empty session id should error")
	}
}

func TestScratchpadServiceBuildArchivePreviewNewStrategyDefaultsToBodyAndTags(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "raw content",
		Tags:      []string{"raw"},
		SessionContext: scratchpad.SessionContext{
			CandidateTitle: "previewed title",
			CandidateBody:  "## Body\n\nfrom context",
			CandidateTags:  []string{"ctx", "draft"},
			SourceLinks:    []string{"https://x"},
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyNew,
	}
	preview, err := svc.BuildArchivePreview(sp, nil)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if preview.Strategy != scratchpad.ArchiveStrategyNew {
		t.Fatalf("Strategy = %q", preview.Strategy)
	}
	if preview.Title != "previewed title" {
		t.Fatalf("Title = %q", preview.Title)
	}
	if preview.Body != "## Body\n\nfrom context" {
		t.Fatalf("Body = %q (should prefer session_context.candidate_body)", preview.Body)
	}
	if len(preview.Tags) != 2 || preview.Tags[0] != "ctx" || preview.Tags[1] != "draft" {
		t.Fatalf("Tags = %v (should prefer session_context.candidate_tags)", preview.Tags)
	}
	if len(preview.SourceLinks) != 1 || preview.SourceLinks[0] != "https://x" {
		t.Fatalf("SourceLinks = %v", preview.SourceLinks)
	}
	if preview.Diff != nil {
		t.Fatalf("Diff should be nil for new strategy, got %+v", preview.Diff)
	}
}

func TestScratchpadServiceBuildArchivePreviewUsesRicherSummaryWhenBodyIsRaw(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "raw request",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "previewed title",
			CandidateBody:    "raw request",
			CandidateSummary: "## 当前收敛结论\n\n- 已补全背景、约束与待确认事项\n- 可直接归档为 Thought",
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyNew,
	}
	preview, err := svc.BuildArchivePreview(sp, nil)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if preview.Body != "## 当前收敛结论\n\n- 已补全背景、约束与待确认事项\n- 可直接归档为 Thought" {
		t.Fatalf("Body = %q", preview.Body)
	}
}

func TestScratchpadServiceBuildArchivePreviewIncludesMissingAIHistory(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "用户第一轮输入\n\n用户第二轮输入",
		Messages: []scratchpad.Message{
			{Role: "user", Text: "用户第一轮输入"},
			{Role: "ai", Text: "第一轮整理：需要保留的背景和目标。"},
			{Role: "user", Text: "用户第二轮输入"},
			{Role: "ai", Text: "第二轮整理：需要保留的约束和风险。"},
		},
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "previewed title",
			CandidateBody:    "## 最终整理\n\n- 第二轮整理：需要保留的约束和风险。",
			CandidateSummary: "## 最终整理\n\n- 第二轮整理：需要保留的约束和风险。",
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyNew,
	}
	preview, err := svc.BuildArchivePreview(sp, nil)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if !strings.Contains(preview.Body, "第二轮整理：需要保留的约束和风险。") {
		t.Fatalf("Body missing latest synthesis: %q", preview.Body)
	}
	if !strings.Contains(preview.Body, "第一轮整理：需要保留的背景和目标。") {
		t.Fatalf("Body missing prior AI synthesis: %q", preview.Body)
	}
	if strings.Contains(preview.Body, "用户第一轮输入") || strings.Contains(preview.Body, "用户第二轮输入") {
		t.Fatalf("Body should not archive raw user input: %q", preview.Body)
	}
}

func TestScratchpadServiceBuildArchivePreviewDoesNotUseRawScratchpadContent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID:  "s1",
		Content:    "scratchpad body",
		Title:      "scratchpad title",
		Tags:       []string{"a", "b"},
		URL:        "https://y",
		TopicHints: []string{"topic-1"},
		Draft:      scratchpad.Draft{TitleSet: "renamed"},
		// no SessionContext
		ArchiveStrategy: scratchpad.ArchiveStrategyNew,
	}
	preview, err := svc.BuildArchivePreview(sp, nil)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if preview.Title != "renamed" {
		t.Fatalf("Title = %q (should fall back to draft.title_set)", preview.Title)
	}
	if preview.Body != "" {
		t.Fatalf("Body = %q (should not fall back to raw scratchpad content)", preview.Body)
	}
	if len(preview.SourceLinks) != 1 || preview.SourceLinks[0] != "https://y" {
		t.Fatalf("SourceLinks = %v (should fall back to URL)", preview.SourceLinks)
	}
	if len(preview.RelatedTopics) != 1 || preview.RelatedTopics[0] != "topic-1" {
		t.Fatalf("RelatedTopics = %v", preview.RelatedTopics)
	}
}

func TestScratchpadServiceBuildArchivePreviewUpdateRequiresCurrentThought(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID:       "s1",
		Content:         "new body",
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
	}
	if _, err := svc.BuildArchivePreview(sp, nil); !errors.Is(err, ErrDiffRequired) {
		t.Fatalf("err = %v, want ErrDiffRequired", err)
	}
}

func TestScratchpadServiceBuildArchivePreviewUpdateComputesDiff(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			CandidateBody: "## New Body\n\nchanged",
			CandidateTags: []string{"x", "y"},
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
	}
	current := &models.ThoughtSnapshot{
		Thought: models.Thought{
			ID:        "thought-1",
			UserTitle: "Old Body",
			UserTags:  []string{"a"},
		},
		Content: models.ThoughtContent{Original: "old raw"},
	}
	preview, err := svc.BuildArchivePreview(sp, current)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if preview.Diff == nil {
		t.Fatalf("Diff should be non-nil for update_thought")
	}
	if preview.Diff.Before != "Old Body" {
		t.Fatalf("Diff.Before = %q (should use UserTitle)", preview.Diff.Before)
	}
	if preview.Diff.After != "## New Body\n\nchanged" {
		t.Fatalf("Diff.After = %q", preview.Diff.After)
	}
	wantChanged := map[string]bool{"body": true, "tags": true}
	for _, c := range preview.Diff.ChangedFields {
		if !wantChanged[c] {
			t.Fatalf("unexpected changed field: %q", c)
		}
		delete(wantChanged, c)
	}
	if len(wantChanged) != 0 {
		t.Fatalf("missing changed fields: %v", wantChanged)
	}
}

func TestScratchpadServiceBuildArchivePreviewUpdateDetectsTagOnlyChange(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			CandidateBody: "same body",
			CandidateTags: []string{"x", "y"},
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
	}
	current := &models.ThoughtSnapshot{
		Thought: models.Thought{
			ID:        "thought-1",
			UserTitle: "same body",
			UserTags:  []string{"a", "b"},
		},
	}
	preview, err := svc.BuildArchivePreview(sp, current)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if len(preview.Diff.ChangedFields) != 1 || preview.Diff.ChangedFields[0] != "tags" {
		t.Fatalf("expected only tags in changed fields, got %+v", preview.Diff.ChangedFields)
	}
}

func TestScratchpadServiceBuildArchivePreviewUpdateDetectsNoChange(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			CandidateBody: "same",
			CandidateTags: []string{"a", "b"},
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
	}
	current := &models.ThoughtSnapshot{
		Thought: models.Thought{
			ID:        "thought-1",
			UserTitle: "same",
			UserTags:  []string{"a", "b"},
		},
	}
	preview, err := svc.BuildArchivePreview(sp, current)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if len(preview.Diff.ChangedFields) != 0 {
		t.Fatalf("expected no changed fields, got %+v", preview.Diff.ChangedFields)
	}
}

func TestScratchpadServiceBuildArchivePreviewSupplementPrependsParentTag(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID: "s1",
		SessionContext: scratchpad.SessionContext{
			CandidateBody: "supplement body",
		},
		ArchiveStrategy: scratchpad.ArchiveStrategySupplement,
	}
	current := &models.ThoughtSnapshot{
		Thought: models.Thought{ID: "parent-1"},
	}
	preview, err := svc.BuildArchivePreview(sp, current)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if !strings.HasPrefix(preview.Body, "[补充] 前置 thought-parent-1") {
		t.Fatalf("Body = %q (should start with [补充] 前置 tag)", preview.Body)
	}
	if preview.ThoughtID != "parent-1" {
		t.Fatalf("ThoughtID = %q (should echo parent for back-link)", preview.ThoughtID)
	}
}

func TestScratchpadServiceBuildArchivePreviewUnknownStrategyDefaultsToNew(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store)
	sp := scratchpad.Scratchpad{
		SessionID:       "s1",
		Content:         "x",
		ArchiveStrategy: scratchpad.ArchiveStrategy("what"),
	}
	preview, err := svc.BuildArchivePreview(sp, nil)
	if err != nil {
		t.Fatalf("BuildArchivePreview: %v", err)
	}
	if preview.Strategy != scratchpad.ArchiveStrategyNew {
		t.Fatalf("Strategy = %q (unknown should default to new)", preview.Strategy)
	}
}

func TestScratchpadServiceSameTagSetIgnoresOrderAndDuplicates(t *testing.T) {
	a := []string{"a", "b", "a"}
	b := []string{"b", "a", "a"}
	if !sameTagSet(a, b) {
		t.Fatalf("sameTagSet should ignore order and duplicates")
	}
	c := []string{"a", "b", "c"}
	if sameTagSet(a, c) {
		t.Fatalf("sameTagSet should detect different sets")
	}
}

func TestTrimNonEmpty(t *testing.T) {
	got := trimNonEmpty([]string{"a", "  ", "b", "", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("trimNonEmpty = %v", got)
	}
}

func TestUnionStringsPreservesOrderDedupe(t *testing.T) {
	got := unionStrings([]string{"a", "b"}, []string{"b", "c", "a"})
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("union = %v, want %v", got, want)
	}
}

func TestSubtractStringsRemovesAllOccurrences(t *testing.T) {
	got := subtractStrings([]string{"a", "b", "a", "c"}, []string{"a"})
	want := []string{"b", "c"}
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("subtract = %v, want %v", got, want)
	}
}

// stubCapture records Capture / PatchThought / ApplyDraftInternal
// calls so the commit pipeline can be exercised without a real
// capture Service. The two patch paths are tracked separately so
// tests can assert which one the scratchpad commit flow chose —
// fresh commits must go through ApplyDraftInternal (no lock) so
// the async refiner / expander don't see "thought is locked" and
// skip the thought forever.
type stubCapture struct {
	captureCalls       int
	patchCalls         int
	applyCalls         int
	getCalls           int
	patchReq           models.ThoughtPatchRequest
	applyReq           models.ThoughtPatchRequest
	captureCmd         models.CaptureCommand
	captureResult      models.CaptureResult
	patchResult        models.ThoughtSnapshot
	applyResult        models.ThoughtSnapshot
	getThoughtResult   models.ThoughtSnapshot
	patchErr           error
	applyErr           error
	captureErr         error
	getThoughtErr      error
	lastPatchRaw       []byte
	lastApplyRaw       []byte
	lastSessionID      string
	lastApplySessionID string
}

func (s *stubCapture) Capture(_ context.Context, cmd models.CaptureCommand) (models.CaptureResult, error) {
	s.captureCalls++
	s.captureCmd = cmd
	if s.captureErr != nil {
		return models.CaptureResult{}, s.captureErr
	}
	return s.captureResult, nil
}

func (s *stubCapture) PatchThought(_ context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	s.patchCalls++
	s.patchReq = request
	s.lastPatchRaw = rawBody
	s.lastSessionID = sessionID
	if s.patchErr != nil {
		return models.ThoughtSnapshot{}, s.patchErr
	}
	return s.patchResult, nil
}

func (s *stubCapture) ApplyDraftInternal(_ context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	s.applyCalls++
	s.applyReq = request
	s.lastApplyRaw = rawBody
	s.lastApplySessionID = sessionID
	if s.applyErr != nil {
		return models.ThoughtSnapshot{}, s.applyErr
	}
	return s.applyResult, nil
}

func (s *stubCapture) GetThought(_ context.Context, thoughtID string) (models.ThoughtSnapshot, error) {
	s.getCalls++
	if s.getThoughtErr != nil {
		return models.ThoughtSnapshot{}, s.getThoughtErr
	}
	return s.getThoughtResult, nil
}

func TestScratchpadServiceCommitFreshFiresCaptureAndMarksCommitted(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{
		captureResult: models.CaptureResult{
			Thought: models.Thought{ID: "thought-1", Type: models.ThoughtTypeText},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.AppendMessage("s1", "user", "draft content"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	sp, _ := store.Get("s1")
	sp.SessionContext.CandidateBody = "final draft content"
	_, _ = store.Save(sp)
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Thought.ID != "thought-1" {
		t.Fatalf("thought id = %q", result.Thought.ID)
	}
	if captureStub.captureCalls != 1 {
		t.Fatalf("Capture called %d times, want 1", captureStub.captureCalls)
	}
	// The final body was already written by Capture, so a fresh
	// commit with no extra draft commands should not patch the
	// thought again.
	if captureStub.applyCalls != 0 {
		t.Fatalf("ApplyDraftInternal called %d, want 0", captureStub.applyCalls)
	}
	if captureStub.patchCalls != 0 {
		t.Fatalf("PatchThought should not be called on fresh commit, got %d", captureStub.patchCalls)
	}
	sp, _ = store.Get("s1")
	if sp.CommittedThoughtID != "thought-1" {
		t.Fatalf("CommittedThoughtID = %q, want thought-1", sp.CommittedThoughtID)
	}
	if sp.CommittedAt == nil {
		t.Fatalf("CommittedAt not set")
	}
	if sp.Content != "" || len(sp.Messages) != 0 {
		t.Fatalf("ResetAfterCommit did not clear volatile fields: %+v", sp)
	}
}

func TestScratchpadServiceCommitFreshPersistsFinalLLMSynthesis(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "raw request",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "LLM title",
			CandidateTags:    []string{"llm"},
			CandidateBody:    "raw request",
			CandidateSummary: "## 当前收敛结论\n\n- 最终整理结果\n- 可直接进入归档",
		},
	})
	captureStub := &stubCapture{
		captureResult: models.CaptureResult{
			Thought: models.Thought{ID: "thought-1", Type: models.ThoughtTypeText},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.captureCmd.Content != "## 当前收敛结论\n\n- 最终整理结果\n- 可直接进入归档" {
		t.Fatalf("captured content = %q", captureStub.captureCmd.Content)
	}
	if captureStub.captureCmd.Title != "LLM title" {
		t.Fatalf("captured title = %q", captureStub.captureCmd.Title)
	}
	if len(captureStub.captureCmd.Tags) != 1 || captureStub.captureCmd.Tags[0] != "llm" {
		t.Fatalf("captured tags = %v", captureStub.captureCmd.Tags)
	}
}

func TestScratchpadServiceCommitFreshUsesPersistedArchivePreviewBody(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "raw request",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "LLM title",
			CandidateBody:    "candidate body that should not win",
			CandidateSummary: "candidate summary that should not win",
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyNew,
		ArchivePreview: &scratchpad.ArchivePreview{
			Title:    "preview title",
			Body:     "preview body shown to the user",
			Strategy: scratchpad.ArchiveStrategyNew,
		},
	})
	captureStub := &stubCapture{
		captureResult: models.CaptureResult{
			Thought: models.Thought{ID: "thought-1", Type: models.ThoughtTypeText},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.captureCmd.Content != "preview body shown to the user" {
		t.Fatalf("captured content = %q", captureStub.captureCmd.Content)
	}
}

func TestScratchpadServiceCommitUpdateUsesPersistedArchivePreviewBody(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "raw request",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "Updated Title",
			CandidateTags:    []string{"updated"},
			CandidateBody:    "candidate body that should not win",
			CandidateSummary: "candidate summary that should not win",
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
		SourceThoughtID: "thought-source",
		ArchivePreview: &scratchpad.ArchivePreview{
			Title:    "preview title",
			Body:     "preview body shown to the user",
			Strategy: scratchpad.ArchiveStrategyUpdate,
		},
	})
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{ID: "thought-source", UserTitle: "Original Title"},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.patchReq.AINotes == nil || *captureStub.patchReq.AINotes != "preview body shown to the user" {
		t.Fatalf("patched ai notes = %v", captureStub.patchReq.AINotes)
	}
	if captureStub.patchReq.Body != nil {
		t.Fatalf("patched body should be empty for AI Notes update, got %v", captureStub.patchReq.Body)
	}
}

func TestAppendContextReplyMessageDedupesExistingAIReply(t *testing.T) {
	at := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	messages := []scratchpad.Message{
		{Role: "user", Text: "将上述内容归档", At: at},
		{Role: "ai", Text: "## 当前收敛结论\n\n- 最终整理结果", At: at},
		{Role: "user", Text: "归档", At: at},
	}
	got := appendContextReplyMessage(messages, "## 当前收敛结论\n\n- 最终整理结果", at)
	if len(got) != len(messages) {
		t.Fatalf("message count = %d, want %d", len(got), len(messages))
	}
}

func TestScratchpadServiceCommitRepeatAppendsToExistingThought(t *testing.T) {
	store := newMemoryScratchpad()
	// Pre-stage: scratchpad already committed to thought-1.
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:          "s1",
		Content:            "first round",
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
	})
	// User adds more content + a rename + a tag.
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "first round\n\nmore thoughts",
		SessionContext: scratchpad.SessionContext{
			CandidateBody: "final more thoughts",
		},
		Draft: scratchpad.Draft{
			TitleSet:  "renamed",
			TagsAdded: []string{"new-tag"},
		},
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
	})

	captureStub := &stubCapture{}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.captureCalls != 0 {
		t.Fatalf("Capture should not run on repeat commit, called %d", captureStub.captureCalls)
	}
	// Repeat commit goes through the lock-free ApplyDraftInternal
	// path so the refiner / expander async jobs don't see the
	// thought as "locked by an active session" and skip it forever.
	if captureStub.applyCalls != 1 {
		t.Fatalf("ApplyDraftInternal called %d, want 1", captureStub.applyCalls)
	}
	if captureStub.patchCalls != 0 {
		t.Fatalf("PatchThought should not be called on repeat commit (would race refiner), got %d", captureStub.patchCalls)
	}
	if captureStub.applyReq.Title == nil || *captureStub.applyReq.Title != "renamed" {
		t.Fatalf("Title = %v, want renamed", captureStub.applyReq.Title)
	}
	if captureStub.applyReq.Tags == nil || len(*captureStub.applyReq.Tags) != 1 {
		t.Fatalf("Tags = %v", captureStub.applyReq.Tags)
	}
	if captureStub.applyReq.Body == nil || *captureStub.applyReq.Body != "final more thoughts" {
		t.Fatalf("Body = %v", captureStub.applyReq.Body)
	}
	if captureStub.lastApplySessionID != "s1" {
		t.Fatalf("session id = %q, want s1", captureStub.lastApplySessionID)
	}
	if result.Thought.ID != "thought-1" {
		t.Fatalf("result thought id = %q, want thought-1", result.Thought.ID)
	}
}

func TestScratchpadServiceCommitRepeatAppendsFinalLLMSynthesis(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "raw follow-up",
		SessionContext: scratchpad.SessionContext{
			CandidateBody:    "raw follow-up",
			CandidateSummary: "## 后续整理\n\n- 新增内容已经合并\n- 待办事项已经收口",
		},
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
	})
	captureStub := &stubCapture{}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.applyReq.Body == nil || *captureStub.applyReq.Body != "## 后续整理\n\n- 新增内容已经合并\n- 待办事项已经收口" {
		t.Fatalf("Body = %v", captureStub.applyReq.Body)
	}
}

func TestScratchpadServiceCommitRequiresCaptureWiring(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{SessionID: "s1", Content: "x"})
	svc := NewScratchpadService(store) // no WithCapture
	_, err := svc.Commit(context.Background(), "s1")
	if err == nil || !strings.Contains(err.Error(), "not wired up") {
		t.Fatalf("err = %v, want wiring error", err)
	}
}

func TestScratchpadServiceCommitRejectsEmptyContent(t *testing.T) {
	store := newMemoryScratchpad()
	svc := NewScratchpadService(store, WithCapture(&stubCapture{}))
	_, err := svc.Commit(context.Background(), "s1")
	if err == nil {
		t.Fatalf("empty content should error")
	}
}

func TestScratchpadServiceCommitRepeatWithNoDraftChangesIsNoop(t *testing.T) {
	store := newMemoryScratchpad()
	// Pre-stage: scratchpad already committed, but no new content.
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:          "s1",
		CommittedThoughtID: "thought-1",
		CommittedAt:        ptrTime(),
	})
	captureStub := &stubCapture{}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.patchCalls != 0 {
		t.Fatalf("Patch should not run when nothing changed, called %d", captureStub.patchCalls)
	}
	if result.Thought.ID != "thought-1" {
		t.Fatalf("result thought id = %q", result.Thought.ID)
	}
	// Reset should still happen so the UI is in a clean state.
	sp, _ := store.Get("s1")
	if sp.Content != "" || len(sp.Draft.TagsAdded) != 0 {
		t.Fatalf("scratchpad not reset: %+v", sp)
	}
}

func ptrTime() *time.Time {
	t := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	return &t
}

func TestScratchpadServiceCommitUpdateThoughtFiresPatchWithSource(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "drafted body",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "Updated Title",
			CandidateBody:    "drafted body",
			CandidateSummary: "## New Body\n\n- updated synthesis",
			CandidateTags:    []string{"updated"},
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
		SourceThoughtID: "thought-source",
	})
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{ID: "thought-source", UserTitle: "Original Title"},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.patchCalls != 1 {
		t.Fatalf("PatchThought called %d, want 1", captureStub.patchCalls)
	}
	if captureStub.getCalls != 1 {
		t.Fatalf("GetThought called %d, want 1", captureStub.getCalls)
	}
	if captureStub.applyCalls != 0 {
		t.Fatalf("ApplyDraftInternal should NOT be used for update_thought, called %d", captureStub.applyCalls)
	}
	if result.Thought.ID != "thought-source" {
		t.Fatalf("result thought id = %q, want thought-source", result.Thought.ID)
	}
	if captureStub.patchReq.Title == nil || *captureStub.patchReq.Title != "Updated Title" {
		t.Fatalf("Title = %v, want Updated Title", captureStub.patchReq.Title)
	}
	if captureStub.patchReq.Tags == nil || len(*captureStub.patchReq.Tags) != 1 || (*captureStub.patchReq.Tags)[0] != "updated" {
		t.Fatalf("Tags = %v, want [updated]", captureStub.patchReq.Tags)
	}
	if captureStub.patchReq.Body != nil {
		t.Fatalf("Body should be empty for AI Notes update, got %v", captureStub.patchReq.Body)
	}
	if captureStub.patchReq.AINotes == nil || *captureStub.patchReq.AINotes != "## New Body\n\n- updated synthesis" {
		t.Fatalf("AINotes = %v", captureStub.patchReq.AINotes)
	}
	// The update lands on the source file, so the scratchpad is marked
	// committed to that same thought instead of remaining a draft.
	sp, _ := store.Get("s1")
	if sp.CommittedThoughtID != "thought-source" || sp.CommittedAt == nil {
		t.Fatalf("update_thought should stamp source as committed, got %+v", sp)
	}
}

func TestScratchpadServiceCommitUpdateThoughtRejectsMissingSource(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:       "s1",
		Content:         "x",
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
		// no SourceThoughtID
	})
	captureStub := &stubCapture{}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err == nil || !strings.Contains(err.Error(), "source_thought_id") {
		t.Fatalf("err = %v, want missing source_thought_id", err)
	}
	if captureStub.patchCalls != 0 {
		t.Fatalf("Patch should not be called when source missing, got %d", captureStub.patchCalls)
	}
}

func TestScratchpadServiceCommitUpdateThoughtRejectsGetThoughtError(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:       "s1",
		Content:         "x",
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
		SourceThoughtID: "thought-missing",
	})
	captureStub := &stubCapture{getThoughtErr: errors.New("not found")}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err == nil || !strings.Contains(err.Error(), "source thought not found") {
		t.Fatalf("err = %v, want source thought not found", err)
	}
}

func TestScratchpadServiceCommitUpdateThoughtNoChangesDegradesToReset(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:       "s1",
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
		SourceThoughtID: "thought-source",
		// no CandidateTitle / CandidateBody / CandidateTags
	})
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{ID: "thought-source"},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.patchCalls != 0 {
		t.Fatalf("Patch should not be called when nothing changed, got %d", captureStub.patchCalls)
	}
	if result.Thought.ID != "thought-source" {
		t.Fatalf("result thought id = %q, want thought-source", result.Thought.ID)
	}
	sp, _ := store.Get("s1")
	if sp.Content != "" || len(sp.Messages) != 0 {
		t.Fatalf("Reset should still run, got %+v", sp)
	}
}

func TestScratchpadServiceCommitSupplementFiresCaptureWithoutAINotesBacklink(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID: "s1",
		Content:   "supplement content",
		Tags:      []string{"ai"},
		SessionContext: scratchpad.SessionContext{
			CandidateTitle: "Supplement",
			CandidateBody:  "final supplement content",
		},
		ArchiveStrategy: scratchpad.ArchiveStrategySupplement,
		SourceThoughtID: "thought-parent",
	})
	captureStub := &stubCapture{
		captureResult: models.CaptureResult{
			Thought: models.Thought{ID: "thought-new", Type: models.ThoughtTypeText},
		},
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{ID: "thought-parent", UserTitle: "Parent"},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	result, err := svc.Commit(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.captureCalls != 1 {
		t.Fatalf("Capture called %d, want 1", captureStub.captureCalls)
	}
	// Capture should have been called with the supplement prefix prepended.
	if !strings.HasPrefix(captureStub.captureResult.Thought.ID, "thought-new") {
		t.Fatalf("result thought = %+v", captureStub.captureResult)
	}
	if !strings.HasPrefix(captureStub.captureCmd.Content, "[补充] 前置 thought-thought-parent") {
		t.Fatalf("capture content missing supplement prefix: %q", captureStub.captureCmd.Content)
	}
	if captureStub.applyCalls != 0 {
		t.Fatalf("ApplyDraftInternal should not write duplicate AI Notes, got %d", captureStub.applyCalls)
	}
	// scratchpad's CommittedThoughtID should point at the new thought.
	sp, _ := store.Get("s1")
	if sp.CommittedThoughtID != "thought-new" {
		t.Fatalf("CommittedThoughtID = %q, want thought-new", sp.CommittedThoughtID)
	}
	if result.Thought.ID != "thought-new" {
		t.Fatalf("result thought id = %q, want thought-new", result.Thought.ID)
	}
}

func TestScratchpadServiceCommitSupplementRejectsMissingParent(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:       "s1",
		Content:         "x",
		ArchiveStrategy: scratchpad.ArchiveStrategySupplement,
		// no SourceThoughtID
	})
	captureStub := &stubCapture{}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err == nil || !strings.Contains(err.Error(), "source_thought_id") {
		t.Fatalf("err = %v, want missing source_thought_id", err)
	}
	if captureStub.captureCalls != 0 {
		t.Fatalf("Capture should not run when source missing, called %d", captureStub.captureCalls)
	}
}

func TestScratchpadServiceCommitUnknownStrategyDegradesToNew(t *testing.T) {
	store := newMemoryScratchpad()
	_, _ = store.Save(scratchpad.Scratchpad{
		SessionID:       "s1",
		Content:         "draft",
		SessionContext:  scratchpad.SessionContext{CandidateBody: "final draft"},
		ArchiveStrategy: scratchpad.ArchiveStrategy("bogus"),
	})
	captureStub := &stubCapture{
		captureResult: models.CaptureResult{
			Thought: models.Thought{ID: "thought-fresh", Type: models.ThoughtTypeText},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("s1"))
	if _, err := svc.Commit(context.Background(), "s1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if captureStub.captureCalls != 1 {
		t.Fatalf("unknown strategy should route to commitFresh (Capture), called %d", captureStub.captureCalls)
	}
}

func TestScratchpadServiceReopenFromThoughtSeedsContext(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{
				ID:                "thought-1",
				UserTitle:         "Original Title",
				ExtractedTitle:    "Extracted",
				URL:               "https://example.com",
				UserTags:          []string{"alpha"},
				AITags:            []string{"beta", "alpha"},
				TopicIDs:          []string{"topic-1"},
				RelatedThoughtIDs: []string{"other-1", "other-2"},
				URLFollowups: []models.URLFollowup{
					{URL: "https://followup.com", Title: "Followup"},
				},
			},
			Content: models.ThoughtContent{
				ExtractedContent: "extracted body",
				AINotes:          "AI-generated notes",
			},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("scratchpad"))
	sp, err := svc.ReopenFromThought(context.Background(), "thought-1", "")
	if err != nil {
		t.Fatalf("ReopenFromThought: %v", err)
	}
	if sp.SessionID == "" {
		t.Fatalf("SessionID should be auto-generated when empty")
	}
	if sp.SourceThoughtID != "thought-1" {
		t.Fatalf("SourceThoughtID = %q", sp.SourceThoughtID)
	}
	if sp.Title != "Original Title" {
		t.Fatalf("Title = %q (UserTitle wins over ExtractedTitle)", sp.Title)
	}
	if sp.Content != "" {
		t.Fatalf("Content = %q (reopen should not restore archived content as user input)", sp.Content)
	}
	wantBody := "AI-generated notes"
	if len(sp.Messages) != 1 || sp.Messages[0].Role != "ai" || sp.Messages[0].Text != wantBody {
		t.Fatalf("Messages = %+v (should restore final archived content as an ai bubble)", sp.Messages)
	}
	if len(sp.Tags) != 2 {
		t.Fatalf("Tags = %v (UserTags ∪ AITags, deduplicated)", sp.Tags)
	}
	if sp.ArchiveStrategy != scratchpad.ArchiveStrategyUpdate {
		t.Fatalf("ArchiveStrategy = %q, want update_thought", sp.ArchiveStrategy)
	}
	if sp.ArchiveIntent != scratchpad.ArchiveIntentMenu {
		t.Fatalf("ArchiveIntent = %q, want menu", sp.ArchiveIntent)
	}
	if len(sp.SessionContext.SourceLinks) != 2 {
		t.Fatalf("SourceLinks = %v (URL + URLFollowups)", sp.SessionContext.SourceLinks)
	}
	if len(sp.SessionContext.RelatedThoughtIDs) != 2 {
		t.Fatalf("RelatedThoughtIDs = %v", sp.SessionContext.RelatedThoughtIDs)
	}
	if len(sp.TopicHints) != 1 || sp.TopicHints[0] != "topic-1" {
		t.Fatalf("TopicHints = %v", sp.TopicHints)
	}
	if sp.SessionContext.CandidateTitle != "Original Title" {
		t.Fatalf("CandidateTitle = %q", sp.SessionContext.CandidateTitle)
	}
	if sp.SessionContext.CandidateBody != wantBody {
		t.Fatalf("CandidateBody = %q", sp.SessionContext.CandidateBody)
	}
	if sp.SessionContext.CandidateSummary != wantBody {
		t.Fatalf("CandidateSummary = %q", sp.SessionContext.CandidateSummary)
	}
}

func TestScratchpadServiceReopenCommitUpdatesSourceThought(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{
				ID:        "thought-1",
				UserTitle: "Original Title",
				UserTags:  []string{"alpha"},
			},
			Content: models.ThoughtContent{AINotes: "previous archive"},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub))
	sp, err := svc.ReopenFromThought(context.Background(), "thought-1", "reopen-session")
	if err != nil {
		t.Fatalf("ReopenFromThought: %v", err)
	}
	sp.SessionContext.CandidateTitle = "Updated Title"
	sp.SessionContext.CandidateTags = []string{"alpha", "updated"}
	sp.SessionContext.CandidateBody = "latest conversation"
	sp.SessionContext.CandidateSummary = "## Final\n\n- latest synthesis"
	if _, err := store.Save(sp); err != nil {
		t.Fatalf("Save reopened scratchpad: %v", err)
	}

	result, err := svc.Commit(context.Background(), "reopen-session")
	if err != nil {
		t.Fatalf("Commit reopened scratchpad: %v", err)
	}
	if result.Thought.ID != "thought-1" {
		t.Fatalf("result thought id = %q, want thought-1", result.Thought.ID)
	}
	if captureStub.captureCalls != 0 {
		t.Fatalf("reopen commit should not create a new thought, Capture called %d", captureStub.captureCalls)
	}
	if captureStub.patchCalls != 1 {
		t.Fatalf("reopen commit should patch source thought, PatchThought called %d", captureStub.patchCalls)
	}
	if captureStub.patchReq.Body != nil {
		t.Fatalf("patched body should be empty for AI Notes update, got %v", captureStub.patchReq.Body)
	}
	if captureStub.patchReq.AINotes == nil || *captureStub.patchReq.AINotes != "## Final\n\n- latest synthesis" {
		t.Fatalf("patched ai notes = %v", captureStub.patchReq.AINotes)
	}
	if captureStub.patchReq.Title == nil || *captureStub.patchReq.Title != "Updated Title" {
		t.Fatalf("patched title = %v", captureStub.patchReq.Title)
	}
	committed, _ := store.Get("reopen-session")
	if committed.CommittedThoughtID != "thought-1" || committed.CommittedAt == nil {
		t.Fatalf("reopen update should mark original thought committed, got %+v", committed)
	}
	if committed.Content != "" || len(committed.Messages) != 0 {
		t.Fatalf("reopen update should reset volatile fields, got %+v", committed)
	}
}

func TestScratchpadServiceReopenFromThoughtUsesSuppliedSessionID(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{ID: "thought-1"},
			Content: models.ThoughtContent{AINotes: "x"},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub), WithSessionID("scratchpad"))
	sp, err := svc.ReopenFromThought(context.Background(), "thought-1", "my-supplied-id")
	if err != nil {
		t.Fatalf("ReopenFromThought: %v", err)
	}
	if sp.SessionID != "my-supplied-id" {
		t.Fatalf("SessionID = %q, want my-supplied-id", sp.SessionID)
	}
}

func TestScratchpadServiceReopenFromThoughtRestoresAINotes(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{ID: "thought-1"},
			Content: models.ThoughtContent{
				AINotes: "from ai notes",
			},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub))
	sp, err := svc.ReopenFromThought(context.Background(), "thought-1", "")
	if err != nil {
		t.Fatalf("ReopenFromThought: %v", err)
	}
	if sp.Content != "" {
		t.Fatalf("Content = %q (reopen should keep user input empty)", sp.Content)
	}
	if len(sp.Messages) != 1 || sp.Messages[0].Role != "ai" || sp.Messages[0].Text != "from ai notes" {
		t.Fatalf("Messages = %+v (should restore AINotes as an ai bubble)", sp.Messages)
	}
}

func TestScratchpadServiceReopenFromThoughtNormalizesSectionHeadings(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{
		getThoughtResult: models.ThoughtSnapshot{
			Thought: models.Thought{ID: "thought-1"},
			Content: models.ThoughtContent{
				AINotes: "## AI Notes\n\n### 2026-06-18 00:00:00 UTC\nextra note",
			},
		},
	}
	svc := NewScratchpadService(store, WithCapture(captureStub))
	sp, err := svc.ReopenFromThought(context.Background(), "thought-1", "")
	if err != nil {
		t.Fatalf("ReopenFromThought: %v", err)
	}
	want := "### 2026-06-18 00:00:00 UTC\nextra note"
	if len(sp.Messages) != 1 || sp.Messages[0].Role != "ai" || sp.Messages[0].Text != want {
		t.Fatalf("Messages = %+v, want ai bubble %q", sp.Messages, want)
	}
	if sp.Content != "" {
		t.Fatalf("Content = %q (reopen should keep user input empty)", sp.Content)
	}
}

func TestScratchpadServiceReopenFromThoughtRejectsEmptyThoughtID(t *testing.T) {
	svc := NewScratchpadService(newMemoryScratchpad(), WithCapture(&stubCapture{}))
	if _, err := svc.ReopenFromThought(context.Background(), "", ""); err == nil {
		t.Fatalf("empty thought id should error")
	}
}

func TestScratchpadServiceReopenFromThoughtSurfacesGetThoughtError(t *testing.T) {
	store := newMemoryScratchpad()
	captureStub := &stubCapture{getThoughtErr: errors.New("not found")}
	svc := NewScratchpadService(store, WithCapture(captureStub))
	_, err := svc.ReopenFromThought(context.Background(), "thought-missing", "")
	if err == nil || !strings.Contains(err.Error(), "source thought not found") {
		t.Fatalf("err = %v, want source thought not found", err)
	}
}
