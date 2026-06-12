package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/observability"
)

const localEmbeddingModel = "local-hash-embedding-v1"
const openAIMaxAttempts = 3

type RefineRequest struct {
	Thought models.Thought
	Content models.ThoughtContent
}

type EmbedRequest struct {
	ThoughtID string
	Text      string
}

type SynthesisRequest struct {
	ThoughtIDs  []string
	Goal        string
	Format      string
	Snapshots   []models.ThoughtSnapshot
	SourceLinks []string
}

// ExpandRequest is the post-refine expansion input. The expander
// passes the refined thought, its current content, and the LLM-side
// signals (summary / key points / ai tags) so the LLM can produce a
// self-contained "处理思路与方案" without re-reading the entire
// content body. This is intentionally narrower than RefineRequest —
// the plan does not need the full Content struct.
type ExpandRequest struct {
	Thought models.Thought
	Content models.ThoughtContent
	Summary string
	Tags    []string
}

// ExpandResult is the expansion plan as the LLM returned it. The
// caller persists Result.Plan to the thought's expansion_plan front
// matter and shows it under "处理思路与方案" in the UI.
type ExpandResult struct {
	Plan        string
	Model       string
	GeneratedAt time.Time
}

type RefineProvider interface {
	Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error)
}

type EmbeddingProvider interface {
	Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error)
}

type WeaveProvider interface {
	Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error)
}

type SynthesisProvider interface {
	Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error)
}

type ExpandProvider interface {
	Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error)
}

type Provider interface {
	RefineProvider
	EmbeddingProvider
	WeaveProvider
	SynthesisProvider
}

type ProviderError struct {
	Code       string
	StatusCode int
	Message    string
	Retryable  bool
}

func (e ProviderError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s: status %d: %s", e.Code, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type LocalRefineProvider struct{}

func NewLocalRefineProvider() *LocalRefineProvider {
	return &LocalRefineProvider{}
}

func NewRefineProvider(cfg appconfig.LLMConfig, embeddingCfg appconfig.EmbeddingConfig) RefineProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedRefineProvider{next: NewLocalRefineProvider()}
	}
	return observedRefineProvider{next: NewOpenAICompatibleProvider(cfg, embeddingCfg)}
}

func NewEmbeddingProvider(cfg appconfig.EmbeddingConfig) EmbeddingProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedEmbeddingProvider{next: NewLocalRefineProvider()}
	}
	return observedEmbeddingProvider{next: NewOpenAICompatibleEmbeddingProvider(cfg)}
}

func NewWeaveProvider(cfg appconfig.LLMConfig) WeaveProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedWeaveProvider{next: NewLocalRefineProvider()}
	}
	return observedWeaveProvider{next: NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})}
}

func NewSynthesisProvider(cfg appconfig.LLMConfig) SynthesisProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedSynthesisProvider{next: NewLocalRefineProvider()}
	}
	return observedSynthesisProvider{next: NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})}
}

func NewExpandProvider(cfg appconfig.LLMConfig) ExpandProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedExpandProvider{next: NewLocalRefineProvider()}
	}
	return observedExpandProvider{next: NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})}
}

type observedRefineProvider struct {
	next RefineProvider
}

func (p observedRefineProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	observability.IncrementAIRequest()
	return p.next.Refine(ctx, req)
}

type observedEmbeddingProvider struct {
	next EmbeddingProvider
}

func (p observedEmbeddingProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	observability.IncrementAIRequest()
	return p.next.Embed(ctx, req)
}

type observedWeaveProvider struct {
	next WeaveProvider
}

func (p observedWeaveProvider) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	observability.IncrementAIRequest()
	return p.next.Weave(ctx, req)
}

type observedSynthesisProvider struct {
	next SynthesisProvider
}

func (p observedSynthesisProvider) Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error) {
	observability.IncrementAIRequest()
	return p.next.Synthesize(ctx, req)
}

type observedExpandProvider struct {
	next ExpandProvider
}

func (p observedExpandProvider) Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error) {
	observability.IncrementAIRequest()
	return p.next.Expand(ctx, req)
}

func (p *LocalRefineProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	text := strings.TrimSpace(req.Content.ExtractedContent)
	if text == "" {
		text = strings.TrimSpace(req.Content.Original)
	}
	embedding, _ := p.Embed(ctx, EmbedRequest{ThoughtID: req.Thought.ID, Text: text})
	return models.ThoughtRefinement{
		ThoughtID:   req.Thought.ID,
		Status:      models.RefineStatusRefined,
		Summary:     summarize(text),
		KeyPoints:   keyPoints(text),
		AITags:      inferTags(text),
		Model:       "local-rule",
		InputHash:   models.ContentHash(text),
		GeneratedAt: time.Now().UTC(),
		Embedding:   &embedding,
	}, nil
}

func (p *LocalRefineProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	_ = ctx
	vector := localEmbedding(req.Text, 64)
	return models.EmbeddingRecord{
		ThoughtID:   req.ThoughtID,
		Model:       localEmbeddingModel,
		Dimension:   len(vector),
		Vector:      vector,
		ContentHash: models.ContentHash(req.Text),
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (p *LocalRefineProvider) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	_ = ctx
	document := strings.TrimRight(req.CurrentDocument, "\n")
	if document == "" {
		document = localInitialTopicDocument(req.Topic)
	}
	if strings.Contains(document, req.SourceLink) {
		return models.TopicWeaveResult{Document: document, Model: "local-rule", Strategy: "already-linked"}, nil
	}
	section := localThoughtSection(req, chooseOutlineSection(req))
	if target := chooseOutlineSection(req); target != "" {
		document = insertIntoOutlineSection(document, target, section)
	} else {
		document = strings.TrimRight(document, "\n") + "\n\n" + section
	}
	return models.TopicWeaveResult{Document: strings.TrimRight(document, "\n") + "\n", Model: "local-rule", Strategy: "outline-insert"}, nil
}

func (p *LocalRefineProvider) Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error) {
	_ = ctx
	contentParts := []string{}
	for _, snapshot := range req.Snapshots {
		title := firstNonEmpty(snapshot.Thought.DisplayTitle, snapshot.Thought.UserTitle, snapshot.Thought.ExtractedTitle, snapshot.Thought.ID)
		body := firstNonEmpty(snapshot.Thought.Summary, snapshot.Content.AINotes, snapshot.Content.ExtractedContent, snapshot.Content.Original)
		contentParts = append(contentParts, "## "+title+"\n\n"+body)
	}
	format := firstNonEmpty(req.Format, "summary")
	goal := firstNonEmpty(req.Goal, "Synthesize selected thoughts")
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          models.NewJobID("synthesis", now),
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        goal,
		Format:      format,
		Content:     ensureSynthesisSourceLinks("# "+goal+"\n\n"+strings.Join(contentParts, "\n\n"), req.SourceLinks),
		SourceLinks: req.SourceLinks,
		Model:       "local-rule",
		Status:      "draft",
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// Expand returns a best-effort local expansion plan built from the
// summary, key points, and original content. The result is good
// enough to populate the "处理思路与方案" section when no LLM is
// configured; the LLM path is the production path and is expected
// to overwrite this.
func (p *LocalRefineProvider) Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error) {
	_ = ctx
	title := firstNonEmpty(req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID)
	summary := firstNonEmpty(req.Summary, req.Thought.Summary)
	body := strings.TrimSpace(req.Content.Original)
	if body == "" {
		body = strings.TrimSpace(req.Content.ExtractedContent)
	}
	var plan strings.Builder
	plan.WriteString("## 背景与现状分析\n\n")
	if summary != "" {
		plan.WriteString(summary)
		plan.WriteString("\n\n")
	} else {
		plan.WriteString("基于标题「")
		plan.WriteString(title)
		plan.WriteString("」补全背景：用户提交了一条碎片笔记，需要进一步梳理思路和落地步骤。\n\n")
	}
	if body != "" {
		plan.WriteString("笔记原文要点：\n\n")
		plan.WriteString("- ")
		plan.WriteString(strings.Join(strings.Fields(strings.SplitN(body, "\n", 2)[0]), " "))
		plan.WriteString("\n\n")
	}
	plan.WriteString("## 可能的处理方向\n\n")
	for _, tag := range req.Tags {
		plan.WriteString("- 围绕「")
		plan.WriteString(tag)
		plan.WriteString("」延伸出一个具体的下一步动作\n")
	}
	if len(req.Tags) == 0 {
		plan.WriteString("- 列出 3 个最直接的下一步动作\n")
	}
	plan.WriteString("\n## 推荐的具体步骤\n\n1. 复述本笔记的目标\n2. 关联 1-2 条已有 Thought（搜索 hybrid 模式）\n3. 写入专题并触发 weave\n\n## 关键注意事项\n\n- 涉及 URL 时走 Jina reader 抓取\n- 专题判定阈值默认 0.75，near-miss 阈值 0.55\n- 锁与 refiner 共享，避免与正在 refine 的 Thought 互踩\n\n## 延伸阅读建议\n\n- 现有的相关专题文档\n- 与本笔记同类型的前置笔记")
	return ExpandResult{Plan: plan.String(), Model: "local-rule", GeneratedAt: time.Now().UTC()}, nil
}

type OpenAICompatibleProvider struct {
	baseURL           string
	apiKey            string
	chatModel         string
	client            *http.Client
	embeddingProvider EmbeddingProvider
}

type OpenAICompatibleEmbeddingProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// newLLMHttpClient builds an http.Client whose every timeout layer
// respects `cfg.Timeout`. mnet.NewDNSCacheHttpClient pins
// Transport.ResponseHeaderTimeout to 10s and Client.Timeout to 15s
// internally; a subsequent `client.Timeout = cfg.Timeout` only covers
// the outer deadline, so a slow LLM cold-start would still be killed
// at 10s before the configured 600s ever matters. Cloning the default
// transport lets us lift every ceiling to cfg.Timeout uniformly and
// also raise the TLS handshake to 30s so handshake latency on a
// long-lived connection never causes spurious failures.
func newLLMHttpClient(timeout time.Duration) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tr := base.Clone()
	if timeout > 0 {
		tr.ResponseHeaderTimeout = timeout
		tr.TLSHandshakeTimeout = 30 * time.Second
		tr.ExpectContinueTimeout = 10 * time.Second
		return &http.Client{Timeout: timeout, Transport: tr}
	}
	return &http.Client{Transport: tr}
}

func NewOpenAICompatibleProvider(cfg appconfig.LLMConfig, embeddingCfg appconfig.EmbeddingConfig) *OpenAICompatibleProvider {
	client := newLLMHttpClient(cfg.Timeout)
	var embeddingProvider EmbeddingProvider
	if strings.TrimSpace(embeddingCfg.APIKey) != "" {
		embeddingProvider = NewOpenAICompatibleEmbeddingProvider(embeddingCfg)
	}
	return &OpenAICompatibleProvider{
		baseURL:           strings.TrimRight(firstNonEmpty(cfg.BaseURL, "https://api.openai.com"), "/"),
		apiKey:            strings.TrimSpace(cfg.APIKey),
		chatModel:         firstNonEmpty(cfg.ChatModel, "gpt-4o-mini"),
		client:            client,
		embeddingProvider: embeddingProvider,
	}
}

func NewOpenAICompatibleEmbeddingProvider(cfg appconfig.EmbeddingConfig) *OpenAICompatibleEmbeddingProvider {
	client := newLLMHttpClient(cfg.Timeout)
	return &OpenAICompatibleEmbeddingProvider{
		baseURL: strings.TrimRight(firstNonEmpty(cfg.BaseURL, "https://api.openai.com"), "/"),
		apiKey:  strings.TrimSpace(cfg.APIKey),
		model:   firstNonEmpty(cfg.Model, "text-embedding-3-small"),
		client:  client,
	}
}

func (p *OpenAICompatibleProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	text := strings.TrimSpace(req.Content.ExtractedContent)
	if text == "" {
		text = strings.TrimSpace(req.Content.Original)
	}
	if text == "" {
		return models.ThoughtRefinement{}, errors.New("refine text is empty")
	}
	payload := map[string]any{
		"model": p.chatModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You refine notes for ThoughtFlow. Return strict JSON only with fields summary string, key_points string array, tags string array, title string.",
			},
			{
				"role": "user",
				"content": "Refine this note without changing the original content.\n\nTitle: " +
					firstNonEmpty(req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID) +
					"\n\nContent:\n" + text,
			},
		},
		"temperature": 0.2,
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return models.ThoughtRefinement{}, err
	}
	if len(response.Choices) == 0 {
		return models.ThoughtRefinement{}, errors.New("chat completion returned no choices")
	}
	content := strings.TrimSpace(response.Choices[0].Message.Content)
	var parsed refineJSON
	if err := json.Unmarshal([]byte(extractJSONObject(content)), &parsed); err != nil {
		return models.ThoughtRefinement{}, fmt.Errorf("parse refinement json: %w", err)
	}
	embedding, err := p.Embed(ctx, EmbedRequest{ThoughtID: req.Thought.ID, Text: text})
	if err != nil {
		embedding = models.EmbeddingRecord{}
	}
	refinement := models.ThoughtRefinement{
		ThoughtID:      req.Thought.ID,
		Status:         models.RefineStatusRefined,
		ExtractedTitle: strings.TrimSpace(parsed.Title),
		Summary:        strings.TrimSpace(parsed.Summary),
		KeyPoints:      normalizeList(parsed.KeyPoints),
		AITags:         normalizeList(parsed.Tags),
		Model:          p.chatModel,
		InputHash:      models.ContentHash(text),
		GeneratedAt:    time.Now().UTC(),
	}
	if len(embedding.Vector) > 0 {
		refinement.Embedding = &embedding
	}
	return refinement, nil
}

func (p *OpenAICompatibleProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	if p.embeddingProvider == nil {
		return models.EmbeddingRecord{}, errors.New("embedding provider is not configured")
	}
	return p.embeddingProvider.Embed(ctx, req)
}

func (p *OpenAICompatibleEmbeddingProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return models.EmbeddingRecord{}, errors.New("embedding text is empty")
	}
	payload := map[string]any{
		"model": p.model,
		"input": text,
	}
	var response embeddingResponse
	if err := p.postJSON(ctx, "/embeddings", payload, &response); err != nil {
		return models.EmbeddingRecord{}, err
	}
	if len(response.Data) == 0 || len(response.Data[0].Embedding) == 0 {
		return models.EmbeddingRecord{}, errors.New("embedding response is empty")
	}
	vector := response.Data[0].Embedding
	return models.EmbeddingRecord{
		ThoughtID:   req.ThoughtID,
		Model:       p.model,
		Dimension:   len(vector),
		Vector:      vector,
		ContentHash: models.ContentHash(text),
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (p *OpenAICompatibleEmbeddingProvider) postJSON(ctx context.Context, path string, payload any, target any) error {
	return postOpenAICompatibleJSON(ctx, p.client, p.baseURL, p.apiKey, path, payload, target)
}

func (p *OpenAICompatibleProvider) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	if strings.TrimSpace(req.CurrentDocument) == "" {
		req.CurrentDocument = localInitialTopicDocument(req.Topic)
	}
	section := localThoughtSection(req, "")
	payload := map[string]any{
		"model": p.chatModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a careful Markdown editor for ThoughtFlow topic documents. Return strict JSON only with field document string. Preserve YAML front matter, existing content, and source links. Insert or merge the new thought into the most appropriate existing outline section. Never remove the required source link.",
			},
			{
				"role": "user",
				"content": "Topic name: " + req.Topic.Name +
					"\nTopic description: " + req.Topic.Description +
					"\nRequired source link substring: " + req.SourceLink +
					"\nMatch reasons: " + strings.Join(req.Membership.Reasons, ", ") +
					"\n\nCurrent topic Markdown:\n" + req.CurrentDocument +
					"\n\nNew thought section to weave:\n" + section,
			},
		},
		"temperature": 0.2,
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return models.TopicWeaveResult{}, err
	}
	if len(response.Choices) == 0 {
		return models.TopicWeaveResult{}, errors.New("topic weave returned no choices")
	}
	var parsed topicWeaveJSON
	if err := json.Unmarshal([]byte(extractJSONObject(response.Choices[0].Message.Content)), &parsed); err != nil {
		return models.TopicWeaveResult{}, fmt.Errorf("parse topic weave json: %w", err)
	}
	document := strings.TrimSpace(parsed.Document)
	if document == "" {
		return models.TopicWeaveResult{}, errors.New("topic weave document is empty")
	}
	if !strings.Contains(document, req.SourceLink) {
		return models.TopicWeaveResult{}, errors.New("topic weave document missing required source link")
	}
	return models.TopicWeaveResult{Document: document + "\n", Model: p.chatModel, Strategy: "llm-document-merge"}, nil
}

func (p *OpenAICompatibleProvider) Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error) {
	if len(req.Snapshots) == 0 {
		return models.SynthesisDraft{}, errors.New("synthesis snapshots are required")
	}
	goal := firstNonEmpty(req.Goal, "Synthesize selected thoughts")
	format := firstNonEmpty(req.Format, "summary")
	payload := map[string]any{
		"model": p.chatModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You synthesize ThoughtFlow notes into Markdown. Return strict JSON only with field content string. Preserve every provided source link in a Sources section. Do not invent sources.",
			},
			{
				"role": "user",
				"content": "Goal: " + goal +
					"\nFormat: " + format +
					"\nRequired source links:\n" + strings.Join(req.SourceLinks, "\n") +
					"\n\nInput notes:\n" + renderSynthesisInputs(req.Snapshots),
			},
		},
		"temperature": 0.2,
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return models.SynthesisDraft{}, err
	}
	if len(response.Choices) == 0 {
		return models.SynthesisDraft{}, errors.New("synthesis returned no choices")
	}
	var parsed synthesisJSON
	if err := json.Unmarshal([]byte(extractJSONObject(response.Choices[0].Message.Content)), &parsed); err != nil {
		return models.SynthesisDraft{}, fmt.Errorf("parse synthesis json: %w", err)
	}
	content := strings.TrimSpace(parsed.Content)
	if content == "" {
		return models.SynthesisDraft{}, errors.New("synthesis content is empty")
	}
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          models.NewJobID("synthesis", now),
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        goal,
		Format:      format,
		Content:     ensureSynthesisSourceLinks(content, req.SourceLinks),
		SourceLinks: req.SourceLinks,
		Model:       p.chatModel,
		Status:      "draft",
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (p *OpenAICompatibleProvider) postJSON(ctx context.Context, path string, payload any, target any) error {
	return postOpenAICompatibleJSON(ctx, p.client, p.baseURL, p.apiKey, path, payload, target)
}

// Expand asks the LLM for a "处理思路与方案" Markdown plan. Unlike
// Refine/Weave/Synthesize the response is plain Markdown, not JSON —
// the prompt explicitly forbids code fences, so the caller can show
// the raw `Choices[0].Message.Content` directly.
func (p *OpenAICompatibleProvider) Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error) {
	title := firstNonEmpty(req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID)
	original := firstNonEmpty(strings.TrimSpace(req.Content.Original), strings.TrimSpace(req.Content.ExtractedContent))
	summary := firstNonEmpty(req.Summary, req.Thought.Summary)
	payload := map[string]any{
		"model": p.chatModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": expandSystemPrompt(),
			},
			{
				"role":    "user",
				"content": expandUserPrompt(title, summary, req.Thought.KeyPoints, req.Tags, original, req.Thought.Type, req.Thought.URL),
			},
		},
		"temperature": 0.3,
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return ExpandResult{}, err
	}
	if len(response.Choices) == 0 {
		return ExpandResult{}, errors.New("expand returned no choices")
	}
	plan := strings.TrimSpace(stripMarkdownFence(response.Choices[0].Message.Content))
	if plan == "" {
		return ExpandResult{}, errors.New("expand plan is empty")
	}
	return ExpandResult{Plan: plan, Model: p.chatModel, GeneratedAt: time.Now().UTC()}, nil
}

func expandSystemPrompt() string {
	return "你是 ThoughtFlow 的研究助手。用户提交了一条碎片笔记，请你基于笔记内容产出「处理思路与方案」。" +
		"要求：1. 直接给方案，不要反问、不要「我需要更多信息」。" +
		"2. 必须包含三段并使用 Markdown 二级标题：## 设计思路（分析问题与核心抓手）、## 推荐步骤（用有序列表给 3-6 步可执行动作）、## 参考与延伸（列 2-4 条延伸阅读或相关方向）。" +
		"3. 使用中文，简洁专业；可用列表、引用、加粗；不要输出 ```markdown 围栏，不要复述原文。" +
		"4. 长度 300-500 字；原内容不足时基于常识合理补全，给出清晰的方向与可执行步骤。"
}

func expandUserPrompt(title string, summary string, keyPoints []string, tags []string, original string, thoughtType string, thoughtURL string) string {
	var b strings.Builder
	b.WriteString("标题：")
	b.WriteString(title)
	b.WriteString("\n摘要：")
	b.WriteString(firstNonEmpty(summary, "(无摘要)"))
	b.WriteString("\n关键点：\n")
	if len(keyPoints) == 0 {
		b.WriteString("- (无关键点)\n")
	} else {
		for _, kp := range keyPoints {
			b.WriteString("- ")
			b.WriteString(kp)
			b.WriteString("\n")
		}
	}
	b.WriteString("AI 标签：")
	if len(tags) == 0 {
		b.WriteString("(无)")
	} else {
		b.WriteString(strings.Join(tags, ", "))
	}
	b.WriteString("\n类型：")
	b.WriteString(firstNonEmpty(thoughtType, "text"))
	if thoughtURL != "" {
		b.WriteString("\nURL：")
		b.WriteString(thoughtURL)
	}
	b.WriteString("\n原始内容：\n")
	if strings.TrimSpace(original) == "" {
		b.WriteString("(无原始内容)")
	} else {
		b.WriteString(original)
	}
	b.WriteString("\n\n请输出处理思路与方案。")
	return b.String()
}

// stripMarkdownFence removes an optional ``` ... ``` wrapping that
// some chat models still emit even when prompted not to. It is
// conservative: if the fence markers are not present, the input is
// returned unchanged.
func stripMarkdownFence(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "```") {
		return value
	}
	body := strings.TrimPrefix(trimmed, "```")
	if newline := strings.Index(body, "\n"); newline >= 0 {
		body = body[newline+1:]
	}
	body = strings.TrimRight(body, "`")
	body = strings.TrimRight(body, "\n")
	return strings.TrimSpace(body)
}

func postOpenAICompatibleJSON(ctx context.Context, client *http.Client, baseURL string, apiKey string, path string, payload any, target any) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("ai api key is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= openAIMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICompatibleAPIURL(baseURL, path), bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = ProviderError{Code: "thoughtflow.ai.request_failed", Message: err.Error(), Retryable: true}
			if attempt < openAIMaxAttempts && waitRetryBackoff(ctx, attempt) == nil {
				continue
			}
			return lastErr
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = ProviderError{Code: "thoughtflow.ai.read_failed", Message: readErr.Error(), Retryable: true}
			if attempt < openAIMaxAttempts && waitRetryBackoff(ctx, attempt) == nil {
				continue
			}
			return lastErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			providerErr := classifyProviderStatus(resp.StatusCode, body)
			lastErr = providerErr
			if providerErr.Retryable && attempt < openAIMaxAttempts && waitRetryBackoff(ctx, attempt) == nil {
				continue
			}
			return providerErr
		}
		if err := json.Unmarshal(body, target); err != nil {
			return ProviderError{Code: "thoughtflow.ai.invalid_json", Message: err.Error(), Retryable: false}
		}
		return nil
	}
	return lastErr
}

func classifyProviderStatus(statusCode int, body []byte) ProviderError {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	retryable := statusCode == http.StatusTooManyRequests || statusCode >= 500
	code := "thoughtflow.ai.http_status"
	if retryable {
		code = "thoughtflow.ai.transient_status"
	}
	return ProviderError{
		Code:       code,
		StatusCode: statusCode,
		Message:    message,
		Retryable:  retryable,
	}
}

func waitRetryBackoff(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt*attempt) * 10 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func openAICompatibleAPIURL(baseURL string, path string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1" + path
}

func summarize(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 240 {
		return text
	}
	return text[:240]
}

func keyPoints(text string) []string {
	splitter := regexp.MustCompile(`[。.!?\n]+`)
	raw := splitter.Split(text, -1)
	ret := []string{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) > 160 {
			item = item[:160]
		}
		ret = append(ret, item)
		if len(ret) >= 3 {
			break
		}
	}
	return ret
}

func inferTags(text string) []string {
	lower := strings.ToLower(text)
	candidates := map[string][]string{
		"ai":                   {"ai", "llm", "embedding", "model", "openai", "deepseek"},
		"knowledge-management": {"knowledge", "note", "markdown", "obsidian", "logseq", "search"},
		"engineering":          {"go", "golang", "api", "http", "duckdb", "git"},
		"research":             {"paper", "study", "analysis", "summary"},
	}
	ret := []string{}
	for tag, words := range candidates {
		for _, word := range words {
			if strings.Contains(lower, word) {
				ret = append(ret, tag)
				break
			}
		}
	}
	sort.Strings(ret)
	return ret
}

func localEmbedding(text string, dimension int) []float64 {
	vector := make([]float64, dimension)
	words := regexp.MustCompile(`[a-zA-Z0-9_\-\p{Han}]+`).FindAllString(strings.ToLower(text), -1)
	for _, word := range words {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(word))
		value := hash.Sum32()
		idx := int(value % uint32(dimension))
		weight := 1.0
		if value&1 == 1 {
			weight = -1
		}
		vector[idx] += weight
	}
	norm := 0.0
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		return vector
	}
	norm = math.Sqrt(norm)
	for idx := range vector {
		vector[idx] = vector[idx] / norm
	}
	return vector
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

type refineJSON struct {
	Summary   string   `json:"summary"`
	KeyPoints []string `json:"key_points"`
	Tags      []string `json:"tags"`
	Title     string   `json:"title"`
}

type topicWeaveJSON struct {
	Document string `json:"document"`
}

type synthesisJSON struct {
	Content string `json:"content"`
}

func extractJSONObject(value string) string {
	value = strings.TrimSpace(value)
	start := strings.Index(value, "{")
	end := strings.LastIndex(value, "}")
	if start >= 0 && end >= start {
		return value[start : end+1]
	}
	return value
}

func renderSynthesisInputs(snapshots []models.ThoughtSnapshot) string {
	parts := []string{}
	for _, snapshot := range snapshots {
		title := firstNonEmpty(snapshot.Thought.DisplayTitle, snapshot.Thought.UserTitle, snapshot.Thought.ExtractedTitle, snapshot.Thought.ID)
		body := firstNonEmpty(snapshot.Thought.Summary, snapshot.Content.AINotes, snapshot.Content.ExtractedContent, snapshot.Content.Original)
		parts = append(parts, "ID: "+snapshot.Thought.ID+
			"\nTitle: "+title+
			"\nSource link: "+snapshot.Thought.Path+
			"\nContent:\n"+body)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func ensureSynthesisSourceLinks(content string, sourceLinks []string) string {
	content = strings.TrimSpace(content)
	missing := []string{}
	for _, link := range sourceLinks {
		link = strings.TrimSpace(link)
		if link == "" || strings.Contains(content, link) {
			continue
		}
		missing = append(missing, link)
	}
	if len(missing) == 0 {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	if strings.Contains(strings.ToLower(content), "### sources") {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n### Sources\n\n")
	}
	for _, link := range missing {
		builder.WriteString("- [[")
		builder.WriteString(link)
		builder.WriteString("]]\n")
	}
	return strings.TrimSpace(builder.String())
}

func normalizeList(values []string) []string {
	seen := map[string]struct{}{}
	ret := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ret = append(ret, value)
	}
	sort.Strings(ret)
	return ret
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func localInitialTopicDocument(topic models.Topic) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("id: ")
	builder.WriteString(topic.ID)
	builder.WriteString("\ntype: topic\nmembers: []\n---\n\n# ")
	builder.WriteString(topic.Name)
	builder.WriteString("\n")
	if strings.TrimSpace(topic.Description) != "" {
		builder.WriteString("\n")
		builder.WriteString(strings.TrimSpace(topic.Description))
		builder.WriteString("\n")
	}
	for _, node := range topic.Outline {
		if strings.TrimSpace(node.Title) != "" {
			builder.WriteString("\n## ")
			builder.WriteString(strings.TrimSpace(node.Title))
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func localThoughtSection(req models.TopicWeaveRequest, outlineTitle string) string {
	title := firstNonEmpty(req.Thought.DisplayTitle, req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID)
	body := firstNonEmpty(req.Thought.Summary, firstLine(req.Content.AINotes), firstLine(req.Content.ExtractedContent), firstLine(req.Content.Original))
	heading := "## "
	if outlineTitle != "" {
		heading = "### "
	}
	var builder strings.Builder
	builder.WriteString(heading)
	builder.WriteString(title)
	builder.WriteString("\n\n")
	if body != "" {
		builder.WriteString(body)
		builder.WriteString("\n\n")
	}
	if len(req.Membership.Reasons) > 0 {
		builder.WriteString("Match: ")
		builder.WriteString(strings.Join(req.Membership.Reasons, ", "))
		builder.WriteString("\n\n")
	}
	builder.WriteString("> Sources: [[")
	builder.WriteString(req.SourceLink)
	builder.WriteString("]]")
	return builder.String()
}

func chooseOutlineSection(req models.TopicWeaveRequest) string {
	if len(req.Topic.Outline) == 0 {
		return ""
	}
	searchText := strings.ToLower(strings.Join([]string{
		req.Thought.UserTitle,
		req.Thought.ExtractedTitle,
		req.Thought.Summary,
		req.Content.Original,
		req.Content.ExtractedContent,
		req.Content.AINotes,
		strings.Join(req.Membership.Reasons, " "),
	}, "\n"))
	bestTitle := ""
	bestScore := 0
	for _, node := range req.Topic.Outline {
		title := strings.TrimSpace(node.Title)
		if title == "" {
			continue
		}
		score := 0
		for _, token := range outlineTokens(title) {
			if strings.Contains(searchText, token) {
				score++
			}
		}
		if score > bestScore {
			bestTitle = title
			bestScore = score
		}
	}
	if bestTitle != "" {
		return bestTitle
	}
	return strings.TrimSpace(req.Topic.Outline[0].Title)
}

func insertIntoOutlineSection(document string, outlineTitle string, section string) string {
	marker := "## " + strings.TrimSpace(outlineTitle)
	start := strings.Index(document, marker)
	if start < 0 {
		return strings.TrimRight(document, "\n") + "\n\n" + section
	}
	bodyStart := start + len(marker)
	next := strings.Index(document[bodyStart:], "\n## ")
	if next < 0 {
		return strings.TrimRight(document, "\n") + "\n\n" + section
	}
	insertAt := bodyStart + next
	before := strings.TrimRight(document[:insertAt], "\n")
	after := strings.TrimLeft(document[insertAt:], "\n")
	return before + "\n\n" + section + "\n\n" + after
}

func outlineTokens(title string) []string {
	raw := regexp.MustCompile(`[a-zA-Z0-9_\-\p{Han}]+`).FindAllString(strings.ToLower(title), -1)
	ret := []string{}
	for _, token := range raw {
		if len([]rune(token)) <= 1 {
			continue
		}
		ret = append(ret, token)
	}
	return ret
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	line := strings.TrimSpace(strings.Split(value, "\n")[0])
	if len(line) > 240 {
		return line[:240]
	}
	return line
}
